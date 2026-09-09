package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/agentinstall"
	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/integrations"
	"prism/internal/management"
	"prism/internal/provider"
	"prism/internal/quota"
)

// fakePool adapts a static snapshot to the management pool interface.
type fakePool struct {
	accounts []account.Account
	paused   map[string]bool
	deleted  map[string]bool
}

func (p *fakePool) Acquire(ctx context.Context, req account.AcquireRequest) (account.Lease, error) {
	return account.Lease{}, fmt.Errorf("not used")
}

func (p *fakePool) Record(ctx context.Context, l account.Lease, o account.Outcome) error {
	return nil
}

func (p *fakePool) Pause(ctx context.Context, id account.AccountID, v account.StateVersion) error {
	if p.paused == nil {
		p.paused = map[string]bool{}
	}
	p.paused[string(id)] = true
	return nil
}

func (p *fakePool) Resume(ctx context.Context, id account.AccountID, v account.StateVersion) error {
	if p.paused == nil {
		p.paused = map[string]bool{}
	}
	p.paused[string(id)] = false
	return nil
}

func (p *fakePool) UpdatePriority(ctx context.Context, id account.AccountID, prio int, v account.StateVersion) error {
	return nil
}

func (p *fakePool) Snapshot() account.Snapshot {
	return account.Snapshot{Accounts: append([]account.Account(nil), p.accounts...)}
}

func (p *fakePool) DeleteAccount(ctx context.Context, id account.AccountID) error {
	if p.deleted == nil {
		p.deleted = map[string]bool{}
	}
	p.deleted[string(id)] = true
	return nil
}

type fakeCreds struct {
	store map[string][]byte
}

func (c *fakeCreds) Put(ctx context.Context, id string, secret []byte) error {
	c.store[id] = secret
	return nil
}

func (c *fakeCreds) Delete(ctx context.Context, id string) error {
	delete(c.store, id)
	return nil
}

func (c *fakeCreds) Configured(ctx context.Context, id string) (bool, error) {
	_, ok := c.store[id]
	return ok, nil
}

type fakeCatalog struct {
	models []provider.Model
	err    error
}

func (c *fakeCatalog) Models(ctx context.Context) ([]provider.Model, error) {
	return c.models, c.err
}

type fakeQuota struct {
	// known accounts; others return account.ErrNotFound like the real source.
	known map[account.AccountID]bool
}

func (q *fakeQuota) Quota(ctx context.Context, id account.AccountID) (quota.Snapshot, error) {
	if q.known != nil && !q.known[id] {
		return quota.Snapshot{}, account.ErrNotFound
	}
	return quota.Snapshot{Used: 42, Limit: ptrInt64(100), WindowEnd: time.Unix(1_700_000_000, 0).UTC(), Source: quota.SourceEndpoint}, nil
}

func (q *fakeQuota) RefreshQuota(ctx context.Context, id account.AccountID) (quota.Snapshot, error) {
	return q.Quota(ctx, id)
}

func ptrInt64(v int64) *int64 { return &v }

type fakeAuthSvc struct {
	start  auth.AuthStart
	status map[string]auth.AuthStatus
	// onStatus, when set, overrides the status map; poll tests use it to
	// drive deterministic state transitions.
	onStatus func(sid string) auth.AuthStatus
	// sessions started so far, in order
	started []string
}

func (a *fakeAuthSvc) Start(ctx context.Context, p account.ProviderID) (auth.AuthStart, error) {
	sid := fmt.Sprintf("sess-%d", len(a.started)+1)
	a.started = append(a.started, sid)
	a.start.Session = auth.AuthSessionID(sid)
	return a.start, nil
}

func (a *fakeAuthSvc) Complete(ctx context.Context, p account.ProviderID, cb auth.AuthCallback) error {
	return nil
}

func (a *fakeAuthSvc) Status(ctx context.Context, p account.ProviderID, s auth.AuthSessionID) (auth.AuthStatus, error) {
	if s == "" {
		return auth.AuthStatus{State: auth.StatusUnauthorized}, nil
	}
	if a.onStatus != nil {
		return a.onStatus(string(s)), nil
	}
	if st, ok := a.status[string(s)]; ok {
		return st, nil
	}
	return auth.AuthStatus{State: auth.StatusPending}, nil
}

// daemonEnv spins a real management Server behind httptest with a sandbox
// config — full round-trip through the daemon's routes, shared DTOs.
type daemonEnv struct {
	ts         *httptest.Server
	rt         *cliRuntime
	stdout     bytes.Buffer
	stderr     bytes.Buffer
	cfg        *config.Manager
	pool       *fakePool
	auth       *fakeAuthSvc
	registry   *integrations.Registry
	sandboxDir string
}

func newDaemonEnv(t *testing.T, extra func(env *daemonEnv)) *daemonEnv {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Seed one provider + one combo + one route so mutation commands have state.
	if _, err := cfg.Update(seedDocument(), 0); err != nil {
		t.Fatal(err)
	}
	pool := &fakePool{accounts: []account.Account{
		{
			ID:       "codex:acc-1",
			Provider: "codex",
			State:    account.Active,
			Priority: 1,
			Version:  3,
			CredGen:  2,
			Quota: quota.Snapshot{
				Used:      120,
				Limit:     ptrInt64(500),
				WindowEnd: time.Unix(1_700_000_000, 0).UTC(),
				Source:    quota.SourceHeader,
			},
			InFlight: 1,
		},
		{
			ID:       "codex:acc-2",
			Provider: "codex",
			State:    account.Paused,
			Priority: 2,
			Version:  5,
		},
	}}
	fake := &fakeAuthSvc{start: auth.AuthStart{URL: "https://idp.example.com/authorize?client_id=x&state=st-1"}}
	quotaSrc := &fakeQuota{known: map[account.AccountID]bool{"codex:acc-1": true, "codex:acc-2": true}}
	registry := integrations.NewRegistry()
	srv := management.New(pool, cfg, &fakeCatalog{models: []provider.Model{
		{ID: "codex/gpt-5.3", Alias: "gpt-5.3-codex", Caps: provider.ModelCaps{Reasoning: true}},
		{ID: "codex/gpt-5.2"},
	}}, quotaSrc, &fakeCreds{store: map[string][]byte{}}, fake, registry, nil, agentsManagerForTest(t))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	env := &daemonEnv{
		ts:         ts,
		cfg:        cfg,
		pool:       pool,
		auth:       fake,
		registry:   registry,
		sandboxDir: dir,
	}
	env.rt = &cliRuntime{
		baseURL: ts.URL,
		env:     map[string]string{},
		stdout:  &env.stdout,
		stderr:  &env.stderr,
		client:  newClient(ts.URL),
	}
	if extra != nil {
		extra(env)
	}
	return env
}

