package agentinstall

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
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
	outputTailCap = 4096
	verifyTimeout = 5 * time.Second
	verifyRetry   = 60 * time.Second
	jobTimeout    = 15 * time.Minute
)

type Job struct {
	Key             string     `json:"agent"`
	Op              string     `json:"op"`
	State           JobState   `json:"state"`
	Method          string     `json:"method,omitempty"`
	Command         string     `json:"command,omitempty"`
	Output          string     `json:"output,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	UpdatedAt       *time.Time `json:"updatedAt,omitempty"`
	Error           string     `json:"error,omitempty"`
	BeforeVersion   string     `json:"beforeVersion,omitempty"`
	ExpectedVersion string     `json:"expectedVersion,omitempty"`
	Version         string     `json:"version,omitempty"`
	Verification    string     `json:"verification,omitempty"`
	Note            string     `json:"note,omitempty"`
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

var ErrInstallActive = errors.New("agentinstall: an install or update job is already active for this binary or global root")
var ErrNotInstalled = errors.New("agentinstall: agent is not installed")

type clockFunc func() time.Time

type Manager struct {
	mu            sync.Mutex
	discoveryMu   sync.Mutex
	lifetime      context.Context
	stopProbes    context.CancelFunc
	jobs          map[string]*Job
	active        map[string]string
	roots         map[string]string
	stopped       bool
	env           integrations.Env
	stat          func(string) (os.FileInfo, error)
	eval          func(string) (string, error)
	runner        Runner
	now           clockFunc
	fetchScript   func(context.Context, string) (string, error)
	jobTimeout    time.Duration
	verifyTimeout time.Duration
	verifyRetry   time.Duration
	cancels       map[string]func()
	wg            sync.WaitGroup
	versions      map[string]versionObservation
	destinations  map[string][]string
	observations  map[string]installation
	http          *http.Client
}

func NewManager(env integrations.Env, runner Runner, stat func(string) (os.FileInfo, error), now clockFunc, fetchScript func(context.Context, string) (string, error), releaseHTTP *http.Client) *Manager {
	lifetime, stopProbes := context.WithCancel(context.Background())
	return &Manager{
		lifetime:      lifetime,
		stopProbes:    stopProbes,
		jobs:          make(map[string]*Job),
		versions:      make(map[string]versionObservation),
		destinations:  make(map[string][]string),
		observations:  make(map[string]installation),
		active:        make(map[string]string),
		roots:         make(map[string]string),
		cancels:       make(map[string]func()),
		env:           copyEnv(env),
		stat:          stat,
		eval:          filepath.EvalSymlinks,
		runner:        runner,
		http:          releaseHTTP,
		now:           now,
		fetchScript:   fetchScript,
		jobTimeout:    jobTimeout,
		verifyTimeout: verifyTimeout,
		verifyRetry:   verifyRetry,
	}
}
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
	m.discoveryMu.Lock()
	defer m.discoveryMu.Unlock()
	m.mu.Lock()
	_, busy := m.active[def.Binary]
	obs := m.observations[def.Key]
	m.mu.Unlock()
	if !busy {
		obs = m.observe(def)
	}
	st := AgentStatus{ID: def.IDs[0], Key: def.Key, Job: m.jobOf(def.Key), Installed: obs.entry != "", Path: obs.entry, Source: obs.source, Reason: obs.reason}
	if !busy && obs.entry != "" && obs.reason == "" {
		m.mu.Lock()
		_, rootBusy := m.roots[obs.target.lockRoot()]
		m.mu.Unlock()
		if rootBusy {
			st.Reason = "installation root is busy"
			return st
		}
		_, err := m.prepare(m.lifetime, def, obs, "update", false)
		st.CanUpdate = err == nil
		if err != nil {
			st.Reason = err.Error()
		}
	}
	return st
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
func (m *Manager) admission(def Definition) error {
	if m.stopped {
		return errors.New("agentinstall: manager stopped")
	}
	if _, busy := m.active[def.Binary]; busy {
		return fmt.Errorf("%w (binary %s)", ErrInstallActive, def.Binary)
	}
	return nil
}
func (m *Manager) checkActive(def Definition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.admission(def)
}
func (t installTarget) lockRoot() string {
	if t.source == SourceNpm || t.source == SourceBun || t.source == SourceBrew || t.source == SourcePnpm || t.archiveTarget != "" {
		return t.root
	}
	return ""
}
func (m *Manager) begin(def Definition, op string, target installTarget) (*Job, context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.admission(def); err != nil {
		return nil, nil, nil, err
	}
	root := target.lockRoot()
	if owner, busy := m.roots[root]; root != "" && busy {
		return nil, nil, nil, fmt.Errorf("%w (global root %s is used by %s)", ErrInstallActive, root, owner)
	}
	now := m.now()
	job := &Job{Key: def.Key, Op: op, State: StateRunning, StartedAt: &now, UpdatedAt: &now}
	ctx, cancel := context.WithCancel(context.Background())
	m.active[def.Binary] = def.Key
	m.cancels[def.Binary] = cancel
	m.jobs[def.Key] = job
	if root != "" {
		m.roots[root] = def.Binary
	}
	m.wg.Add(1)
	return job, ctx, cancel, nil
}
func (m *Manager) transition(job *Job, state JobState, method, command, output, errMsg string) Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.apply(job, state, method, command, output, errMsg)
}
func (m *Manager) complete(def Definition, job *Job, target installTarget, state JobState, output, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apply(job, state, "", "", output, errMsg)
	delete(m.active, def.Binary)
	delete(m.cancels, def.Binary)
	delete(m.roots, target.lockRoot())
}
func (m *Manager) apply(job *Job, state JobState, method, command, output, errMsg string) Job {
	now := m.now()
	job.State = state
	job.UpdatedAt = &now
	if method != "" {
		job.Method = method
	}
	if command != "" {
		job.Command = redactSecrets(command)
	}
	if output != "" {
		job.Output = tailCap(redactSecrets(output))
	}
	if errMsg != "" {
		job.Error = tailCap(redactSecrets(errMsg))
	}
	return *job
}
func (m *Manager) unsupported(def Definition, op, reason string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.admission(def); err != nil {
		return Job{}, err
	}
	now := m.now()
	job := &Job{Key: def.Key, Op: op, State: StateUnsupported, StartedAt: &now, UpdatedAt: &now, Error: tailCap(redactSecrets(reason))}
	m.jobs[def.Key] = job
	return *job, nil
}
func (m *Manager) Install(id integrations.ID, force bool) (Job, error) {
	return m.start(id, "install", force)
}
func (m *Manager) Update(id integrations.ID) (Job, error) { return m.start(id, "update", false) }
func (m *Manager) start(id integrations.ID, op string, force bool) (Job, error) {
	m.discoveryMu.Lock()
	defer m.discoveryMu.Unlock()
	def, ok := definitionByID(id)
	if !ok {
		return Job{}, fmt.Errorf("agentinstall: unknown agent id %q", id)
	}
	if err := m.checkActive(def); err != nil {
		return Job{}, err
	}
	obs := m.observe(def)
	m.mu.Lock()
	owner, rootBusy := m.roots[obs.target.lockRoot()]
	m.mu.Unlock()
	if rootBusy {
		return Job{}, fmt.Errorf("%w (root used by %s)", ErrInstallActive, owner)
	}
	if op == "update" && obs.entry == "" {
		return Job{}, ErrNotInstalled
	}
	action, err := m.prepare(m.lifetime, def, obs, op, force)
	if err != nil {
		return m.unsupported(def, op, err.Error())
	}
	m.mu.Lock()
	m.observations[def.Key] = obs
	m.destinations[def.Key] = append(m.destinations[def.Key], filepath.Dir(action.target.entry))
	m.mu.Unlock()
	job, ctx, cancel, err := m.begin(def, op, action.target)
	if err != nil {
		return Job{}, err
	}
	command := strings.Join(action.argv, " ")
	if action.script != nil {
		command = strings.Join(append([]string{action.script.Interpreter, action.script.URL}, action.script.Args...), " ")
	}
	snapshot := m.transition(job, StateRunning, string(action.method), command, "", "")
	go m.runJob(def, job, ctx, cancel, action)
	return snapshot, nil
}
func (m *Manager) jobError(ctx, deadline context.Context, err error) (JobState, string) {
	if ctx.Err() != nil {
		return StateInterrupted, "canceled by daemon shutdown"
	}
	if errors.Is(deadline.Err(), context.DeadlineExceeded) {
		return StateFailed, "job timed out"
	}
	if err != nil {
		return StateFailed, err.Error()
	}
	return "", ""
}
func (m *Manager) runJob(def Definition, job *Job, ctx context.Context, cancel func(), action preparedAction) {
	defer m.wg.Done()
	defer cancel()
	state, output, reason := m.execute(def, job, ctx, action)
	m.complete(def, job, action.target, state, output, reason)
}
func (m *Manager) execute(def Definition, job *Job, ctx context.Context, action preparedAction) (JobState, string, string) {
	deadline, timeoutCancel := context.WithTimeout(ctx, m.jobTimeout)
	defer timeoutCancel()
	check := releaseCheck{verification: "installed"}
	if job.Op == "update" {
		var err error
		check, err = m.checkRelease(deadline, def, action)
		m.evidence(job, check, "")
		if state, reason := m.jobError(ctx, deadline, err); state != "" {
			return state, "", "release check: " + reason
		}
		if check.current || (check.expected != "" && compareVersions(check.before, check.expected) >= 0) {
			m.evidence(job, check, check.before)
			return StateSucceeded, "already current", ""
		}
	}
	if action.method == MethodArchive {
		m.transition(job, StateInstalling, "", "", "", "")
		err := m.updateStandalone(deadline, def, action.target, check, job)
		if state, reason := m.jobError(ctx, deadline, err); state != "" {
			return state, "", reason
		}
		return StateSucceeded, "verified archive activated", ""
	}
	argv := action.argv
	if action.script != nil {
		path, err := m.fetchScript(deadline, action.script.URL)
		if state, reason := m.jobError(ctx, deadline, err); state != "" {
			if err != nil && ctx.Err() == nil && deadline.Err() == nil {
				reason = "fetch script: " + reason
			}
			return state, "", reason
		}
		defer os.Remove(path)
		argv = append([]string{action.script.Interpreter, path}, action.script.Args...)
	}
	if !action.fresh {
		obs := m.observeEntry(def, action.target.entry)
		if obs.reason != "" || obs.target != action.target {
			return StateFailed, "", "selected owner changed before mutation"
		}
	}
	m.transition(job, StateInstalling, "", "", "", "")
	out := &boundedOutput{limit: outputTailCap}
	stdout, stderr := &diagnosticStream{tail: out}, &diagnosticStream{tail: out}
	err := m.runner.Run(deadline, action.env, argv, stdout, stderr)
	stdout.flush()
	stderr.flush()
	if state, reason := m.jobError(ctx, deadline, err); state != "" {
		return state, out.String(), reason
	}
	m.transition(job, StateVerifying, "", "", out.String(), "")
	if action.fresh && action.script != nil {
		action.target, err = m.publishedTarget(def, action.publication)
	}
	if err == nil {
		version, verifyErr := m.verifyVersion(deadline, def, action.target)
		m.evidence(job, check, version)
		err = verifyErr
		if err == nil && check.expected != "" && compareVersions(version, check.expected) < 0 {
			err = fmt.Errorf("verification returned %s, older than expected %s", version, check.expected)
		}
	}
	if state, reason := m.jobError(ctx, deadline, err); state != "" {
		if err != nil && ctx.Err() == nil && deadline.Err() == nil {
			reason = "verify: " + reason
		}
		return state, out.String(), reason
	}
	return StateSucceeded, out.String(), ""
}

func (m *Manager) evidence(job *Job, check releaseCheck, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job.BeforeVersion, job.ExpectedVersion, job.Version, job.Verification, job.Note = check.before, check.expected, version, check.verification, check.note
}

func (m *Manager) verifyVersion(ctx context.Context, def Definition, target installTarget) (string, error) {
	obs := m.observeEntry(def, target.entry)
	if obs.reason != "" || obs.target != target {
		return "", fmt.Errorf("installation owner changed or cannot be proven at %s", target.entry)
	}
	version, timedOut, err := m.versionAt(ctx, def, target.entry, m.verifyTimeout)
	if err != nil && ctx.Err() == nil && timedOut {
		version, _, err = m.versionAt(ctx, def, target.entry, m.verifyRetry)
	}
	if err != nil {
		return version, err
	}
	obs = m.observeEntry(def, target.entry)
	if obs.entry != target.entry || obs.reason != "" || obs.target != target {
		return version, fmt.Errorf("installation owner changed during version probe at %s", target.entry)
	}
	return version, nil
}
func (m *Manager) versionAt(ctx context.Context, def Definition, entry string, timeout time.Duration) (string, bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, stderr := &boundedOutput{limit: outputTailCap}, &boundedOutput{limit: outputTailCap}
	argv := []string{entry, def.VerifyArg}
	if err := m.runner.Run(probeCtx, maintenanceEnv(m.env, "probe", entry), argv, out, stderr); err != nil {
		return "", errors.Is(probeCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil, fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	text := out.String()
	if strings.TrimSpace(text) == "" {
		text = stderr.String()
	}
	version := parsedVersion(text, false)
	if out.overflow || stderr.overflow || version == "" {
		return "", false, fmt.Errorf("%s returned no valid bounded version", entry)
	}
	return version, false, nil
}
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.stopped = true
	m.stopProbes()
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
		m.discoveryMu.Lock()
		m.discoveryMu.Unlock()
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("maintenance cleanup incomplete: %w", ctx.Err())
	}
}
func tailCap(s string) string {
	if len(s) <= outputTailCap {
		return s
	}
	return s[len(s)-outputTailCap:]
}
func asEnv(env integrations.Env) integrationsEnv { return envAdapter{env: env} }
