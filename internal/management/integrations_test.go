package management

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
	"github.com/deLiseLINO/prism/internal/integrations"
)

func integrationEnv(t *testing.T) (*httptest.Server, *integrations.Registry) {
	t.Helper()
	m, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := integrations.NewRegistry()
	srv := New(&fakePool{}, m, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}}, &fakeAuth{}, registry, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, registry
}

func toggleEnv(t *testing.T) (*httptest.Server, *config.Manager, *integrations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	registry := integrations.NewRegistry()
	srv := New(&fakePool{}, m, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}}, &fakeAuth{}, registry, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, m, registry, path
}

func registerSandbox(t *testing.T, registry *integrations.Registry, dir string) {
	t.Helper()
	codexPath := filepath.Join(dir, "codex", "config.toml")
	grokPath := filepath.Join(dir, "grok", "config.toml")
	modelsPath := filepath.Join(dir, "omp", "models.yml")
	claudePath := filepath.Join(dir, "claude", "settings.json")
	piPath := filepath.Join(dir, "pi", "models.json")
	opencodePath := filepath.Join(dir, "opencode", "opencode.json")
	hermesPath := filepath.Join(dir, "hermes", "config.yaml")
	registrations := []struct {
		module integrations.Module
	}{
		{integrations.NewCodex(integrations.CodexOptions{Port: 8787, ConfigPath: codexPath})},
		{integrations.NewGrok(integrations.GrokOptions{Port: 8787, Models: integrations.DefaultPrismModels, ConfigPath: grokPath})},
		{integrations.NewOmp(integrations.OmpOptions{Port: 8787, Models: integrations.DefaultPrismModels, ModelsPath: modelsPath})},
		{integrations.NewClaude(integrations.ClaudeOptions{Port: 8787, Models: integrations.DefaultPrismModels, ConfigPath: claudePath})},
		{integrations.NewPi(integrations.PiOptions{Port: 8787, Models: integrations.DefaultPrismModels, ConfigPath: piPath})},
		{integrations.NewOpencode(integrations.OpencodeOptions{Port: 8787, Models: integrations.DefaultPrismModels, ConfigPath: opencodePath})},
		{integrations.NewHermes(integrations.HermesOptions{Port: 8787, Models: integrations.DefaultPrismModels, ConfigPath: hermesPath})},
	}
	for _, registration := range registrations {
		if err := registry.Register(registration.module); err != nil {
			t.Fatal(err)
		}
	}
}
func TestIntegrationsListStatus(t *testing.T) {
	dir := t.TempDir()
	ts, registry := integrationEnv(t)
	registerSandbox(t, registry, dir)

	res, err := http.Get(ts.URL + "/api/v1/integrations")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var body struct {
		Integrations []integrations.Status `json:"integrations"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Integrations) != 7 {
		t.Fatalf("expected 7 statuses, got %d", len(body.Integrations))
	}
	for i, want := range []integrations.ID{integrations.Codex, integrations.Grok, integrations.Omp, integrations.Claude, integrations.Pi, integrations.Opencode, integrations.Hermes} {
		if body.Integrations[i].ID != want {
			t.Errorf("status %d id %s, want %s", i, body.Integrations[i].ID, want)
		}
		if body.Integrations[i].Installed || body.Integrations[i].Managed {
			t.Errorf("fresh sandbox reported facts: %+v", body.Integrations[i])
		}
		if body.Integrations[i].TargetPath == nil {
			t.Errorf("status %s has null targetPath", want)
		}
	}
}

func TestIntegrationUnknownClient404(t *testing.T) {
	ts, _ := integrationEnv(t)
	for _, path := range []string{
		"/api/v1/integrations/disable",
	} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, res.StatusCode)
		}
	}
	for _, path := range []string{"/api/v1/integrations/codexx/apply", "/api/v1/integrations/disable/rollback"} {
		res, err := http.Post(ts.URL+path, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s: status %d, want 404", path, res.StatusCode)
		}
	}
}

func TestIntegrationToggleOnPersistsKeyAndAdvancesGeneration(t *testing.T) {
	ts, m, registry, path := toggleEnv(t)
	registerSandbox(t, registry, t.TempDir())
	registry.SetEnabledSource(func() map[integrations.ID]bool {
		out := make(map[integrations.ID]bool)
		for id, settings := range m.Get().Config.Integrations {
			if settings.Enabled {
				out[integrations.ID(id)] = true
			}
		}
		return out
	})
	gen := m.Get().Generation

	res := putJSON(t, ts.URL+"/api/v1/integrations/codex/enabled",
		fmt.Sprintf(`{"enabled":true,"expectedGeneration":%d}`, gen))
	defer res.Body.Close()
	var toggled IntegrationToggleResponse
	if err := json.NewDecoder(res.Body).Decode(&toggled); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !toggled.Enabled {
		t.Fatalf("toggle on: status %d, body %+v", res.StatusCode, toggled)
	}
	if toggled.Generation != gen+1 {
		t.Fatalf("generation did not advance: %d", toggled.Generation)
	}
	if !m.Get().Config.Integrations["codex"].Enabled {
		t.Fatal("toggle not persisted to the live manager")
	}
	reopened, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Get().Config.Integrations["codex"].Enabled {
		t.Fatal("toggle not persisted to disk")
	}

	list, err := http.Get(ts.URL + "/api/v1/integrations")
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	var body IntegrationsResponse
	if err := json.NewDecoder(list.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Generation != toggled.Generation {
		t.Fatalf("list generation %d, want %d", body.Generation, toggled.Generation)
	}
	var codexStatus *integrations.Status
	for i := range body.Integrations {
		if body.Integrations[i].ID == integrations.Codex {
			codexStatus = &body.Integrations[i]
		}
	}
	if codexStatus == nil || !codexStatus.Enabled {
		t.Fatalf("codex status not overlaid as enabled: %+v", codexStatus)
	}
}

func TestIntegrationToggleOffKeepsKey(t *testing.T) {
	ts, m, _, path := toggleEnv(t)
	gen := m.Get().Generation

	res := putJSON(t, ts.URL+"/api/v1/integrations/grok/enabled",
		fmt.Sprintf(`{"enabled":true,"expectedGeneration":%d}`, gen))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("toggle on: status %d", res.StatusCode)
	}
	gen = m.Get().Generation

	res = putJSON(t, ts.URL+"/api/v1/integrations/grok/enabled",
		fmt.Sprintf(`{"enabled":false,"expectedGeneration":%d}`, gen))
	defer res.Body.Close()
	var toggled IntegrationToggleResponse
	if err := json.NewDecoder(res.Body).Decode(&toggled); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || toggled.Enabled {
		t.Fatalf("toggle off: status %d, body %+v", res.StatusCode, toggled)
	}
	settings, exists := m.Get().Config.Integrations["grok"]
	if !exists || settings.Enabled {
		t.Fatalf("toggle off dropped the key: %+v", m.Get().Config.Integrations)
	}
	reopened, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, exists = reopened.Get().Config.Integrations["grok"]
	if !exists || settings.Enabled {
		t.Fatalf("toggle off did not persist the kept key: %+v", reopened.Get().Config.Integrations)
	}
}

func TestIntegrationToggleStaleGenerationConflicts(t *testing.T) {
	ts, m, _, _ := toggleEnv(t)
	if _, err := m.Update(config.Document{Version: config.SchemaVersion}, 0); err != nil {
		t.Fatal(err)
	}

	res := putJSON(t, ts.URL+"/api/v1/integrations/codex/enabled", `{"enabled":true,"expectedGeneration":99}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("stale generation must conflict, got %d", res.StatusCode)
	}
	var body ErrorBody
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "stale_generation" {
		t.Fatalf("error code %q, want stale_generation", body.Error.Code)
	}
	if _, exists := m.Get().Config.Integrations["codex"]; exists {
		t.Fatal("refused toggle must not write config")
	}
}