func seedDocument() config.Document {
	return config.Document{
		Version: config.SchemaVersion,
		Daemon:  config.Daemon{Listen: "127.0.0.1:8787"},
		Providers: map[string]config.Provider{
			"codex":       {Wire: config.WireCodex, Models: []string{"gpt-5.3", "gpt-5.2"}, Pool: &config.PoolSettings{Strategy: config.PoolQuota}},
			"custom-resp": {Wire: config.WireOpenAIResponses, BaseURL: "http://127.0.0.1:9000/v1", Models: []string{"m-a"}},
		},
		Combos: map[string]config.Combo{
			"combo-1": {
				Targets:  []config.Target{{Provider: "codex", Model: "gpt-5.3"}, {Provider: "codex", Model: "gpt-5.2", Weight: 2}},
				Strategy: config.ComboFailover,
			},
		},
		Routes: map[string]string{"gpt-5.3": "codex/gpt-5.3"},
	}
}

// runCLI parses and executes one command line against the env, returning the
// process exit code, stdout, and stderr.
func (e *daemonEnv) runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	e.stdout.Reset()
	e.stderr.Reset()
	cmd, err := parseCommand(args)
	if err != nil {
		if ue, ok := err.(*usageError); ok {
			fmt.Fprintf(&e.stderr, "prismctl: %v\n\n%s", ue.msg, ue.help)
		} else {
			fmt.Fprintf(&e.stderr, "prismctl: %v", err)
		}
		return exitUsage, e.stdout.String(), e.stderr.String()
	}
	ctx := context.Background()
	err = cmd.run(ctx, e.rt)
	if err == nil {
		return exitOK, e.stdout.String(), e.stderr.String()
	}
	if ee, ok := err.(*exitError); ok {
		fmt.Fprintf(&e.stderr, "prismctl: %s\n", ee.msg)
		return ee.code, e.stdout.String(), e.stderr.String()
	}
	fmt.Fprintf(&e.stderr, "prismctl: %v\n", err)
	return exitFailure, e.stdout.String(), e.stderr.String()
}

// decodeJSON is a test helper for --json output verification.
func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("json output not a JSON object: %v\n%s", err, s)
	}
	return m
}

func decodeJSONArray(t *testing.T, s string) []any {
	t.Helper()
	var a []any
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		t.Fatalf("json output not a JSON array: %v\n%s", err, s)
	}
	return a
}

// --- core lifecycle ---

func TestStatusHumanAndJSON(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, errOut := env.runCLI(t, "status")
	if code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "status: ok") {
		t.Fatalf("human status missing ok: %q", out)
	}
	code, out, _ = env.runCLI(t, "status", "--json")
	if code != exitOK {
		t.Fatalf("json code=%d", code)
	}
	m := decodeJSON(t, out)
	if m["health"] == nil || m["providers"] == nil || m["accounts"] == nil {
		t.Fatalf("status json missing fields: %v", m)
	}
}

func TestDoctorReportsConfigProblems(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, _ := env.runCLI(t, "doctor")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "daemon: ok") {
		t.Fatalf("doctor output: %q", out)
	}
	code, out, _ = env.runCLI(t, "doctor", "--json")
	if code != exitOK {
		t.Fatalf("json code=%d", code)
	}
	m := decodeJSON(t, out)
	if _, ok := m["problems"]; !ok {
		t.Fatalf("doctor json missing problems: %v", m)
	}
}

func TestUsageTableAndJSON(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, _ := env.runCLI(t, "usage")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "codex:acc-1") {
		t.Fatalf("usage table missing account: %q", out)
	}
	code, out, _ = env.runCLI(t, "usage", "--json")
	if code != exitOK {
		t.Fatalf("json code=%d", code)
	}
	if !strings.Contains(out, "\"accounts\"") {
		t.Fatalf("usage json: %q", out)
	}
}

