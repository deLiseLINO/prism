package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
	"github.com/deLiseLINO/prism/internal/integrations"
	"github.com/deLiseLINO/prism/internal/management"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/rename"
	"github.com/deLiseLINO/prism/internal/server"
	"github.com/deLiseLINO/prism/internal/store"
	"github.com/deLiseLINO/prism/internal/usage"
)

// renameMachine is one daemon's runtime over a state directory; building a
// second one over the same directory stands in for a restart.
type renameMachine struct {
	env   *daemonEnv
	cfg   *config.Manager
	usage usage.Store
	infer http.Handler
	mgmt  http.Handler
}

func openRenameMachine(t *testing.T, dir string) *renameMachine {
	t.Helper()
	mgr, err := config.Open(filepath.Join(dir, "prism.json"))
	if err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(dir, "credentials")
	u, err := usage.Open(filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Close() })
	pool := account.New()
	file := store.NewFileCredentialStore(creds)
	env := newDaemonEnv(creds, mgr, pool, newQuotaTable(pool, nil, nil), provider.NewRegistry(),
		credentialStore{file: file, pool: pool, publication: &credentialPublication{cfg: mgr, refs: map[string]string{}}},
		http.DefaultClient, http.DefaultClient)
	env.renames = rename.Stores{Creds: file, AccountsPath: env.accountsPath, Usage: u}
	env.reconcileOnce(context.Background())
	infer := server.New(server.Options{Planner: server.NewConfigPlanner(mgr), Registry: env.registry, Pool: env.pool, Config: mgr, Usage: u}).Handler()
	srv := management.New(env.pool, mgr, catalog{mgr}, nil, env.creds, nil, integrations.NewRegistry(), nil, nil)
	srv.SetUsageStore(u)
	srv.SetRenamer(newProviderRenamer(env, nil))
	return &renameMachine{env: env, cfg: mgr, usage: u, infer: infer, mgmt: srv.Handler()}
}

func (m *renameMachine) api(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	m.mgmt.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func decodeAs[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return v
}

// keyedUpstream answers chat completions for one API key and counts them.
func keyedUpstream(t *testing.T, key string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	t.Cleanup(up.Close)
	return up, &hits
}

func seedCustomProvider(t *testing.T, dir, upstream, key string) {
	t.Helper()
	mgr, err := config.Open(filepath.Join(dir, "prism.json"))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.NewFileCredentialStore(filepath.Join(dir, "credentials")).StageCustomDefaultSecret(context.Background(), "gw", "", []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	doc := config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"gw": {Wire: config.WireOpenAIChat, BaseURL: upstream + "/v1", Models: []string{"m1"}, APIKeyRef: ref},
	}}
	if _, err := mgr.Update(doc, 0); err != nil {
		t.Fatal(err)
	}
}

func (m *renameMachine) chat(t *testing.T, model string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	m.infer.ServeHTTP(rec, req)
	return rec
}

