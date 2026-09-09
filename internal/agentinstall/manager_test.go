package agentinstall

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/integrations"
)

// newTestManager builds a manager over a fake environment: /bin holds the
// tools named in tools; the clock is deterministic; script fetches land in
// a canned temp path.
func newTestManager(tools ...string) (*Manager, *fakeRunner) {
	paths := make([]string, 0, len(tools))
	for _, tool := range tools {
		paths = append(paths, "/bin/"+tool)
	}
	runner := &fakeRunner{behaviors: map[string]func(ctx context.Context, out io.Writer) error{}}
	env := integrations.Env{"PATH": "/bin"}
	m := NewManager(env, runner, statWith(paths...).stat, testNow, fetchOK)
	return m, runner
}

// fetchOK is the network-free script-fetch seam: it records nothing and
// hands back a canned path.
func fetchOK(ctx context.Context, url string) (string, error) {
	return "/tmp/prism-fetched.sh", nil
}

func TestInstallLifecycleSucceeds(t *testing.T) {
	m, runner := newTestManager("npm")
	job, err := m.Install(integrations.Pi, false)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateInstalling {
		t.Fatalf("initial state = %q, want installing", job.State)
	}
	if job.Method != string(MethodNpm) {
		t.Fatalf("method = %q, want npm", job.Method)
	}
	final := waitTerminal(t, m, "pi", time.Second)
	if final.State != StateSucceeded {
		t.Fatalf("final state = %q, want succeeded (err %q)", final.State, final.Error)
	}
	if final.Op != "install" {
		t.Errorf("op = %q, want install", final.Op)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want 2 (install + verify)", len(runner.calls))
	}
	verify := runner.calls[1]
	if strings.Join(verify.argv, " ") != "pi --version" {
		t.Errorf("verify argv = %v, want [pi --version]", verify.argv)
	}
}

func TestInstallLifecycleVerifyFail(t *testing.T) {
	m, runner := newTestManager("npm")
	runner.behaviors["pi --version"] = func(ctx context.Context, out io.Writer) error {
		return errors.New("exit status 127")
	}
	if _, err := m.Install(integrations.Pi, false); err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, m, "pi", time.Second)
	if final.State != StateFailed {
		t.Fatalf("state = %q, want failed", final.State)
	}
	if !strings.Contains(final.Error, "verify") {
		t.Errorf("error = %q, want verify mention", final.Error)
	}
}

func TestInstallLifecycleRunFail(t *testing.T) {
	m, runner := newTestManager("npm")
	runner.behaviors["npm install -g @earendil-works/pi-coding-agent"] = func(ctx context.Context, out io.Writer) error {
		io.WriteString(out, "npm ERR! network")
		return errors.New("exit status 1")
	}
	if _, err := m.Install(integrations.Pi, false); err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, m, "pi", time.Second)
	if final.State != StateFailed {
		t.Fatalf("state = %q, want failed", final.State)
	}
	if final.Error != "exit status 1" {
		t.Errorf("error = %q, want exit status 1", final.Error)
	}
	if !strings.Contains(final.Output, "npm ERR! network") {
		t.Errorf("output = %q, want it to carry the run output", final.Output)
	}
}

func TestInstallUnsupportedWhenNoTool(t *testing.T) {
	m, _ := newTestManager()
	job, err := m.Install(integrations.Pi, false)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateUnsupported {
		t.Fatalf("state = %q, want unsupported", job.State)
	}
	if job.Error == "" {
		t.Error("unsupported job must carry a reason")
	}
}

// TestErrInstallActiveSharedBinary: opencode and opencode2 are two ids of one
// binary; a job started through one id must refuse the other.
func TestErrInstallActiveSharedBinary(t *testing.T) {
	m, runner := newTestManager("npm", "opencode", "opencode2")
	block := make(chan struct{})
	runner.behaviors["npm install -g opencode-ai@latest"] = func(ctx context.Context, out io.Writer) error {
		<-block
		return nil
	}
	if _, err := m.Install(integrations.Opencode, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(integrations.Opencode2, false); !errors.Is(err, ErrInstallActive) {
		t.Fatalf("second install err = %v, want ErrInstallActive", err)
	}
	if _, err := m.Update(integrations.Opencode2); !errors.Is(err, ErrInstallActive) {
		t.Fatalf("update while active err = %v, want ErrInstallActive", err)
	}
	close(block)
	waitTerminal(t, m, "opencode", time.Second)
	// Guard released: a new job through the sibling id now starts.
	if _, err := m.Install(integrations.Opencode2, false); err != nil {
		t.Fatalf("install after release: %v", err)
	}
	waitTerminal(t, m, "opencode2", time.Second)
}

// TestOutputTailCap: a 10KB installer transcript reports only the last 4096
// bytes.
func TestOutputTailCap(t *testing.T) {
	m, runner := newTestManager("npm")
	big := strings.Repeat("x", 10*1024)
	runner.behaviors["npm install -g @earendil-works/pi-coding-agent"] = func(ctx context.Context, out io.Writer) error {
		io.WriteString(out, big)
		return nil
	}
	if _, err := m.Install(integrations.Pi, false); err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, m, "pi", time.Second)
	if len(final.Output) != outputTailCap {
		t.Fatalf("output len = %d, want %d", len(final.Output), outputTailCap)
	}
	if !strings.HasSuffix(final.Output, strings.Repeat("x", 10)) {
		t.Error("output must be the tail, not the head")
	}
}