func TestStatsRenderAndJSON(t *testing.T) {
	fixture := `{
	  "range": "24h",
	  "overview": {
	    "requests": 12, "completed": 10, "failed": 2,
	    "input_tokens": 1000, "output_tokens": 500, "cached_tokens": 300,
	    "reasoning_tokens": 50, "total_tokens": 1800, "measured": 9
	  },
	  "models": [
	    {"model": "gpt-5.3", "provider": "codex",
	     "requests": 12, "completed": 10, "failed": 2,
	     "input_tokens": 1000, "output_tokens": 500, "cached_tokens": 300,
	     "reasoning_tokens": 50, "total_tokens": 1800, "measured": 9}
	  ],
	  "providers": [
	    {"provider": "codex",
	     "requests": 12, "completed": 10, "failed": 2,
	     "input_tokens": 1000, "output_tokens": 500, "cached_tokens": 300,
	     "reasoning_tokens": 50, "total_tokens": 1800, "measured": 9}
	  ]
	}`
	var gotRange string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stats" {
			http.NotFound(w, r)
			return
		}
		gotRange = r.URL.Query().Get("range")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fixture)
	}))
	t.Cleanup(ts.Close)
	env := &daemonEnv{stdout: bytes.Buffer{}, stderr: bytes.Buffer{}}
	env.rt = &cliRuntime{
		baseURL: ts.URL,
		env:     map[string]string{},
		stdout:  &env.stdout,
		stderr:  &env.stderr,
		client:  newClient(ts.URL),
	}

	code, out, _ := env.runCLI(t, "stats")
	if code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, env.stderr.String())
	}
	if gotRange != "24h" {
		t.Fatalf("default range = %q, want 24h", gotRange)
	}
	for _, want := range []string{"codex", "gpt-5.3", "measured: 9/12", "1800"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stats output missing %q:\n%s", want, out)
		}
	}

	code, out, _ = env.runCLI(t, "stats", "--range", "7d", "--json")
	if code != exitOK {
		t.Fatalf("json code=%d stderr=%s", code, env.stderr.String())
	}
	m := decodeJSON(t, out)
	if m["range"] != "24h" {
		// The fixture always echoes 24h; the assertion proves --json is
		// the raw server body, not a CLI re-rendering.
		t.Fatalf("json range = %v, want fixture value 24h", m["range"])
	}
	if !strings.Contains(out, "\"total_tokens\": 1800") {
		t.Fatalf("json body missing snake_case totals:\n%s", out)
	}
}

// --- exit codes ---

func TestDaemonUnreachableExitCode(t *testing.T) {
	rt := &cliRuntime{
		baseURL: "http://127.0.0.1:1",
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
		client:  newClient("http://127.0.0.1:1"),
	}
	env := &daemonEnv{rt: rt, stdout: bytes.Buffer{}, stderr: bytes.Buffer{}}
	env.rt.stdout = &env.stdout
	env.rt.stderr = &env.stderr
	code, _, errOut := env.runCLI(t, "status")
	if code != exitUnreach {
		t.Fatalf("code=%d want %d stderr=%s", code, exitUnreach, errOut)
	}
	if !strings.Contains(errOut, "unreachable") {
		t.Fatalf("stderr: %q", errOut)
	}
}

func TestMalformedServerJSONExitCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "this is not json <")
	}))
	t.Cleanup(ts.Close)
	rt := &cliRuntime{
		baseURL: ts.URL,
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
		client:  newClient(ts.URL),
	}
	env := &daemonEnv{rt: rt, stdout: bytes.Buffer{}, stderr: bytes.Buffer{}}
	env.rt.stdout = &env.stdout
	env.rt.stderr = &env.stderr
	code, _, errOut := env.runCLI(t, "status")
	if code != exitMalformed {
		t.Fatalf("code=%d want %d stderr=%s", code, exitMalformed, errOut)
	}
	if !strings.Contains(errOut, "malformed") {
		t.Fatalf("stderr: %q", errOut)
	}
}

func TestStaleGenerationConflictExitCode(t *testing.T) {
	// 409 stale_generation from the daemon must map to exit 3 with a re-run hint.
	env := newDaemonEnv(t, nil)
	c := newClient(env.ts.URL)
	_, err := c.routesPut(context.Background(), "newkey", management.RouteWrite{Value: "codex/gpt-5.3", ExpectedGeneration: 99})
	if err == nil {
		t.Fatal("expected stale generation error")
	}
	ee, ok := err.(*exitError)
	if !ok {
		t.Fatalf("want exitError, got %T %v", err, err)
	}
	if ee.code != exitConflict {
		t.Fatalf("exit code %d, want %d", ee.code, exitConflict)
	}
	if !strings.Contains(ee.msg, "re-run") {
		t.Fatalf("missing re-run hint: %q", ee.msg)
	}
}

func TestNotFoundExitCodeAndMessage(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, _, errOut := env.runCLI(t, "accounts", "quota", "no-such-account")
	if code != exitFailure {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "not_found") {
		t.Fatalf("stderr: %q", errOut)
	}
}

func TestBadJSONBodyExitCode(t *testing.T) {
	env := newDaemonEnv(t, nil)
	// daemon 400 malformed_json surfaces code+message; CLI maps to exit 1.
	c := newClient(env.ts.URL)
	err := c.call(context.Background(), http.MethodPost, "/api/v1/accounts/codex:acc-1/pause", "not-json", new(any))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "malformed_json") {
		t.Fatalf("err: %v", err)
	}
}

// --- parse discipline ---

func TestParseFailsBeforeNetwork(t *testing.T) {
	// Usage errors exit 2 and never touch the daemon.
	env := newDaemonEnv(t, nil)
	for _, args := range [][]string{
		{"bogus-command"},
		{"auth"},
		{"auth", "login", "unknown-provider"},
		{"accounts"},
		{"accounts", "distribute", "codex", "bogus"},
		{"providers", "add", "x"},
		{"combos", "set", "x"},
		{"integrations", "apply", "not-a-client"},
		{"integrations", "rollback", "codex", "--force"},
		{"status", "extra-arg"},
		{"routes", "set", "only-key"},
		{"stats", "--range", "bogus"},
	} {
		before := len(env.pool.paused)
		code, _, _ := env.runCLI(t, args...)
		if code != exitUsage {
			t.Errorf("%v: code=%d want 2", args, code)
		}
		if len(env.pool.paused) != before {
			t.Errorf("%v: mutated state during parse failure", args)
		}
	}
}

func TestHelpDeterministic(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out1, _ := env.runCLI(t, "help")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	code, out2, _ := env.runCLI(t, "help")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if out1 != out2 {
		t.Fatal("help output not deterministic")
	}
	if !strings.Contains(out1, "Usage: prismctl") {
		t.Fatalf("help: %q", out1)
	}
}

// --- accounts ---