func TestIntegrationToggleUnknownClient404(t *testing.T) {
	ts, m, _, _ := toggleEnv(t)
	gen := m.Get().Generation

	res := putJSON(t, ts.URL+"/api/v1/integrations/codexx/enabled",
		fmt.Sprintf(`{"enabled":true,"expectedGeneration":%d}`, gen))
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown client: status %d, want 404", res.StatusCode)
	}
	var body ErrorBody
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "not_found" {
		t.Fatalf("error code %q, want not_found", body.Error.Code)
	}
}

func TestIntegrationsListUnregisteredNeverOverlayEnabled(t *testing.T) {
	ts, m, registry, _ := toggleEnv(t)
	registry.SetEnabledSource(func() map[integrations.ID]bool {
		out := make(map[integrations.ID]bool)
		for id, settings := range m.Get().Config.Integrations {
			if settings.Enabled {
				out[integrations.ID(id)] = true
			}
		}
		return out
	})
	gen := m.Get().Generation

	res := putJSON(t, ts.URL+"/api/v1/integrations/grok/enabled",
		fmt.Sprintf(`{"enabled":true,"expectedGeneration":%d}`, gen))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("toggle on: status %d", res.StatusCode)
	}

	list, err := http.Get(ts.URL + "/api/v1/integrations")
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	var body IntegrationsResponse
	if err := json.NewDecoder(list.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, status := range body.Integrations {
		if status.Enabled {
			t.Fatalf("unregistered %s overlaid as enabled: %+v", status.ID, status)
		}
	}
}