// TestStopMarksInterrupted: Stop cancels the running job's context; the
// runner observes it and the job lands in interrupted.
func TestStopMarksInterrupted(t *testing.T) {
	m, runner := newTestManager("npm")
	runner.behaviors["npm install -g @earendil-works/pi-coding-agent"] = func(ctx context.Context, out io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := m.Install(integrations.Pi, false); err != nil {
		t.Fatal(err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go m.Stop(stopCtx)
	final := waitTerminal(t, m, "pi", time.Second)
	if final.State != StateInterrupted {
		t.Fatalf("state = %q, want interrupted", final.State)
	}
	cancel()
}

// TestJobTimeoutFails: a run that outlives the job budget fails with the
// timeout error; the budget is shortened so the test races milliseconds.
func TestJobTimeoutFails(t *testing.T) {
	m, runner := newTestManager("npm")
	m.jobTimeout = 50 * time.Millisecond
	runner.behaviors["npm install -g @earendil-works/pi-coding-agent"] = func(ctx context.Context, out io.Writer) error {
		<-ctx.Done()
		return errors.New("signal: killed")
	}
	if _, err := m.Install(integrations.Pi, false); err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, m, "pi", 2*time.Second)
	if final.State != StateFailed {
		t.Fatalf("state = %q, want failed (err %q)", final.State, final.Error)
	}
	if !strings.Contains(final.Error, "timed out") {
		t.Errorf("error = %q, want timeout mention", final.Error)
	}
}

// TestJobOfIdleWhenNeverRan: the job route reports idle, not zero.
func TestJobOfIdleWhenNeverRan(t *testing.T) {
	m, _ := newTestManager()
	job := m.JobOf(integrations.Grok)
	if job.State != StateIdle {
		t.Fatalf("state = %q, want idle", job.State)
	}
	if job.Key != "grok" {
		t.Errorf("key = %q, want grok", job.Key)
	}
}

func TestUpdateRefusesWhenNotInstalled(t *testing.T) {
	m, _ := newTestManager("npm")
	_, err := m.Update(integrations.Grok)
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
}

func TestUpdateScriptSelfUpdate(t *testing.T) {
	stat := statWith("/Users/x/.grok/downloads/grok")
	env := integrations.Env{"PATH": "/Users/x/.grok/downloads"}
	runner := &fakeRunner{behaviors: map[string]func(ctx context.Context, out io.Writer) error{}}
	m := NewManager(env, runner, stat.stat, testNow, fetchOK)
	job, err := m.Update(integrations.Grok)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateInstalling {
		t.Fatalf("state = %q, want installing", job.State)
	}
	if job.Command != "grok update" {
		t.Errorf("command = %q, want grok update", job.Command)
	}
	final := waitTerminal(t, m, "grok", time.Second)
	if final.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded (err %q)", final.State, final.Error)
	}
}

func TestUpdateUnsupportedForUnknownSource(t *testing.T) {
	// claude at a path no rule matches (versions dir, not via ~/.local/bin).
	stat := statWith("/bin/claude", "/Users/x/.local/share/claude/versions/1.0.32/claude")
	env := integrations.Env{"PATH": "/bin"}
	runner := &fakeRunner{behaviors: map[string]func(ctx context.Context, out io.Writer) error{}}
	m := NewManager(env, runner, stat.stat, testNow, fetchOK)
	job, err := m.Update(integrations.Claude)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateUnsupported {
		t.Fatalf("state = %q, want unsupported", job.State)
	}
	if !strings.Contains(job.Error, "outside Prism") {
		t.Errorf("error = %q, want outside-Prism mention", job.Error)
	}
}

// TestStatusAllDerivesFromPath: installed/source/canUpdate follow PATH and
// the source rules; job snapshots ride along.
func TestStatusAllDerivesFromPath(t *testing.T) {
	stat := statWith(
		"/Users/x/.local/bin/codex",
		"/Users/x/.opencode/bin/opencode",
		"/usr/local/Cellar/grok/1.0/bin/grok",
	)
	env := integrations.Env{"PATH": "/Users/x/.local/bin:/Users/x/.opencode/bin:/usr/local/Cellar/grok/1.0/bin"}
	m := NewManager(env, &fakeRunner{behaviors: map[string]func(ctx context.Context, out io.Writer) error{}}, stat.stat, testNow, fetchOK)
	statuses := m.StatusAll()
	if len(statuses) != len(integrations.IDs) {
		t.Fatalf("statuses = %d, want %d", len(statuses), len(integrations.IDs))
	}
	byID := map[integrations.ID]AgentStatus{}
	for _, st := range statuses {
		byID[st.ID] = st
	}
	codex := byID[integrations.Codex]
	if !codex.Installed || codex.Source != SourceScript || !codex.CanUpdate {
		t.Errorf("codex status = %+v, want installed/script/canUpdate", codex)
	}
	if codex.Path != "/Users/x/.local/bin/codex" {
		t.Errorf("codex path = %q, want the script path (PATH order)", codex.Path)
	}
	opencode := byID[integrations.Opencode]
	if !opencode.Installed || opencode.Source != SourceScript {
		t.Errorf("opencode status = %+v", opencode)
	}
	grok := byID[integrations.Grok]
	if !grok.Installed || grok.Source != SourceBrew {
		t.Errorf("grok status = %+v, want installed/brew", grok)
	}
	// opencode2 shares no binary with opencode here: absent.
	opencode2 := byID[integrations.Opencode2]
	if opencode2.Installed {
		t.Errorf("opencode2 status = %+v, want not installed", opencode2)
	}
	if opencode2.CanUpdate || opencode2.Reason == "" {
		t.Errorf("opencode2 must carry a not-installed reason, got %+v", opencode2)
	}
	// Job rides along as idle.
	if codex.Job.State != StateIdle {
		t.Errorf("codex job state = %q, want idle", codex.Job.State)
	}
}

func TestStatusOfUnknownID(t *testing.T) {
	m, _ := newTestManager()
	if _, ok := m.StatusOf(integrations.ID("nope")); ok {
		t.Fatal("unknown id must not resolve")
	}
}

// TestForceForwardsToArgv: --force lands in the runner argv for npm.
func TestForceForwardsToArgv(t *testing.T) {
	m, runner := newTestManager("npm")
	if _, err := m.Install(integrations.Pi, true); err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, m, "pi", time.Second)
	install := runner.calls[0]
	want := "npm install -g @earendil-works/pi-coding-agent --force"
	if got := strings.Join(install.argv, " "); got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// TestConcurrentInstallsDifferentBinaries: distinct binaries run in parallel
// without tripping the guard.
func TestConcurrentInstallsDifferentBinaries(t *testing.T) {
	m, runner := newTestManager("npm")
	var wg sync.WaitGroup
	runner.behaviors["npm install -g hermes-agent"] = func(ctx context.Context, out io.Writer) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	runner.behaviors["npm install -g @earendil-works/pi-coding-agent"] = func(ctx context.Context, out io.Writer) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	for _, id := range []integrations.ID{integrations.Hermes, integrations.Pi} {
		wg.Add(1)
		go func(id integrations.ID) {
			defer wg.Done()
			if _, err := m.Install(id, false); err != nil {
				t.Errorf("install %s: %v", id, err)
			}
		}(id)
	}
	wg.Wait()
	waitTerminal(t, m, "hermes", time.Second)
	waitTerminal(t, m, "pi", time.Second)
}

// TestInstallScriptFetchesThenRuns: a script plan resolves (bash on PATH),
// the daemon fetches the script, and the runner execs the local copy —
// never a URL argv, which bash cannot open (found live 2026-09-09).
func TestInstallScriptFetchesThenRuns(t *testing.T) {
	m, runner := newTestManager("bash")
	fetched := make(chan string, 1)
	m.fetchScript = func(ctx context.Context, url string) (string, error) {
		if url != "https://x.ai/cli/install.sh" {
			t.Errorf("fetched url = %q, want the grok install script", url)
		}
		fetched <- url
		return "/tmp/prism-fetched.sh", nil
	}
	job, err := m.Install(integrations.Grok, false)
	if err != nil {
		t.Fatal(err)
	}
	if job.Method != string(MethodScript) {
		t.Fatalf("method = %q, want script", job.Method)
	}
	if job.Command != "bash https://x.ai/cli/install.sh" {
		t.Fatalf("command = %q, want the readable interpreter URL form", job.Command)
	}
	final := waitTerminal(t, m, "grok", time.Second)
	if final.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded (err %q)", final.State, final.Error)
	}
	select {
	case <-fetched:
	default:
		t.Fatal("install script was never fetched")
	}
	install := runner.calls[0]
	if got := strings.Join(install.argv, " "); got != "bash /tmp/prism-fetched.sh" {
		t.Errorf("install argv = %q, want the fetched local copy", got)
	}
}

