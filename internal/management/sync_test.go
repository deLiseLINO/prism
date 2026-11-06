package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

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

func TestSyncConfigApplyKeepsMachineState(t *testing.T) {
	e := newEnv(t)
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