// providerRequests waits until usage holds want requests, then reports
// them by provider; usage rows land after the response is written.
func (m *renameMachine) providerRequests(t *testing.T, want int64) map[string]int64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, err := m.usage.ByProvider(context.Background(), time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		var total int64
		for _, r := range rows {
			out[r.Provider] = r.Requests
			total += r.Requests
		}
		if total >= want || time.Now().After(deadline) {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRenameMovesTheRunningProviderToTheNewID(t *testing.T) {
	dir := t.TempDir()
	up, hits := keyedUpstream(t, "sk-gw")
	seedCustomProvider(t, dir, up.URL, "sk-gw")
	m := openRenameMachine(t, dir)
	if rec := m.chat(t, "gw/m1"); rec.Code != http.StatusOK {
		t.Fatalf("before rename: %d %s", rec.Code, rec.Body.String())
	}
	m.providerRequests(t, 1)
	r := newProviderRenamer(m.env, nil)
	res, err := r.Rename(context.Background(), "gw", "edge", m.cfg.Get().Generation, false)
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if res.AlreadyApplied || res.Generation != m.cfg.Get().Generation {
		t.Fatalf("result = %+v", res)
	}
	if rec := m.chat(t, "edge/m1"); rec.Code != http.StatusOK {
		t.Fatalf("edge/m1 after rename: %d %s", rec.Code, rec.Body.String())
	}
	if rec := m.chat(t, "gw/m1"); rec.Code == http.StatusOK {
		t.Fatal("old model id still resolves")
	}
	if hits.Load() != 2 {
		t.Fatalf("upstream hits = %d, want 2", hits.Load())
	}
	if _, ok := m.env.registry.Lookup("gw"); ok {
		t.Fatal("gw runner kept")
	}
	for _, a := range m.env.pool.Snapshot().Accounts {
		if a.Provider == "gw" {
			t.Fatalf("gw account kept in the pool: %+v", a)
		}
	}
	if got := m.providerRequests(t, 2); got["edge"] != 2 || got["gw"] != 0 {
		t.Fatalf("usage by provider = %v", got)
	}
	m.env.reconcileOnce(context.Background())
	if _, ok := m.env.registry.Lookup("gw"); ok {
		t.Fatal("reconcile brought gw back")
	}

	again, err := r.Rename(context.Background(), "gw", "edge", res.Generation-1, false)
	if err != nil || !again.AlreadyApplied || again.Generation != res.Generation {
		t.Fatalf("repeated rename = %+v, %v", again, err)
	}
}

func TestRenameCommittedBeforeACrashFinishesOnRestart(t *testing.T) {
	dir := t.TempDir()
	up, hits := keyedUpstream(t, "sk-gw")
	seedCustomProvider(t, dir, up.URL, "sk-gw")
	m := openRenameMachine(t, dir)
	// Commit the config only: the process dies before moving any data.
	snap := m.cfg.Get()
	next, _, err := config.RenameProvider(snap.Config, "gw", "edge", "r1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.cfg.Update(next, snap.Generation); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.env.renames.Creds.AccountPresent("edge", "edge:default"); ok {
		t.Fatal("fixture moved data before the crash")
	}

	restarted := openRenameMachine(t, dir)
	if rec := restarted.chat(t, "edge/m1"); rec.Code != http.StatusOK {
		t.Fatalf("edge/m1 after restart: %d %s", rec.Code, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hits = %d", hits.Load())
	}
	if ok, _ := restarted.env.renames.Creds.AccountPresent("gw", "gw:default"); ok {
		t.Fatal("gw credentials kept after convergence")
	}
}

func TestRenameConflictChangesNothing(t *testing.T) {
	dir := t.TempDir()
	up, _ := keyedUpstream(t, "sk-gw")
	seedCustomProvider(t, dir, up.URL, "sk-gw")
	m := openRenameMachine(t, dir)
	snap := m.cfg.Get()
	doc := snap.Config
	doc.Providers["router"] = config.Provider{Wire: config.WireOpenAIChat, BaseURL: up.URL + "/v1", Models: []string{"m1"}}
	updated, err := m.cfg.Update(doc, snap.Generation)
	if err != nil {
		t.Fatal(err)
	}
	r := newProviderRenamer(m.env, func(account.ProviderID) bool { return false })
	_, err = r.Rename(context.Background(), "gw", "router", updated.Generation, false)
	var conflicts config.RenameConflicts
	if !errors.As(err, &conflicts) || conflicts[0].Kind != config.ConflictProviderExists {
		t.Fatalf("err = %v", err)
	}
	if m.cfg.Get().Generation != updated.Generation {
		t.Fatal("a refused rename wrote the config")
	}
	if rec := m.chat(t, "gw/m1"); rec.Code != http.StatusOK {
		t.Fatalf("gw stopped working after a refused rename: %d", rec.Code)
	}

	signingIn := newProviderRenamer(m.env, func(p account.ProviderID) bool { return p == "gw" })
	_, err = signingIn.Rename(context.Background(), "gw", "edge", updated.Generation, false)
	if !errors.As(err, &conflicts) || conflicts[0].Kind != config.ConflictAuthPending {
		t.Fatalf("rename during sign-in: %v", err)
	}
}

func TestRenameThroughTheManagementAPI(t *testing.T) {
	dir := t.TempDir()
	up, hits := keyedUpstream(t, "sk-gw")
	seedCustomProvider(t, dir, up.URL, "sk-gw")
	m := openRenameMachine(t, dir)
	if rec := m.chat(t, "gw/m1"); rec.Code != http.StatusOK {
		t.Fatalf("before rename: %d %s", rec.Code, rec.Body.String())
	}
	m.providerRequests(t, 1)
	gen := m.cfg.Get().Generation

	dry := m.api(t, http.MethodPost, "/api/v1/providers/gw/rename", fmt.Sprintf(`{"to":"edge","expectedGeneration":%d,"dryRun":true}`, gen))
	preview := decodeAs[management.ProviderRenameResponse](t, dry)
	if dry.Code != http.StatusOK || !preview.DryRun || preview.Plan.UsageAttempts != 1 || len(preview.Plan.Accounts) != 1 || preview.Provider != nil {
		t.Fatalf("dry run = %d %s", dry.Code, dry.Body.String())
	}
	if m.cfg.Get().Generation != gen {
		t.Fatal("dry run wrote the config")
	}

	clash := m.api(t, http.MethodPost, "/api/v1/providers/gw/rename", fmt.Sprintf(`{"to":"codex","expectedGeneration":%d}`, gen))
	if body := decodeAs[management.RenameConflictBody](t, clash); clash.Code != http.StatusConflict || body.Conflicts[0].Kind != "reserved_login_id" {
		t.Fatalf("conflict = %d %s", clash.Code, clash.Body.String())
	}

	done := m.api(t, http.MethodPost, "/api/v1/providers/gw/rename", fmt.Sprintf(`{"to":"edge","expectedGeneration":%d}`, gen))
	result := decodeAs[management.ProviderRenameResponse](t, done)
	if done.Code != http.StatusOK || result.Provider == nil || result.Provider.ID != "edge" || result.Generation != gen+1 {
		t.Fatalf("rename = %d %s", done.Code, done.Body.String())
	}

	models := decodeAs[management.ModelsResponse](t, m.api(t, http.MethodGet, "/api/v1/models", ""))
	var ids []string
	for _, model := range models.Models {
		ids = append(ids, model.ID)
	}
	if strings.Join(ids, ",") != "edge/m1" {
		t.Fatalf("models after rename = %v", ids)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	m.infer.ServeHTTP(rec, req)
	if body := rec.Body.String(); !strings.Contains(body, `edge--m1"`) || strings.Contains(body, `gw`) {
		t.Fatalf("/v1/models after rename = %s", body)
	}

	if rec := m.chat(t, "edge/m1"); rec.Code != http.StatusOK {
		t.Fatalf("edge/m1: %d %s", rec.Code, rec.Body.String())
	}
	if hits.Load() != 2 {
		t.Fatalf("upstream hits = %d: edge must reach the same upstream with the same key", hits.Load())
	}
	m.providerRequests(t, 2)
	stats := decodeAs[management.StatsResponse](t, m.api(t, http.MethodGet, "/api/v1/stats?range=all", ""))
	if len(stats.Providers) != 1 || stats.Providers[0].Provider != "edge" || stats.Providers[0].Requests != 2 {
		t.Fatalf("stats providers = %+v", stats.Providers)
	}

	retry := m.api(t, http.MethodPost, "/api/v1/providers/gw/rename", fmt.Sprintf(`{"to":"edge","expectedGeneration":%d}`, gen))
	if body := decodeAs[management.ProviderRenameResponse](t, retry); retry.Code != http.StatusOK || !body.AlreadyApplied || m.cfg.Get().Generation != gen+1 {
		t.Fatalf("retried rename = %d %s", retry.Code, retry.Body.String())
	}
}

// seedSyncedMachine writes the config both machines share before the
// rename: a sign-in provider gw and a custom provider router with its key.
func seedSyncedMachine(t *testing.T, dir, upstream, key string) {
	t.Helper()
	mgr, err := config.Open(filepath.Join(dir, "prism.json"))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.NewFileCredentialStore(filepath.Join(dir, "credentials")).StageCustomDefaultSecret(context.Background(), "router", "", []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	doc := config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"gw":     {Wire: config.WireCodex, Models: []string{"m1"}},
		"router": {Wire: config.WireOpenAIChat, BaseURL: upstream + "/v1", Models: []string{"m1"}, APIKeyRef: ref},
	}}
	if _, err := mgr.Update(doc, 0); err != nil {
		t.Fatal(err)
	}
}

func (m *renameMachine) bundle(t *testing.T) hostsync.Bundle {
	t.Helper()
	b, err := hostsync.Portable(context.Background(), m.cfg.Get().Config, func(ctx context.Context, id, ref string) ([]byte, bool, error) {
		return m.env.creds.file.GetCustomDefaultSecret(ctx, account.ProviderID(id), ref)
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (m *renameMachine) sync(t *testing.T, body []byte) hostsync.ApplyResponse {
	t.Helper()
	rec := m.api(t, http.MethodPut, hostsync.ConfigPath, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("sync = %d %s", rec.Code, rec.Body.String())
	}
	return decodeAs[hostsync.ApplyResponse](t, rec)
}

func TestSyncedHostReplaysRenamesBeforeMerging(t *testing.T) {
	ctx := context.Background()
	up, hits := keyedUpstream(t, "sk-router")
	mainDir, hostDir := t.TempDir(), t.TempDir()
	seedSyncedMachine(t, mainDir, up.URL, "sk-router")
	seedSyncedMachine(t, hostDir, up.URL, "sk-router")
	// Only the host has an account signed in under gw, and usage for it.
	hostCreds := store.NewFileCredentialStore(filepath.Join(hostDir, "credentials"))
	if err := hostCreds.Put(ctx, "gw", "gw:alice", 1, []byte("token-alice")); err != nil {
		t.Fatal(err)
	}
	if err := account.OpenMeta(filepath.Join(hostDir, "credentials", "accounts", "gw.json")).CASLoginGeneration("gw", "gw:alice", 0, 1, "alice@example.test"); err != nil {
		t.Fatal(err)
	}
	main, host := openRenameMachine(t, mainDir), openRenameMachine(t, hostDir)
	if err := host.usage.Insert(ctx, usage.Record{RequestID: "host-1", Timestamp: time.Now().Add(-time.Minute), Model: "gw/m1", Protocol: "chat", Status: "completed", Attempts: []usage.Attempt{{Provider: "gw", Model: "m1", Outcome: "completed"}}}); err != nil {
		t.Fatal(err)
	}

	r := newProviderRenamer(main.env, nil)
	for _, step := range [][2]string{{"gw", "edge"}, {"router", "relay"}} {
		if _, err := r.Rename(ctx, step[0], step[1], main.cfg.Get().Generation, false); err != nil {
			t.Fatalf("rename %s: %v", step[0], err)
		}
	}
	raw, err := json.Marshal(main.bundle(t))
	if err != nil {
		t.Fatal(err)
	}
	if res := host.sync(t, raw); !res.Changed {
		t.Fatal("first sync changed nothing")
	}

	doc := host.cfg.Get().Config
	for _, old := range []string{"gw", "router"} {
		if _, ok := doc.Providers[old]; ok {
			t.Fatalf("host kept %s next to its new id: %v", old, doc.Providers)
		}
		if _, ok := host.env.registry.Lookup(account.ProviderID(old)); ok {
			t.Fatalf("host still runs %s", old)
		}
	}
	if len(doc.ProviderRenames) != 2 {
		t.Fatalf("host ledger = %+v", doc.ProviderRenames)
	}
	rows, err := account.OpenMeta(host.env.accountsPath("edge", nil)).Load("edge")
	if err != nil || len(rows) != 1 || rows[0].ID != "edge:alice" {
		t.Fatalf("host edge accounts = %+v, %v", rows, err)
	}
	if blob, ok, err := hostCreds.Get(ctx, "edge", "edge:alice", 1); err != nil || !ok || string(blob) != "token-alice" {
		t.Fatalf("edge:alice credential = %q %v %v", blob, ok, err)
	}
	if ok, _ := hostCreds.AccountPresent("gw", "gw:alice"); ok {
		t.Fatal("gw:alice credentials left behind")
	}
	inPool := false
	for _, a := range host.env.pool.Snapshot().Accounts {
		if a.Provider == "gw" || a.Provider == "router" {
			t.Fatalf("host pool kept %+v", a)
		}
		inPool = inPool || a.ID == "edge:alice"
	}
	if !inPool {
		t.Fatal("edge:alice is not in the host pool")
	}
	if got := host.providerRequests(t, 1); got["edge"] != 1 || got["gw"] != 0 {
		t.Fatalf("host usage by provider = %v", got)
	}
	if rec := host.chat(t, "relay/m1"); rec.Code != http.StatusOK {
		t.Fatalf("relay/m1 on the host: %d %s", rec.Code, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hits = %d", hits.Load())
	}

	gen := host.cfg.Get().Generation
	if res := host.sync(t, raw); res.Changed || res.Generation != gen {
		t.Fatalf("second sync = %+v, want a no-op at generation %d", res, gen)
	}

	// A main machine on an older build sends no ledger.
	var old map[string]any
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	delete(old["document"].(map[string]any), "providerRenames")
	legacy, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if res := host.sync(t, legacy); res.Changed || res.Generation != gen {
		t.Fatalf("sync without a ledger = %+v", res)
	}
	if got := host.cfg.Get().Config.ProviderRenames; len(got) != 2 {
		t.Fatalf("sync without a ledger dropped the host ledger: %+v", got)
	}
}
