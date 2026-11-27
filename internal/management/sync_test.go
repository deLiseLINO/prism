package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
)

func pushBundle(t *testing.T, e *testEnv, bundle hostsync.Bundle) hostsync.ApplyResponse {
	t.Helper()
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	rec := e.do(t, http.MethodPut, hostsync.ConfigPath, string(raw))
	if rec.Code != http.StatusOK {
		t.Fatalf("sync apply: status %d, body %s", rec.Code, rec.Body.String())
	}
	return decodeBody[hostsync.ApplyResponse](t, rec)
}

type fakeReplicas struct {
	owned    map[string][]string
	applied  [][]hostsync.Replica
	rejected []hostsync.RejectedToken
}

func (f *fakeReplicas) Owned(provider string) []string { return f.owned[provider] }

func (f *fakeReplicas) Apply(_ context.Context, _ config.Document, replicas []hostsync.Replica) error {
	f.applied = append(f.applied, replicas)
	return nil
}

func (f *fakeReplicas) Rejected() []hostsync.RejectedToken { return f.rejected }

func TestSyncConfigApplyKeepsMachineState(t *testing.T) {
	e := newEnv(t)
	replicas := &fakeReplicas{owned: map[string][]string{"codex": {"acct-here"}}}
	e.srv.SetAccountReplicas(replicas)
	e.creds.store["stale"] = []byte("old-key")
	local := config.Document{
		Version: config.SchemaVersion,
		Daemon:  config.Daemon{Listen: "127.0.0.1:4100"},
		Hosts:   map[string]config.Host{"box": {Address: "box.lan", DaemonPort: 4100}},
		Providers: map[string]config.Provider{
			"codex":     {Wire: config.WireCodex, Models: []string{"gpt-5.3"}, Pool: &config.PoolSettings{PinnedAccount: "acct-here"}},
			"codex-own": {Wire: config.WireCodex, Models: []string{"gpt-5.3"}},
			"stale":     {Wire: config.WireOpenAIChat, BaseURL: "http://stale.example/v1", APIKeyRef: "stale:default", Models: []string{"m"}},
		},
	}
	if _, err := e.cfg.Update(local, e.cfg.Get().Generation); err != nil {
		t.Fatal(err)
	}
	incoming := config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"codex": {Wire: config.WireCodex, Models: []string{"gpt-5.3", "gpt-5.4"}, Pool: &config.PoolSettings{PinnedAccount: "acct-main"}},
			"edge":  {Wire: config.WireOpenAIChat, BaseURL: "http://edge.example/v1", Models: []string{"m"}},
		},
		Routes:       map[string]string{"default": "edge/m"},
		Integrations: map[string]config.IntegrationSettings{"omp": {Enabled: true}},
	}
	bundle := hostsync.Bundle{Document: incoming, Secrets: map[string]string{"edge": "sk-edge"}}

	first := pushBundle(t, e, bundle)
	if !first.Changed {
		t.Fatal("first push reported no change")
	}
	got := e.cfg.Get().Config
	if got.Daemon.Listen != "127.0.0.1:4100" || got.Hosts["box"].Address != "box.lan" {
		t.Fatalf("machine sections were replaced: daemon %+v hosts %+v", got.Daemon, got.Hosts)
	}
	codex := got.Providers["codex"]
	if len(codex.Models) != 2 || codex.Pool == nil || codex.Pool.PinnedAccount != "acct-here" {
		t.Fatalf("login provider must take main settings but keep its own account: %+v", codex)
	}
	if _, kept := got.Providers["codex-own"]; !kept {
		t.Fatal("a login provider signed in only here was dropped")
	}
	if _, kept := got.Providers["stale"]; kept {
		t.Fatal("a custom provider the main machine does not have survived")
	}
	if _, kept := e.creds.store["stale"]; kept {
		t.Fatal("the removed provider's key was left behind")
	}
	if ref := got.Providers["edge"].APIKeyRef; ref != "edge:default" || string(e.creds.store["edge"]) != "sk-edge" {
		t.Fatalf("pushed key not stored and published: ref %q store %q", ref, e.creds.store["edge"])
	}
	if got.Routes["default"] != "edge/m" || !got.Integrations["omp"].Enabled {
		t.Fatalf("portable sections not applied: routes %+v integrations %+v", got.Routes, got.Integrations)
	}

	again := pushBundle(t, e, bundle)
	if again.Changed || again.Generation != first.Generation {
		t.Fatalf("an identical push must write nothing: %+v after %+v", again, first)
	}
	if len(replicas.applied) != 2 {
		t.Fatalf("replicas applied %d times, want on every push", len(replicas.applied))
	}
}