func TestAccountsListTable(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, _ := env.runCLI(t, "accounts", "list")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "codex:acc-1") || !strings.Contains(out, "paused") {
		t.Fatalf("table: %q", out)
	}
	code, out, _ = env.runCLI(t, "accounts", "list", "--json")
	if code != exitOK {
		t.Fatalf("json code=%d", code)
	}
	m := decodeJSON(t, out)
	accs, ok := m["accounts"].([]any)
	if !ok || len(accs) != 2 {
		t.Fatalf("accounts json: %v", m)
	}
}

func TestAccountsPauseResumePriority(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, errOut := env.runCLI(t, "accounts", "pause", "codex:acc-1")
	if code != exitOK {
		t.Fatalf("pause code=%d stderr=%s out=%s", code, errOut, out)
	}
	if !env.pool.paused["codex:acc-1"] {
		t.Fatal("pause did not reach pool")
	}
	code, _, _ = env.runCLI(t, "accounts", "resume", "codex:acc-1")
	if code != exitOK {
		t.Fatalf("resume code=%d", code)
	}
	code, out, _ = env.runCLI(t, "accounts", "priority", "codex:acc-1", "7")
	if code != exitOK {
		t.Fatalf("priority code=%d", code)
	}
	if !strings.Contains(out, "priority") {
		t.Fatalf("priority output: %q", out)
	}
}

func TestAccountsQuotaAndRemove(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, _ := env.runCLI(t, "accounts", "quota", "codex:acc-1")
	if code != exitOK {
		t.Fatalf("quota code=%d", code)
	}
	if !strings.Contains(out, "used: 42") {
		t.Fatalf("quota output: %q", out)
	}
	code, out, _ = env.runCLI(t, "accounts", "remove", "codex:acc-2", "--json")
	if code != exitOK {
		t.Fatalf("remove code=%d out=%s", code, out)
	}
	if !env.pool.deleted["codex:acc-2"] {
		t.Fatal("remove did not reach pool")
	}
}

func TestAccountPolicyCommandsPreservePoolFields(t *testing.T) {
	env := newDaemonEnv(t, nil)
	// select pins an account; distribute and affinity change other pool fields;
	// each must preserve what the previous command set.
	if code, _, errOut := env.runCLI(t, "accounts", "select", "codex", "codex:acc-1"); code != exitOK {
		t.Fatalf("select code=%d stderr=%s", code, errOut)
	}
	if code, _, errOut := env.runCLI(t, "accounts", "distribute", "codex", "round-robin"); code != exitOK {
		t.Fatalf("distribute code=%d stderr=%s", code, errOut)
	}
	if code, _, errOut := env.runCLI(t, "accounts", "affinity", "codex", "off"); code != exitOK {
		t.Fatalf("affinity code=%d stderr=%s", code, errOut)
	}
	if code, _, errOut := env.runCLI(t, "accounts", "auto-switch", "codex", "off", "--threshold", "0.9"); code != exitOK {
		t.Fatalf("auto-switch code=%d stderr=%s", code, errOut)
	}
	doc := env.cfg.Get().Config
	p := doc.Providers["codex"]
	if p.Pool == nil {
		t.Fatal("pool nil after policy commands")
	}
	if p.Pool.PinnedAccount != "codex:acc-1" {
		t.Fatalf("pin lost: %+v", p.Pool)
	}
	if p.Pool.Strategy != config.PoolRoundRobin {
		t.Fatalf("strategy lost: %+v", p.Pool)
	}
	if p.Pool.Affinity != config.AffinityOff {
		t.Fatalf("affinity lost: %+v", p.Pool)
	}
	if p.Pool.AutoSwitch == nil || *p.Pool.AutoSwitch {
		t.Fatal("autoSwitch lost")
	}
	if p.Pool.AutoSwitchThreshold != 0.9 {
		t.Fatalf("threshold lost: %+v", p.Pool)
	}
	// unmentioned fields must survive too (models on the provider).
	if len(p.Models) != 2 {
		t.Fatalf("models erased: %+v", p)
	}
}

