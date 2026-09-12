package agentinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"prism/internal/integrations"
)

type JobState string

const (
	StateIdle        JobState = "idle"
	StateRunning     JobState = "running"
	StateInstalling  JobState = "installing"
	StateVerifying   JobState = "verifying"
	StateSucceeded   JobState = "succeeded"
	StateFailed      JobState = "failed"
	StateUnsupported JobState = "unsupported"
	StateInterrupted JobState = "interrupted"
)

const (
	// outputTailCap bounds the combined output a job keeps in memory and
	// reports; installers are chatty but only the tail explains outcomes.
	outputTailCap = 4096
	// verifyTimeout bounds each post-install `binary --version` probe;
	// verifyRetry is the one longer chance for slow first-run bootstraps.
	verifyTimeout = 5 * time.Second
	verifyRetry   = 60 * time.Second
	// jobTimeout bounds one whole install/update run.
	jobTimeout = 15 * time.Minute
)

type Job struct {
	Key       string     `json:"agent"`
	Op        string     `json:"op"`
	State     JobState   `json:"state"`
	Method    string     `json:"method,omitempty"`
	Command   string     `json:"command,omitempty"`
	Output    string     `json:"output,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type AgentStatus struct {
	ID        integrations.ID `json:"id"`
	Key       string          `json:"key"`
	Installed bool            `json:"installed"`
	Source    Source          `json:"source"`
	Path      string          `json:"path,omitempty"`
	CanUpdate bool            `json:"canUpdate"`
	Reason    string          `json:"reason,omitempty"`
	Job       Job             `json:"job"`
}

// ErrInstallActive refuses a new job while one is running for the same
// binary (opencode and opencode2 are two ids of one shared binary).
var ErrInstallActive = errors.New("agentinstall: an install or update job is already active for this binary")

type clockFunc func() time.Time

type Manager struct {
	mu     sync.Mutex
	jobs   map[string]*Job
	active map[string]string // guard binary -> key of the running job
	env    integrations.Env
	stat   func(string) (os.FileInfo, error)
	eval   func(string) (string, error)
	runner Runner
	now    clockFunc
	// fetchScript downloads an install script to a temp file; the seam
	// keeps manager tests network-free.
	fetchScript func(ctx context.Context, url string) (string, error)
	// jobTimeout is the per-run budget; overridable only in tests.
	jobTimeout time.Duration
	// verifyTimeout and verifyRetry bound the post-install version probe;
	// overridable only in tests.
	verifyTimeout time.Duration
	verifyRetry   time.Duration
	// cancels holds per-guard-binary cancel funcs so Stop can interrupt runs.
	cancels map[string]func()
	// wg tracks running job goroutines so Stop can wait for final states.
	wg sync.WaitGroup
}

func NewManager(env integrations.Env, runner Runner, stat func(string) (os.FileInfo, error), now clockFunc, fetchScript func(ctx context.Context, url string) (string, error)) *Manager {
	return &Manager{
		jobs:          make(map[string]*Job),
		active:        make(map[string]string),
		cancels:       make(map[string]func()),
		env:           env,
		stat:          stat,
		eval:          filepath.EvalSymlinks,
		runner:        runner,
		now:           now,
		fetchScript:   fetchScript,
		jobTimeout:    jobTimeout,
		verifyTimeout: verifyTimeout,
		verifyRetry:   verifyRetry,
	}
}

// guardBinary is the identity the active-job lock uses.
func (d Definition) guardBinary() string {
	if d.GuardBinary != "" {
		return d.GuardBinary
	}
	return d.Binary
}

// StatusAll derives every agent's status from PATH in integrations.IDs order.
func (m *Manager) StatusAll() []AgentStatus {
	out := make([]AgentStatus, 0, len(integrations.IDs))
	for _, id := range integrations.IDs {
		if st, ok := m.StatusOf(id); ok {
			out = append(out, st)
		}
	}
	return out
}

func (m *Manager) StatusOf(id integrations.ID) (AgentStatus, bool) {
	def, ok := definitionByID(id)
	if !ok {
		return AgentStatus{}, false
	}
	return m.status(def), true
}

func (m *Manager) status(def Definition) AgentStatus {
	path := LookPath(asEnv(m.env), def.Binary, m.stat)
	st := AgentStatus{
		ID:  def.IDs[0],
		Key: def.Key,
		Job: m.jobOf(def.Key),
	}
	if path == "" {
		st.Reason = "binary " + def.Binary + " not found on PATH"
		return st
	}
	st.Installed = true
	st.Path = path
	st.Source = m.classify(path)
	switch st.Source {
	case SourceUnknown:
		st.Reason = "installed from an unrecognized location; update is managed outside Prism"
	default:
		st.CanUpdate = true
	}
	return st
}

// classify resolves how a binary was installed. npm/pnpm link prefix/bin
// into node_modules trees, so the symlink-followed path decides those; but
// script installers (codex, claude) symlink ~/.local/bin/<name> into private
// version dirs, so an unrecognized target falls back to the PATH entry
// itself, which still names the script layout (found live 2026-09-09).
func (m *Manager) classify(path string) Source {
	if source := DetectSource(resolveSymlinks(path, m.eval)); source != SourceUnknown {
		return source
	}
	return DetectSource(path)
}

func (m *Manager) JobOf(id integrations.ID) Job {
	def, ok := definitionByID(id)
	if !ok {
		return Job{}
	}
	return m.jobOf(def.Key)
}

func (m *Manager) jobOf(key string) Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job, ok := m.jobs[key]; ok {
		return *job
	}
	return Job{Key: key, State: StateIdle}
}

func (m *Manager) begin(def Definition, op string) (*Job, context.Context, func(), error) {
	guard := def.guardBinary()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, busy := m.active[guard]; busy {
		return nil, nil, nil, fmt.Errorf("%w (binary %s)", ErrInstallActive, guard)
	}
	now := m.now()
	job := &Job{
		Key:       def.Key,
		Op:        op,
		State:     StateRunning,
		StartedAt: &now,
		UpdatedAt: &now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.active[guard] = def.Key
	m.cancels[guard] = cancel
	m.jobs[def.Key] = job
	return job, ctx, cancel, nil
}

func (m *Manager) finish(def Definition, cancel func()) {
	cancel()
	guard := def.guardBinary()
	m.mu.Lock()
	delete(m.active, guard)
	delete(m.cancels, guard)
	m.mu.Unlock()
	m.wg.Done()
}

// transition mutates a job under the manager lock and returns the post-write
// copy, so the launching goroutine can hand back a consistent snapshot while
// the runner goroutine is already live.
func (m *Manager) transition(job *Job, state JobState, method, command, output, errMsg string) Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	job.State = state
	job.UpdatedAt = &now
	if method != "" {
		job.Method = method
	}
	if command != "" {
		job.Command = command
	}
	if output != "" {
		job.Output = tailCap(output)
	}
	if errMsg != "" {
		job.Error = errMsg
	}
	return *job
}

// Install launches an install job for the agent behind id. It returns the
// initial job snapshot; the run continues on its own goroutine.
func (m *Manager) Install(id integrations.ID, force bool) (Job, error) {
	def, ok := definitionByID(id)
	if !ok {
		return Job{}, fmt.Errorf("agentinstall: unknown agent id %q", id)
	}
	plan, _, found := resolveInstallPlan(def.Key, runtime.GOOS, asEnv(m.env), m.stat)
	if !found {
		now := m.now()
		job := &Job{
			Key:       def.Key,
			Op:        "install",
			State:     StateUnsupported,
			StartedAt: &now,
			UpdatedAt: &now,
			Error:     "no install plan is executable in this environment (no known package manager or script tool on PATH)",
		}
		m.mu.Lock()
		m.jobs[def.Key] = job
		m.mu.Unlock()
		return *job, nil
	}
	if plan.Script != nil {
		return m.launchScript(def, "install", string(plan.Method), plan.Script)
	}
	return m.launch(def, "install", plan, force)
}

// Update launches an update job for the agent behind id.
func (m *Manager) Update(id integrations.ID) (Job, error) {
	def, ok := definitionByID(id)
	if !ok {
		return Job{}, fmt.Errorf("agentinstall: unknown agent id %q", id)
	}
	if err := m.checkActive(def); err != nil {
		return Job{}, err
	}
	path := LookPath(asEnv(m.env), def.Binary, m.stat)
	if path == "" {
		return Job{}, ErrNotInstalled
	}
	source := m.classify(path)
	plan := resolveUpdatePlan(def.Key, source, runtime.GOOS, asEnv(m.env), m.stat)
	if plan.Unsupported != "" {
		now := m.now()
		job := &Job{
			Key:       def.Key,
			Op:        "update",
			State:     StateUnsupported,
			StartedAt: &now,
			UpdatedAt: &now,
			Error:     plan.Unsupported,
		}
		m.mu.Lock()
		m.jobs[def.Key] = job
		m.mu.Unlock()
		return *job, nil
	}
	if plan.Script != nil {
		return m.launchScript(def, "update", string(MethodScript), plan.Script)
	}
	return m.launchArgv(def, "update", "", plan.Command)
}

func (m *Manager) checkActive(def Definition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, busy := m.active[def.guardBinary()]; busy {
		return fmt.Errorf("%w (binary %s)", ErrInstallActive, def.guardBinary())
	}
	return nil
}

// ErrNotInstalled refuses an update when the binary is absent from PATH.
var ErrNotInstalled = errors.New("agentinstall: agent is not installed")

func (m *Manager) launch(def Definition, op string, plan Plan, force bool) (Job, error) {
	return m.launchArgv(def, op, string(plan.Method), plan.argv(force))
}

func (m *Manager) launchArgv(def Definition, op, method string, argv []string) (Job, error) {
	job, ctx, cancel, err := m.begin(def, op)
	if err != nil {
		return Job{}, err
	}
	command := strings.Join(argv, " ")
	snapshot := m.transition(job, StateInstalling, method, command, "", "")
	m.wg.Add(1)
	go m.runJob(def, job, ctx, cancel, argv)
	return snapshot, nil
}

// launchScript starts a job that fetches the script to a temp file and runs
// it with the plan's interpreter. Interpreters cannot open URLs, so the
// runner sees [interpreter, tempPath] while the job's command keeps the
// readable "interpreter URL" form.
func (m *Manager) launchScript(def Definition, op, method string, script *Script) (Job, error) {
	job, ctx, cancel, err := m.begin(def, op)
	if err != nil {
		return Job{}, err
	}
	snapshot := m.transition(job, StateInstalling, method, script.Interpreter+" "+script.URL, "", "")
	m.wg.Add(1)
	go m.runScriptJob(def, job, ctx, cancel, script)
	return snapshot, nil
}

func (m *Manager) runJob(def Definition, job *Job, ctx context.Context, cancel func(), argv []string) {
	defer m.finish(def, cancel)
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, m.jobTimeout)
	defer timeoutCancel()
	m.execJob(def, job, ctx, timeoutCtx, argv)
}

func (m *Manager) runScriptJob(def Definition, job *Job, ctx context.Context, cancel func(), script *Script) {
	defer m.finish(def, cancel)
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, m.jobTimeout)
	defer timeoutCancel()
	path, err := m.fetchScript(timeoutCtx, script.URL)
	switch {
	case ctx.Err() != nil:
		m.transition(job, StateInterrupted, "", "", "", "canceled by daemon shutdown")
		return
	case errors.Is(timeoutCtx.Err(), context.DeadlineExceeded):
		m.transition(job, StateFailed, "", "", "", "job timed out after 15 minutes")
		return
	case err != nil:
		m.transition(job, StateFailed, "", "", "", "fetch script: "+err.Error())
		return
	}
	defer os.Remove(path)
	m.execJob(def, job, ctx, timeoutCtx, []string{script.Interpreter, path})
}

func (m *Manager) execJob(def Definition, job *Job, ctx, timeoutCtx context.Context, argv []string) {
	var buf bytes.Buffer
	err := m.runner.Run(timeoutCtx, m.env, argv, &buf)
	output := buf.String()
	switch {
	case ctx.Err() != nil:
		m.transition(job, StateInterrupted, "", "", output, "canceled by daemon shutdown")
		return
	case errors.Is(timeoutCtx.Err(), context.DeadlineExceeded):
		m.transition(job, StateFailed, "", "", output, "job timed out after 15 minutes")
		return
	case err != nil:
		m.transition(job, StateFailed, "", "", output, err.Error())
		return
	}
	m.transition(job, StateVerifying, "", "", output, "")
	if verr := m.verify(ctx, def); verr != nil {
		if ctx.Err() != nil {
			m.transition(job, StateInterrupted, "", "", output, "canceled by daemon shutdown")
			return
		}
		m.transition(job, StateFailed, "", "", output, "verify: "+verr.Error())
		return
	}
	m.transition(job, StateSucceeded, "", "", output, "")
}

// verify probes the installed binary's version. Some agents bootstrap a
// runtime on first run and outlive the quick probe (hermes: ~11s cold,
// ~0.1s warm), so a probe that died on its own deadline gets exactly one
// longer chance before the job fails.
func (m *Manager) verify(ctx context.Context, def Definition) error {
	timedOut, err := m.probe(ctx, def, m.verifyTimeout)
	if err == nil || ctx.Err() != nil || !timedOut {
		return err
	}
	_, err = m.probe(ctx, def, m.verifyRetry)
	return err
}

func (m *Manager) probe(ctx context.Context, def Definition, timeout time.Duration) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var buf bytes.Buffer
	argv := []string{def.Binary, def.VerifyArg}
	if err := m.runner.Run(probeCtx, m.env, argv, &buf); err != nil {
		timedOut := probeCtx.Err() != nil && ctx.Err() == nil
		return timedOut, fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return false, nil
}

// Stop cancels every active job's context and waits for the job goroutines
// to record their final state, bounded by the passed ctx (the daemon's
// shutdown window).
func (m *Manager) Stop(ctx context.Context) {
	m.mu.Lock()
	cancels := make([]func(), 0, len(m.cancels))
	for _, cancel := range m.cancels {
		cancels = append(cancels, cancel)
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func tailCap(s string) string {
	if len(s) <= outputTailCap {
		return s
	}
	return s[len(s)-outputTailCap:]
}

func asEnv(env integrations.Env) integrationsEnv { return envAdapter{env: env} }