// TestInstallScriptFetchFails: a dead script URL fails the job with a fetch
// prefix instead of a confusing interpreter exit.
func TestInstallScriptFetchFails(t *testing.T) {
	m, _ := newTestManager("bash")
	m.fetchScript = func(ctx context.Context, url string) (string, error) {
		return "", errors.New("GET https://x.ai/cli/install.sh: 404 Not Found")
	}
	if _, err := m.Install(integrations.Grok, false); err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, m, "grok", time.Second)
	if final.State != StateFailed {
		t.Fatalf("state = %q, want failed", final.State)
	}
	if !strings.HasPrefix(final.Error, "fetch script: ") {
		t.Errorf("error = %q, want fetch script prefix", final.Error)
	}
}

// TestUpdateScriptRerunFetches: opencode2 (no self-update argv) updates by
// rerunning its fetched install script.
func TestUpdateScriptRerunFetches(t *testing.T) {
	m, runner := newTestManager("bash")
	stat := statWith("/Users/x/.opencode/bin/opencode2")
	m.stat = stat.stat
	m.env = integrations.Env{"PATH": "/Users/x/.opencode/bin"}
	m.fetchScript = func(ctx context.Context, url string) (string, error) {
		if url != "https://opencode.ai/install" {
			t.Errorf("fetched url = %q, want the opencode install script", url)
		}
		return "/tmp/prism-fetched.sh", nil
	}
	job, err := m.Update(integrations.Opencode2)
	if err != nil {
		t.Fatal(err)
	}
	if job.Command != "bash https://opencode.ai/install" {
		t.Fatalf("command = %q, want script rerun", job.Command)
	}
	final := waitTerminal(t, m, "opencode2", time.Second)
	if final.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded (err %q)", final.State, final.Error)
	}
	if got := strings.Join(runner.calls[0].argv, " "); got != "bash /tmp/prism-fetched.sh" {
		t.Errorf("update argv = %q, want the fetched local copy", got)
	}
}