func TestSyncConfigApplyAppliesPushedReplicas(t *testing.T) {
	e := newEnv(t)
	replicas := &fakeReplicas{}
	e.srv.SetAccountReplicas(replicas)
	pushed := []hostsync.Replica{{Provider: "codex", ID: "codex:acct", State: hostsync.ReplicaActive, Credential: []byte(`{"version":1,"access":"at"}`)}}
	pushBundle(t, e, hostsync.Bundle{
		Document: config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{"codex": {Wire: config.WireCodex, Models: []string{"gpt-5.3"}}}},
		Accounts: pushed,
	})
	if len(replicas.applied) != 1 || len(replicas.applied[0]) != 1 || replicas.applied[0][0].ID != "codex:acct" {
		t.Fatalf("pushed replicas not applied: %+v", replicas.applied)
	}
}

func TestSyncEditsReportRejectedReplicaTokens(t *testing.T) {
	e := newEnv(t)
	rejected := []hostsync.RejectedToken{{Provider: "codex", Account: "codex:acct", Hash: hostsync.TokenHash("at")}}
	e.srv.SetAccountReplicas(&fakeReplicas{rejected: rejected})
	rec := e.do(t, http.MethodGet, hostsync.EditsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("edits: status %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"at"`) {
		t.Fatalf("the edits answer carries the token itself: %s", rec.Body.String())
	}
	got := decodeBody[hostsync.Edits](t, rec)
	if len(got.RejectedTokens) != 1 || got.RejectedTokens[0] != rejected[0] {
		t.Fatalf("rejected tokens = %+v, want %+v", got.RejectedTokens, rejected)
	}
}

func TestReplicaAccountsAreChangedOnTheMainMachineOnly(t *testing.T) {
	e := newEnv(t)
	e.pool.accounts = append(e.pool.accounts, account.Account{ID: "codex:main", Provider: "codex", State: account.Active, Origin: account.OriginMain, CredGen: 1, Version: 1})
	for _, req := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/accounts/codex:main/pause", `{"version":1}`},
		{http.MethodPost, "/api/v1/accounts/codex:main/resume", `{"version":1}`},
		{http.MethodPost, "/api/v1/accounts/codex:main/priority", `{"version":1,"priority":3}`},
		{http.MethodDelete, "/api/v1/accounts/codex:main", ""},
	} {
		rec := e.do(t, req.method, req.path, req.body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "main machine") {
			t.Fatalf("%s %s: status %d, body %s", req.method, req.path, rec.Code, rec.Body.String())
		}
	}
	if a := e.pool.accounts[len(e.pool.accounts)-1]; a.State != account.Active || a.Version != 1 {
		t.Fatalf("a refused change reached the replica: %+v", a)
	}
}

func TestSyncConfigApplyRefusesInvalidDocument(t *testing.T) {
	e := newEnv(t)
	before := e.cfg.Get()
	raw, err := json.Marshal(hostsync.Bundle{Document: config.Document{
		Version: config.SchemaVersion,
		Routes:  map[string]string{"default": "ghost/m"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rec := e.do(t, http.MethodPut, hostsync.ConfigPath, string(raw))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid document: status %d, body %s", rec.Code, rec.Body.String())
	}
	after := e.cfg.Get()
	if after.Generation != before.Generation || !bytes.Equal(mustJSON(t, after.Config), mustJSON(t, before.Config)) {
		t.Fatal("a refused push changed the config")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