func TestHostsListIncludesLocalAlways(t *testing.T) {
	ts, _ := integrationEnv(t)
	res, err := http.Get(ts.URL + "/api/v1/hosts")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body HostsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Hosts) != 1 || body.Hosts[0].ID != HostIDLocal || !body.Hosts[0].Local || body.Hosts[0].Status != "ok" {
		t.Fatalf("expected only the local host, got %+v", body.Hosts)
	}
}

func hostsCrudEnv(t *testing.T) (*httptest.Server, *config.Manager) {
	t.Helper()
	m, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&fakePool{}, m, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}}, &fakeAuth{}, integrations.NewRegistry(), nil, nil)
	srv.SetHostSync(hostsync.NewPusher(hostsync.Options{}))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, m
}

func listHosts(t *testing.T, ts *httptest.Server) map[string]HostView {
	t.Helper()
	res, err := http.Get(ts.URL + "/api/v1/hosts")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body HostsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	out := map[string]HostView{}
	for _, h := range body.Hosts {
		out[h.ID] = h
	}
	return out
}

func putHost(t *testing.T, ts *httptest.Server, path, body string) (*http.Response, HostMutationResponse) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out HostMutationResponse
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

func TestHostsCreatePersistsAsNotInstalled(t *testing.T) {
	ts, m := hostsCrudEnv(t)
	gen := m.Get().Generation

	res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(fmt.Sprintf(`{"id":"workmac","address":"user@10.0.0.4","expectedGeneration":%d}`, gen)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created HostMutationResponse
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || created.Host.ID != "workmac" || created.Host.Status != "not_installed" {
		t.Fatalf("create: status %d, body %+v", res.StatusCode, created)
	}
	if created.Host.AutoSync == nil || !*created.Host.AutoSync || created.Host.Sync != nil {
		t.Fatalf("a new host has auto sync on and no sync state before its daemon exists: %+v", created.Host)
	}
	if created.Generation != gen+1 {
		t.Fatalf("generation did not advance: %d", created.Generation)
	}
	if _, exists := m.Get().Config.Hosts["workmac"]; !exists {
		t.Fatal("host not persisted to config")
	}

	res2, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(fmt.Sprintf(`{"id":"workmac","address":"again","expectedGeneration":%d}`, created.Generation)))
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create must conflict, got %d", res2.StatusCode)
	}
}