// TestVerifySlowFirstRunSucceeds: hermes-class agents bootstrap a runtime on
// first run (~11s live) and outlive the quick probe, so one longer retry
// saves an otherwise-successful install (found live 2026-09-09).
func TestVerifySlowFirstRunSucceeds(t *testing.T) {
	m, runner := newTestManager("npm")
	m.verifyTimeout = 5 * time.Millisecond
	m.verifyRetry = time.Second
	var probes int32
	runner.behaviors["pi --version"] = func(ctx context.Context, out io.Writer) error {
		if atomic.AddInt32(&probes, 1) == 1 {
			<-ctx.Done()
			return errors.New("signal: killed")
		}
		return nil
	}
	if _, err := m.Install(integrations.Pi, false); err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, m, "pi", 2*time.Second)
	if final.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded (err %q)", final.State, final.Error)
	}
	if atomic.LoadInt32(&probes) != 2 {
		t.Fatalf("probes = %d, want 2 (quick kill + long retry)", probes)
	}
}

// TestVerifySlowEveryRunFails: a binary that never answers fails after the
// retry too, with the verify error carrying the killed probe.
func TestVerifySlowEveryRunFails(t *testing.T) {
	m, runner := newTestManager("npm")
	m.verifyTimeout = 5 * time.Millisecond
	m.verifyRetry = 20 * time.Millisecond
	runner.behaviors["pi --version"] = func(ctx context.Context, out io.Writer) error {
		<-ctx.Done()
		return errors.New("signal: killed")
	}
	if _, err := m.Install(integrations.Pi, false); err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, m, "pi", 2*time.Second)
	if final.State != StateFailed {
		t.Fatalf("state = %q, want failed", final.State)
	}
	if !strings.Contains(final.Error, "verify") || !strings.Contains(final.Error, "killed") {
		t.Errorf("error = %q, want verify + killed", final.Error)
	}
}

// TestClassifyFallsBackToPathEntry: codex's script installer symlinks
// ~/.local/bin/codex into a private version dir; the resolved target is
// unrecognized, so classification falls back to the PATH entry itself
// (found live 2026-09-09).
func TestClassifyFallsBackToPathEntry(t *testing.T) {
	link := "/Users/x/.local/bin/codex"
	target := "/Users/x/.codex/bin/codex-1.2.3"
	env := pathEnv("/Users/x/.local/bin")
	m := NewManager(env, &fakeRunner{}, statWith(link).stat, testNow, fetchOK)
	m.eval = func(p string) (string, error) {
		if p != link {
			return "", os.ErrNotExist
		}
		return target, nil
	}
	st, ok := m.StatusOf(integrations.Codex)
	if !ok || !st.Installed {
		t.Fatalf("status = %+v, want installed", st)
	}
	if st.Source != SourceScript || !st.CanUpdate {
		t.Fatalf("source = %q canUpdate = %v, want script/true", st.Source, st.CanUpdate)
	}
}