func TestAccountSelectCrossProviderRefused(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, _, errOut := env.runCLI(t, "accounts", "select", "codex", "antigravity:other")
	if code != exitUsage && code != exitFailure {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
}

// --- providers ---

func TestProvidersListAndAddEditRoundTrip(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, _ := env.runCLI(t, "providers", "list")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "codex") || !strings.Contains(out, "custom-resp") {
		t.Fatalf("providers table: %q", out)
	}
	// add with credential from file
	credFile := filepath.Join(env.sandboxDir, "cred.txt")
	if err := os.WriteFile(credFile, []byte("  sk-secret-do-not-print  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := env.runCLI(t, "providers", "add", "newchat", "--wire", "chat",
		"--endpoint", "http://127.0.0.1:9000/v1", "--model", "m-x",
		"--credential-file", credFile)
	if code != exitOK {
		t.Fatalf("add code=%d stderr=%s", code, errOut)
	}
	// credential must be stored but never printed
	list, err := env.clientList(t)
	if err != nil {
		t.Fatal(err)
	}
	var stored *management.Provider
	for i := range list.Providers {
		if list.Providers[i].ID == "newchat" {
			stored = &list.Providers[i]
		}
	}
	if stored == nil {
		t.Fatal("newchat not in providers")
	}
	if stored.Credential.State != "set" {
		t.Fatalf("credential state: %+v", stored.Credential)
	}
	// secret never appears in any CLI output produced so far
	if strings.Contains(env.stdout.String(), "sk-secret") {
		t.Fatal("secret leaked to stdout")
	}
	// edit: change endpoint only; models and credential state must persist.
	code, _, errOut = env.runCLI(t, "providers", "edit", "newchat",
		"--endpoint", "http://127.0.0.1:9001/v1")
	if code != exitOK {
		t.Fatalf("edit code=%d stderr=%s", code, errOut)
	}
	list, _ = env.clientList(t)
	for _, pr := range list.Providers {
		if pr.ID == "newchat" {
			if pr.BaseURL != "http://127.0.0.1:9001/v1" {
				t.Fatalf("endpoint not updated: %+v", pr)
			}
			if len(pr.Models) != 1 || pr.Models[0] != "m-x" {
				t.Fatalf("models erased by edit: %+v", pr)
			}
			if pr.Credential.State != "set" {
				t.Fatalf("credential lost by edit: %+v", pr)
			}
		}
	}
}

func (e *daemonEnv) clientList(t *testing.T) (management.ProvidersResponse, error) {
	t.Helper()
	return e.rt.client.providersList(context.Background())
}

func TestProvidersEnableDisableRoundTrip(t *testing.T) {
	env := newDaemonEnv(t, nil)
	if code, _, errOut := env.runCLI(t, "providers", "disable", "codex"); code != exitOK {
		t.Fatalf("disable code=%d stderr=%s", code, errOut)
	}
	list, _ := env.clientList(t)
	for _, pr := range list.Providers {
		if pr.ID == "codex" {
			if pr.Enabled == nil || *pr.Enabled {
				t.Fatalf("enabled not persisted: %+v", pr)
			}
			if len(pr.Models) != 2 {
				t.Fatalf("models erased by disable: %+v", pr)
			}
		}
	}
	if code, _, errOut := env.runCLI(t, "providers", "enable", "codex"); code != exitOK {
		t.Fatalf("enable code=%d stderr=%s", code, errOut)
	}
	list, _ = env.clientList(t)
	for _, pr := range list.Providers {
		if pr.ID == "codex" && (pr.Enabled == nil || !*pr.Enabled) {
			t.Fatalf("enable not persisted: %+v", pr)
		}
	}
}

func TestProvidersRemove(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, _ := env.runCLI(t, "providers", "remove", "custom-resp")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "removed") {
		t.Fatalf("remove output: %q", out)
	}
	list, _ := env.clientList(t)
	for _, pr := range list.Providers {
		if pr.ID == "custom-resp" {
			t.Fatal("provider still present")
		}
	}
}

func TestProviderCredentialFromStdinNotArgv(t *testing.T) {
	newDaemonEnv(t, nil)
	// --stdin and --credential-file are mutually exclusive at parse time;
	// the secret-bearing store path is exercised with a file credential in
	// TestProvidersListAndAddEditRoundTrip.
	_, err := parseCommand([]string{"providers", "add", "x", "--wire", "chat", "--stdin", "--credential-file", "f"})
	if err == nil {
		t.Fatal("expected parse failure for stdin+credential-file")
	}
}

// --- models ---

func TestModelsListAndToggle(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, _ := env.runCLI(t, "models", "list")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "codex/gpt-5.3") {
		t.Fatalf("models table: %q", out)
	}
	code, out, errOut := env.runCLI(t, "models", "list", "--json")
	if code != exitOK {
		t.Fatalf("json code=%d stderr=%s", code, errOut)
	}
	var models management.ModelsResponse
	if err := json.Unmarshal([]byte(out), &models); err != nil {
		t.Fatalf("models json: %v output=%q", err, out)
	}
	if len(models.Models) != 2 || strings.Contains(out, `"rows"`) {
		t.Fatalf("models json must preserve management fields: %q", out)
	}
	code, out, errOut = env.runCLI(t, "models", "disable", "codex", "gpt-5.2")
	if code != exitOK {
		t.Fatalf("disable code=%d stderr=%s", code, errOut)
	}
	list, _ := env.clientList(t)
	for _, pr := range list.Providers {
		if pr.ID == "codex" {
			if len(pr.DisabledModels) != 1 || pr.DisabledModels[0] != "gpt-5.2" {
				t.Fatalf("disabledModels: %+v", pr)
			}
			if len(pr.Models) != 2 {
				t.Fatalf("models erased by model toggle: %+v", pr)
			}
		}
	}
	code, _, errOut = env.runCLI(t, "models", "enable", "codex", "gpt-5.2")
	if code != exitOK {
		t.Fatalf("enable code=%d stderr=%s", code, errOut)
	}
	list, _ = env.clientList(t)
	for _, pr := range list.Providers {
		if pr.ID == "codex" && len(pr.DisabledModels) != 0 {
			t.Fatalf("disabledModels not cleared: %+v", pr)
		}
	}
	_ = out
}

func TestModelsToggleUnknownModelRefused(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, _, _ := env.runCLI(t, "models", "disable", "codex", "no-such-model")
	if code == exitOK {
		t.Fatal("expected failure for unknown model")
	}
}

// --- combos and routes ---

func TestCombosListSetRemove(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, _ := env.runCLI(t, "combos", "list")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "combo-1") {
		t.Fatalf("combos table: %q", out)
	}
	code, _, errOut := env.runCLI(t, "combos", "set", "combo-2", "--strategy", "round_robin",
		"--target", "codex/gpt-5.3:2", "--target", "codex/gpt-5.2", "--sticky-limit", "3")
	if code != exitOK {
		t.Fatalf("set code=%d stderr=%s", code, errOut)
	}
	doc := env.cfg.Get().Config
	c2 := doc.Combos["combo-2"]
	if c2.Strategy != config.ComboRoundRobin {
		t.Fatalf("combo strategy: %+v", c2)
	}
	if len(c2.Targets) != 2 || c2.Targets[0].Weight != 2 {
		t.Fatalf("combo targets: %+v", c2)
	}
	if c2.StickyLimit != 3 {
		t.Fatalf("stickyLimit: %+v", c2)
	}
	code, _, errOut = env.runCLI(t, "combos", "remove", "combo-2")
	if code != exitOK {
		t.Fatalf("remove code=%d stderr=%s", code, errOut)
	}
	doc = env.cfg.Get().Config
	if _, ok := doc.Combos["combo-2"]; ok {
		t.Fatal("combo-2 still present")
	}
}