func TestHostsCreateRefusesReservedAndEmpty(t *testing.T) {
	ts, m := hostsCrudEnv(t)
	gen := m.Get().Generation
	for _, body := range []string{
		fmt.Sprintf(`{"id":"local","address":"x","expectedGeneration":%d}`, gen),
		fmt.Sprintf(`{"id":"","address":"x","expectedGeneration":%d}`, gen),
		fmt.Sprintf(`{"id":"mac","address":"","expectedGeneration":%d}`, gen),
	} {
		res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", body, res.StatusCode)
		}
	}
	if len(m.Get().Config.Hosts) != 0 {
		t.Fatalf("refused writes persisted hosts: %+v", m.Get().Config.Hosts)
	}
}

func TestHostsCreateStaleGenerationConflicts(t *testing.T) {
	ts, _ := hostsCrudEnv(t)
	res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(`{"id":"workmac","address":"x","expectedGeneration":99}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("stale generation must conflict, got %d", res.StatusCode)
	}
}

func TestHostsDeleteRemovesConfig(t *testing.T) {
	ts, m := hostsCrudEnv(t)
	res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(fmt.Sprintf(`{"id":"workmac","address":"x","expectedGeneration":%d}`, m.Get().Generation)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/hosts/workmac?expectedGeneration="+
		fmt.Sprint(m.Get().Generation), nil)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", res2.StatusCode)
	}
	if _, exists := m.Get().Config.Hosts["workmac"]; exists {
		t.Fatal("host still in config after delete")
	}
	if _, listed := listHosts(t, ts)["workmac"]; listed {
		t.Fatal("deleted host still listed")
	}
}

func TestHostsDeleteRefusesLocalAndUnknown(t *testing.T) {
	ts, _ := hostsCrudEnv(t)
	for path, want := range map[string]int{
		"/api/v1/hosts/local?expectedGeneration=1": http.StatusBadRequest,
		"/api/v1/hosts/ghost?expectedGeneration=1": http.StatusNotFound,
	} {
		req, err := http.NewRequest(http.MethodDelete, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("DELETE %s: expected %d, got %d", path, want, res.StatusCode)
		}
	}
}

func TestHostsReplaceRegistersDaemonAndKeepsAutoSync(t *testing.T) {
	ts, m := hostsCrudEnv(t)
	res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(fmt.Sprintf(`{"id":"workmac","address":"x","expectedGeneration":%d}`, m.Get().Generation)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res, _ := putHost(t, ts, "/api/v1/hosts/workmac/auto-sync", fmt.Sprintf(`{"enabled":false,"expectedGeneration":%d}`, m.Get().Generation)); res.StatusCode != http.StatusOK {
		t.Fatalf("auto sync off: status %d", res.StatusCode)
	}

	res2, replaced := putHost(t, ts, "/api/v1/hosts/workmac", fmt.Sprintf(`{"address":"x","daemonPort":10200,"expectedGeneration":%d}`, m.Get().Generation))
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("replace: status %d", res2.StatusCode)
	}
	if got := m.Get().Config.Hosts["workmac"]; got.DaemonPort != 10200 || got.SyncEnabled() {
		t.Fatalf("registering the daemon must keep auto sync off: %+v", got)
	}
	if replaced.Host.Status != "ok" || replaced.Host.Sync == nil || replaced.Host.Sync.State != hostsync.StateOff {
		t.Fatalf("installed host with auto sync off: %+v", replaced.Host)
	}
}

func TestHostsAutoSyncToggle(t *testing.T) {
	ts, m := hostsCrudEnv(t)
	res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(fmt.Sprintf(`{"id":"workmac","address":"x","daemonPort":10200,"expectedGeneration":%d}`, m.Get().Generation)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if st := listHosts(t, ts)["workmac"].Sync; st == nil || st.State != hostsync.StatePending {
		t.Fatalf("an installed host waits for its first push: %+v", st)
	}

	res, off := putHost(t, ts, "/api/v1/hosts/workmac/auto-sync", fmt.Sprintf(`{"enabled":false,"expectedGeneration":%d}`, m.Get().Generation))
	if res.StatusCode != http.StatusOK || off.Host.AutoSync == nil || *off.Host.AutoSync || off.Host.Sync.State != hostsync.StateOff {
		t.Fatalf("turn off: status %d, host %+v", res.StatusCode, off.Host)
	}
	if m.Get().Config.Hosts["workmac"].SyncEnabled() {
		t.Fatal("auto sync off was not persisted")
	}
	res, on := putHost(t, ts, "/api/v1/hosts/workmac/auto-sync", fmt.Sprintf(`{"enabled":true,"expectedGeneration":%d}`, m.Get().Generation))
	if res.StatusCode != http.StatusOK || !*on.Host.AutoSync || on.Host.Sync.State != hostsync.StatePending {
		t.Fatalf("turn on: status %d, host %+v", res.StatusCode, on.Host)
	}

	for _, tc := range []struct {
		path string
		body string
		want int
	}{
		{"/api/v1/hosts/local/auto-sync", fmt.Sprintf(`{"enabled":false,"expectedGeneration":%d}`, m.Get().Generation), http.StatusBadRequest},
		{"/api/v1/hosts/ghost/auto-sync", fmt.Sprintf(`{"enabled":false,"expectedGeneration":%d}`, m.Get().Generation), http.StatusNotFound},
		{"/api/v1/hosts/workmac/auto-sync", `{"enabled":false,"expectedGeneration":99}`, http.StatusConflict},
	} {
		if res, _ := putHost(t, ts, tc.path, tc.body); res.StatusCode != tc.want {
			t.Fatalf("PUT %s: expected %d, got %d", tc.path, tc.want, res.StatusCode)
		}
	}
}

func TestHostsReplaceRefusesLocalUnknownAndBadPort(t *testing.T) {
	ts, m := hostsCrudEnv(t)
	gen := m.Get().Generation
	for _, tc := range []struct {
		path string
		body string
		want int
	}{
		{"/api/v1/hosts/local", `{"address":"x","expectedGeneration":1}`, http.StatusBadRequest},
		{"/api/v1/hosts/ghost", `{"address":"x","expectedGeneration":1}`, http.StatusNotFound},
		{"/api/v1/hosts/workmac", fmt.Sprintf(`{"address":"x","daemonPort":70000,"expectedGeneration":%d}`, gen), http.StatusBadRequest},
	} {
		if res, _ := putHost(t, ts, tc.path, tc.body); res.StatusCode != tc.want {
			t.Fatalf("PUT %s: expected %d, got %d", tc.path, tc.want, res.StatusCode)
		}
	}
}

func TestIntegrationApplyForceQueryTakeover(t *testing.T) {
	dir := t.TempDir()
	ts, registry := integrationEnv(t)
	registerSandbox(t, registry, dir)
	grokPath := filepath.Join(dir, "grok", "config.toml")
	userTable := "[model.prism-codex-main-gpt-5-2-codex]\nmodel = \"codex-main/gpt-5.2-codex\"\n"
	writeFile(t, grokPath, "# user configuration\ntop_setting = \"keep\"\n\n"+userTable)

	res, err := http.Post(ts.URL+"/api/v1/integrations/grok/apply", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var plain integrations.ApplyResult
	if err := json.NewDecoder(res.Body).Decode(&plain); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if plain.OK || !plain.Retryable {
		t.Fatalf("plain apply must refuse retryable: %+v", plain)
	}

	res, err = http.Post(ts.URL+"/api/v1/integrations/grok/apply?force=true", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var forced integrations.ApplyResult
	if err := json.NewDecoder(res.Body).Decode(&forced); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if !forced.OK {
		t.Fatalf("forced apply: %+v", forced)
	}
}

func TestIntegrationApplyRollbackRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ts, registry := integrationEnv(t)
	registerSandbox(t, registry, dir)

	tomlSeed := "# user configuration\ntop_setting = \"keep\"\n\n[profile.default]\nmodel = \"gpt-5.2\"\n"
	clients := []struct {
		id       string
		path     string
		seed     string
		endpoint string
	}{
		{"codex", filepath.Join(dir, "codex", "config.toml"), tomlSeed, "http://127.0.0.1:8787/v1"},
		{"grok", filepath.Join(dir, "grok", "config.toml"), tomlSeed, "http://127.0.0.1:8787/v1"},
		{"omp", filepath.Join(dir, "omp", "models.yml"), "theme: dark\nproviders:\n  openai:\n    apiKey: sk-user\n    models: []\n", "http://127.0.0.1:8787/v1"},
		{"claude", filepath.Join(dir, "claude", "settings.json"), "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash\"\n    ]\n  }\n}\n", "http://127.0.0.1:8787"},
		{"pi", filepath.Join(dir, "pi", "models.json"), "{\n  \"providers\": {\n    \"acme-edge\": {\n      \"baseUrl\": \"http://127.0.0.1:9797/v1\",\n      \"api\": \"openai-completions\",\n      \"apiKey\": \"user-key\",\n      \"models\": []\n    }\n  }\n}\n", "http://127.0.0.1:8787/v1"},
		{"opencode", filepath.Join(dir, "opencode", "opencode.json"), "{\n  \"theme\": \"dark\",\n  \"providers\": {\n    \"acme\": {\n      \"name\": \"ACME\",\n      \"package\": \"@opencode-ai/ai/providers/openai-compatible\",\n      \"settings\": {\n        \"baseURL\": \"http://localhost:8080/v1\"\n      },\n      \"models\": {}\n    }\n  }\n}\n", "http://127.0.0.1:8787/v1"},
		{"hermes", filepath.Join(dir, "hermes", "config.yaml"), "model:\n  default: nous/ox-alpha\nproviders:\n  acme:\n    api: http://localhost:8080/v1\n    api_key: user-key\n    api_mode: chat_completions\n    discover_models: true\n", "http://127.0.0.1:8787/v1"},
	}
	for _, client := range clients {
		writeFile(t, client.path, client.seed)
	}

	for _, client := range clients {
		res, err := http.Post(ts.URL+"/api/v1/integrations/"+client.id+"/apply", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		var result integrations.ApplyResult
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK || !result.OK {
			t.Fatalf("%s apply: status %d, %+v", client.id, res.StatusCode, result)
		}
	}

	for _, client := range clients {
		res, err := http.Get(ts.URL + "/api/v1/integrations/" + client.id)
		if err != nil {
			t.Fatal(err)
		}
		var status integrations.Status
		if err := json.NewDecoder(res.Body).Decode(&status); err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if !status.Managed || status.Endpoint == nil || *status.Endpoint != client.endpoint {
			t.Fatalf("%s status after apply: %+v", client.id, status)
		}
	}

	gotCodex := readFile(t, filepath.Join(dir, "codex", "config.toml"))
	if !strings.Contains(gotCodex, integrations.CodexFence.Begin) || !strings.Contains(gotCodex, "base_url = \"http://127.0.0.1:8787/v1\"") || !strings.Contains(gotCodex, "top_setting = \"keep\"") {
		t.Fatalf("codex file after apply:\n%s", gotCodex)
	}

	for _, client := range clients {
		res, err := http.Post(ts.URL+"/api/v1/integrations/"+client.id+"/rollback", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		var result integrations.ApplyResult
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if !result.OK {
			t.Fatalf("%s rollback: %+v", client.id, result)
		}
	}
	for _, client := range clients {
		if got := readFile(t, client.path); got != client.seed {
			t.Fatalf("%s rollback did not restore seed:\nGOT:\n%q\nWANT:\n%q", client.id, got, client.seed)
		}
	}
}

func TestIntegrationRollbackUnmanagedHonestRefusal(t *testing.T) {
	dir := t.TempDir()
	ts, registry := integrationEnv(t)
	registerSandbox(t, registry, dir)
	codexPath := filepath.Join(dir, "codex", "config.toml")
	seed := "# user only\n"
	writeFile(t, codexPath, seed)

	res, err := http.Post(ts.URL+"/api/v1/integrations/codex/rollback", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result integrations.ApplyResult
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if result.OK {
		t.Fatal("rollback of unmanaged client reported ok")
	}
	if got := readFile(t, codexPath); got != seed {
		t.Fatal("rollback of unmanaged client wrote bytes")
	}
}

func TestIntegrationStatusResponseHasNoFileBytes(t *testing.T) {
	dir := t.TempDir()
	ts, registry := integrationEnv(t)
	registerSandbox(t, registry, dir)
	writeFile(t, filepath.Join(dir, "codex", "config.toml"), "top_setting = \"secret-value-xyz\"\n")

	res, err := http.Get(ts.URL + "/api/v1/integrations/codex")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if strings.Contains(string(raw), "secret-value-xyz") {
		t.Fatal("status response leaked file bytes")
	}
	if !strings.Contains(string(raw), "\"managed\"") || !strings.Contains(string(raw), "\"targetPath\"") {
		t.Fatalf("status response missing contract fields: %s", raw)
	}
}

func TestIntegrationConcurrentApplySerializes(t *testing.T) {
	dir := t.TempDir()
	ts, registry := integrationEnv(t)
	registerSandbox(t, registry, dir)
	codexPath := filepath.Join(dir, "codex", "config.toml")

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.Post(ts.URL+"/api/v1/integrations/codex/apply", "application/json", nil)
			if err != nil {
				errs <- err
				return
			}
			var result integrations.ApplyResult
			if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
				errs <- err
			}
			res.Body.Close()
			if res.StatusCode != http.StatusOK || !result.OK {
				errs <- &applyError{result: result, status: res.StatusCode}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	got := readFile(t, codexPath)
	if !strings.Contains(got, integrations.CodexFence.Begin) || !strings.Contains(got, integrations.CodexFence.End) {
		t.Fatalf("concurrent applies corrupted the file:\n%s", got)
	}
	if count := strings.Count(got, integrations.CodexFence.Begin); count != 1 {
		t.Fatalf("concurrent applies produced %d fence blocks", count)
	}
}

type applyError struct {
	result integrations.ApplyResult
	status int
}

func (e *applyError) Error() string {
	return "apply failed: status " + itoa2(e.status) + " " + e.result.Reason
}

func itoa2(n int) string { return strconv.Itoa(n) }

func putJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	res, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	out, err := http.DefaultClient.Do(res)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestIntegrationRollbackForceQueryResetsJournal(t *testing.T) {
	dir := t.TempDir()
	ts, registry := integrationEnv(t)
	registerSandbox(t, registry, dir)
	modelsPath := filepath.Join(dir, "omp", "models.yml")
	seed := "theme: dark\nproviders:\n  openai:\n    apiKey: sk-user\n    models: []\n"
	writeFile(t, modelsPath, seed)

	post := func(query string) integrations.ApplyResult {
		t.Helper()
		res, err := http.Post(ts.URL+"/api/v1/integrations/omp/"+query, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var result integrations.ApplyResult
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := post("apply"); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	journalPath := filepath.Join(dir, "omp", ".models.yml.prism-journal.json")
	writeFile(t, journalPath, strings.Replace(readFile(t, journalPath), `"version": 1`, `"version": 2`, 1))

	plain := post("rollback")
	if plain.OK || !plain.Retryable || plain.Conflict != integrations.ConflictJournal {
		t.Fatalf("plain rollback: %+v", plain)
	}
	forced := post("rollback?force=true")
	if !forced.OK || forced.Backup != journalPath+".bak" {
		t.Fatalf("forced rollback: %+v", forced)
	}
	if got := readFile(t, modelsPath); got != seed {
		t.Fatalf("file after forced rollback:\n%q", got)
	}
}