func TestRoutesListSetRemove(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, _ := env.runCLI(t, "routes", "list")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "gpt-5.3") {
		t.Fatalf("routes table: %q", out)
	}
	code, _, errOut := env.runCLI(t, "routes", "set", "claude-opus", "codex/gpt-5.3")
	if code != exitOK {
		t.Fatalf("set code=%d stderr=%s", code, errOut)
	}
	doc := env.cfg.Get().Config
	if doc.Routes["claude-opus"] != "codex/gpt-5.3" {
		t.Fatalf("route not written: %+v", doc.Routes)
	}
	code, _, errOut = env.runCLI(t, "routes", "remove", "claude-opus")
	if code != exitOK {
		t.Fatalf("remove code=%d stderr=%s", code, errOut)
	}
	doc = env.cfg.Get().Config
	if _, ok := doc.Routes["claude-opus"]; ok {
		t.Fatal("route still present")
	}
}

// --- integrations ---

func TestIntegrationsStatusListAndSingle(t *testing.T) {
	env := newDaemonEnv(t, func(e *daemonEnv) {
		registerSandboxIntegrations(t, e)
	})
	code, out, errOut := env.runCLI(t, "integrations", "status")
	if code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "codex") {
		t.Fatalf("integrations table: %q", out)
	}
	code, out, _ = env.runCLI(t, "integrations", "status", "grok", "--json")
	if code != exitOK {
		t.Fatalf("json code=%d", code)
	}
	m := decodeJSON(t, out)
	if m["id"] != "grok" {
		t.Fatalf("single status json: %v", m)
	}
}

func agentsManagerForTest(t *testing.T) *agentinstall.Manager {
	t.Helper()
	dir := t.TempDir()
	// The sandbox mirrors the real layouts: tools in a neutral bin, agent
	// binaries under <home>/.local/bin so source detection sees a script
	// install like the real machine's.
	toolDir := filepath.Join(dir, "tools")
	localBin := filepath.Join(dir, ".local", "bin")
	for _, d := range []string{toolDir, localBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"npm", "bash", "sh"} {
		if err := os.WriteFile(filepath.Join(toolDir, tool), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, bin := range []string{"codex", "claude", "grok", "omp", "pi", "opencode", "opencode2", "hermes"} {
		if err := os.WriteFile(filepath.Join(localBin, bin), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := integrations.Environ([]string{
		"PATH=" + toolDir + string(os.PathListSeparator) + localBin,
	})
	runner := funcRunner(func(ctx context.Context, e integrations.Env, argv []string, dst io.Writer) error {
		fmt.Fprintf(dst, "ran %s\n", strings.Join(argv, " "))
		return nil
	})
	fetch := func(ctx context.Context, url string) (string, error) {
		return filepath.Join(dir, "fetched.sh"), nil
	}
	return agentinstall.NewManager(env, runner, os.Stat, time.Now, fetch)
}

type funcRunner func(ctx context.Context, env integrations.Env, argv []string, dst io.Writer) error

func (f funcRunner) Run(ctx context.Context, env integrations.Env, argv []string, dst io.Writer) error {
	return f(ctx, env, argv, dst)
}

func TestAgentsStatusListAndSingle(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, errOut := env.runCLI(t, "agents")
	if code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "codex") || !strings.Contains(out, "hermes") {
		t.Fatalf("agents table: %q", out)
	}
	code, out, _ = env.runCLI(t, "agents", "status", "grok", "--json")
	if code != exitOK {
		t.Fatalf("json code=%d", code)
	}
	m := decodeJSON(t, out)
	if m["id"] != "grok" {
		t.Fatalf("single status json: %v", m)
	}
	if m["source"] != "script" {
		t.Fatalf("grok should classify as a script install: %v", m)
	}
	if m["canUpdate"] != true {
		t.Fatalf("grok should be updatable: %v", m)
	}
}

func TestAgentsInstallJobLifecycleThroughCLI(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, _, errOut := env.runCLI(t, "agents", "install", "codex")
	if code != exitOK {
		t.Fatalf("install code=%d stderr=%s", code, errOut)
	}
	for {
		_, out, _ := env.runCLI(t, "agents", "job", "codex", "--json")
		m := decodeJSON(t, out)
		job := m["job"].(map[string]any)
		state := job["state"].(string)
		if state == "succeeded" {
			break
		}
		if state == "failed" || state == "unsupported" || state == "interrupted" {
			t.Fatalf("install ended in %s: %v", state, job)
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, out, _ := env.runCLI(t, "agents", "status", "codex", "--json")
	if code != exitOK {
		t.Fatalf("status code=%d", code)
	}
	m := decodeJSON(t, out)
	if m["installed"] != true {
		t.Fatalf("codex should be installed after job: %v", m)
	}
	if m["source"] != "script" {
		t.Fatalf("unexpected source: %v", m)
	}
}

func TestAgentsUpdateRunsSelfUpdate(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, _, errOut := env.runCLI(t, "agents", "update", "codex")
	if code != exitOK {
		t.Fatalf("update code=%d stderr=%s", code, errOut)
	}
	for {
		_, out, _ := env.runCLI(t, "agents", "job", "codex", "--json")
		m := decodeJSON(t, out)
		job := m["job"].(map[string]any)
		state := job["state"].(string)
		if state == "succeeded" {
			break
		}
		if state == "failed" || state == "unsupported" || state == "interrupted" {
			t.Fatalf("update ended in %s: %v", state, job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAgentsUpdateUnknownAgentUsage(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, _, errOut := env.runCLI(t, "agents", "update", "frobnicate")
	if code != exitUsage {
		t.Fatalf("unknown agent should be a usage error, got code=%d stderr=%s", code, errOut)
	}
	code, _, errOut = env.runCLI(t, "agents", "frobnicate")
	if code != exitUsage {
		t.Fatalf("unknown subcommand should be a usage error, got code=%d stderr=%s", code, errOut)
	}
}

func TestIntegrationRefusalSurfacesVerbatim(t *testing.T) {
	env := newDaemonEnv(t, func(e *daemonEnv) {
		registerSandboxIntegrations(t, e)
	})
	// rollback of an unmanaged file surfaces the honest refusal verbatim
	// with exit 4.
	code, _, errOut := env.runCLI(t, "integrations", "rollback", "codex")
	if code != exitRefused {
		t.Fatalf("code=%d want %d stderr=%s", code, exitRefused, errOut)
	}
	if !strings.Contains(errOut, "not managed") {
		t.Fatalf("refusal text: %q", errOut)
	}
}

func TestIntegrationApplyRollbackRoundTripThroughCLI(t *testing.T) {
	env := newDaemonEnv(t, func(e *daemonEnv) {
		registerSandboxIntegrations(t, e)
	})
	code, _, errOut := env.runCLI(t, "integrations", "apply", "codex")
	if code != exitOK {
		t.Fatalf("apply code=%d stderr=%s", code, errOut)
	}
	if code, _, errOut := env.runCLI(t, "integrations", "status", "codex"); code != exitOK {
		t.Fatalf("status code=%d stderr=%s", code, errOut)
	}
	// after apply, the file exists and is managed
	if !strings.Contains(env.stdout.String(), "yes") {
		t.Fatalf("status after apply: %q", env.stdout.String())
	}
	code, _, errOut = env.runCLI(t, "integrations", "rollback", "codex")
	if code != exitOK {
		t.Fatalf("rollback code=%d stderr=%s", code, errOut)
	}
}

func TestIntegrationApplyForceTakeoverThroughCLI(t *testing.T) {
	env := newDaemonEnv(t, func(e *daemonEnv) {
		registerSandboxIntegrations(t, e)
	})
	grokPath := filepath.Join(env.sandboxDir, "grok", "config.toml")
	userTable := "[model.prism-codex-main-gpt-5-2-codex]\nmodel = \"codex-main/gpt-5.2-codex\"\n"
	if err := os.MkdirAll(filepath.Dir(grokPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grokPath, []byte("# user configuration\ntop_setting = \"keep\"\n\n"+userTable), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := env.runCLI(t, "integrations", "apply", "grok")
	if code != exitRefused {
		t.Fatalf("plain apply code=%d want %d stderr=%s", code, exitRefused, errOut)
	}
	if !strings.Contains(errOut, "--force") {
		t.Fatalf("retryable refusal must name --force: %q", errOut)
	}
	code, _, errOut = env.runCLI(t, "integrations", "apply", "grok", "--force")
	if code != exitOK {
		t.Fatalf("forced apply code=%d stderr=%s", code, errOut)
	}
	code, _, errOut = env.runCLI(t, "integrations", "rollback", "grok")
	if code != exitOK {
		t.Fatalf("rollback code=%d stderr=%s", code, errOut)
	}
}

// registerSandboxIntegrations wires real integration modules with paths
// inside the test sandbox; the CLI process itself touches no files.
func registerSandboxIntegrations(t *testing.T, e *daemonEnv) {
	t.Helper()
	codexPath := filepath.Join(e.sandboxDir, "codex", "config.toml")
	grokPath := filepath.Join(e.sandboxDir, "grok", "config.toml")
	modelsPath := filepath.Join(e.sandboxDir, "omp", "models.yml")
	if err := e.registry.Register(integrations.NewCodex(integrations.CodexOptions{Port: 8787, ConfigPath: codexPath})); err != nil {
		t.Fatal(err)
	}
	if err := e.registry.Register(integrations.NewGrok(integrations.GrokOptions{Port: 8787, Models: integrations.DefaultPrismModels, ConfigPath: grokPath})); err != nil {
		t.Fatal(err)
	}
	if err := e.registry.Register(integrations.NewOmp(integrations.OmpOptions{Port: 8787, Models: integrations.DefaultPrismModels, ModelsPath: modelsPath})); err != nil {
		t.Fatal(err)
	}
}
func TestAuthLoginNoOpenFlow(t *testing.T) {
	env := newDaemonEnv(t, func(e *daemonEnv) {
		// The fake transitions the session to authorized on first Status
		// call so the poll loop terminates deterministically.
		e.auth.onStatus = func(sid string) auth.AuthStatus {
			return auth.AuthStatus{State: auth.StatusAuthorized}
		}
	})
	code, out, errOut := env.runCLI(t, "auth", "login", "codex", "--no-open")
	if code != exitOK {
		t.Fatalf("code=%d stderr=%s out=%s", code, errOut, out)
	}
	if !strings.Contains(out, "idp.example.com") {
		t.Fatalf("login URL not printed: %q", out)
	}
	if !strings.Contains(out, "authorized") {
		t.Fatalf("terminal state not printed: %q", out)
	}
}

func TestAuthLoginPollCancellable(t *testing.T) {
	env := newDaemonEnv(t, func(e *daemonEnv) {
		e.auth.onStatus = func(sid string) auth.AuthStatus {
			return auth.AuthStatus{State: auth.StatusPending}
		}
	})
	e := env
	e.stdout.Reset()
	e.stderr.Reset()
	cmd, err := parseCommand([]string{"auth", "login", "codex", "--no-open"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err = cmd.run(ctx, e.rt)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("cancelled poll must error")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("cancel took %v; polling ignored context", elapsed)
	}
}

func TestAuthStatusReport(t *testing.T) {
	env := newDaemonEnv(t, func(e *daemonEnv) {
		e.auth.status = map[string]auth.AuthStatus{"sess-9": {State: auth.StatusAuthorized}}
	})
	code, out, _ := env.runCLI(t, "auth", "status", "sess-9")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "authorized") {
		t.Fatalf("status output: %q", out)
	}
}

func TestAuthStatusNoSessionReportsProviders(t *testing.T) {
	env := newDaemonEnv(t, nil)
	code, out, _ := env.runCLI(t, "auth", "status")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "codex") || !strings.Contains(out, "antigravity") {
		t.Fatalf("status output: %q", out)
	}
}

func TestAuthSecretsNeverPrinted(t *testing.T) {
	env := newDaemonEnv(t, func(e *daemonEnv) {
		e.auth.status = map[string]auth.AuthStatus{"sess-1": {State: auth.StatusPending}}
	})
	// The fake auth URL carries a state nonce; the CLI must print the URL
	// (it is the authorization URL, safe) but no code/verifier exists in CLI
	// output because the CLI never receives one.
	code, out, _ := env.runCLI(t, "auth", "status", "sess-1")
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(out, "code=") && !strings.Contains(out, "state=") {
		t.Fatal("authorization code appeared in output")
	}
}

// --- secret boundary ---

func TestNoSecretInStdoutStderrHelpOrErrors(t *testing.T) {
	env := newDaemonEnv(t, nil)
	secret := "sk-boundary-secret-12345"
	credFile := filepath.Join(env.sandboxDir, "c.txt")
	if err := os.WriteFile(credFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env.runCLI(t, "providers", "add", "secprov", "--wire", "chat",
		"--endpoint", "http://127.0.0.1:9000/v1", "--credential-file", credFile)
	env.runCLI(t, "providers", "list")
	env.runCLI(t, "providers", "list", "--json")
	env.runCLI(t, "status")
	env.runCLI(t, "doctor")
	env.runCLI(t, "help")
	all := env.stdout.String() + env.stderr.String()
	if strings.Contains(all, secret) {
		t.Fatal("secret leaked into CLI output")
	}
}

// --- helpers ---

func mustParse(t *testing.T, args ...string) *command {
	t.Helper()
	cmd, err := parseCommand(args)
	if err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

func TestParseCommandShapes(t *testing.T) {
	cmd := mustParse(t, "auth", "login", "codex", "--no-open", "--json")
	if cmd.verb != "auth-login" || !cmd.authNoOpen || !cmd.json || cmd.provider != "codex" {
		t.Fatalf("auth login parse: %+v", cmd)
	}
	cmd = mustParse(t, "accounts", "auto-switch", "codex", "on", "--threshold", "0.5")
	if cmd.verb != "accounts-auto-switch" || !cmd.autoSwitch || !cmd.thresholdSet || cmd.threshold != 0.5 {
		t.Fatalf("auto-switch parse: %+v", cmd)
	}
	cmd = mustParse(t, "combos", "set", "c", "--strategy", "failover", "--target", "codex/m:2")
	if cmd.strategy != "failover" || len(cmd.targets) != 1 || cmd.targets[0].Weight != 2 {
		t.Fatalf("combos set parse: %+v", cmd)
	}
	cmd = mustParse(t, "providers", "add", "p", "--wire", "chat", "--endpoint", "u", "--model", "m1", "--model", "m2")
	if len(cmd.models) != 2 || cmd.baseURL != "u" {
		t.Fatalf("providers add parse: %+v", cmd)
	}
	cmd = mustParse(t, "integrations", "apply", "omp")
	if cmd.verb != "integrations-apply" || cmd.clientID != "omp" {
		t.Fatalf("integrations apply parse: %+v", cmd)
	}
}

func TestEscapePath(t *testing.T) {
	if got := escapePath("a/b"); got != "a%2Fb" {
		t.Fatalf("escapePath: %q", got)
	}
	if got := escapePath("plain-1.2"); got != "plain-1.2" {
		t.Fatalf("escapePath: %q", got)
	}
}

func TestTargetParsingVariants(t *testing.T) {
	tgt, err := parseTarget("codex/gpt-5.3:2")
	if err != nil || tgt.Provider != "codex" || tgt.Model != "gpt-5.3" || tgt.Weight != 2 {
		t.Fatalf("target: %+v err=%v", tgt, err)
	}
	tgt, err = parseTarget("codex/gpt-5.3")
	if err != nil || tgt.Weight != 0 {
		t.Fatalf("target: %+v err=%v", tgt, err)
	}
	if _, err = parseTarget("noprovider"); err == nil {
		t.Fatal("expected failure")
	}
	if _, err = parseTarget("a/b:-1"); err == nil {
		t.Fatal("expected failure for negative weight")
	}
}

func TestStatsParseRanges(t *testing.T) {
	cmd := mustParse(t, "stats")
	if cmd.verb != "stats" || cmd.statsRange != "24h" || cmd.json {
		t.Fatalf("stats default parse: %+v", cmd)
	}
	for _, rng := range []string{"1h", "24h", "7d", "30d", "all"} {
		cmd := mustParse(t, "stats", "--range", rng, "--json")
		if cmd.verb != "stats" || cmd.statsRange != rng || !cmd.json {
			t.Fatalf("stats --range %s parse: %+v", rng, cmd)
		}
	}
	if _, err := parseCommand([]string{"stats", "--range", "bogus"}); err == nil {
		t.Fatal("expected parse failure for invalid --range")
	} else if ue, ok := err.(*usageError); !ok || ue.help != helpStats {
		t.Fatalf("invalid --range error = %T %v, want usageError with stats help", err, err)
	}
	if _, err := parseCommand([]string{"stats", "extra"}); err == nil {
		t.Fatal("expected parse failure for unexpected argument")
	}
	if _, err := parseCommand([]string{"stats", "--bogus"}); err == nil {
		t.Fatal("expected parse failure for unknown flag")
	}
}
