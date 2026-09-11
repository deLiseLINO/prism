package management

import (
	"context"
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

	"prism/internal/config"
	"prism/internal/integrations"
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

func registerSandbox(t *testing.T, registry *integrations.Registry, dir string) {
	t.Helper()
	codexPath := filepath.Join(dir, "codex", "config.toml")
	grokPath := filepath.Join(dir, "grok", "config.toml")
	modelsPath := filepath.Join(dir, "omp", "models.yml")
	claudePath := filepath.Join(dir, "claude", "settings.json")
	piPath := filepath.Join(dir, "pi", "models.json")
	opencodePath := filepath.Join(dir, "opencode", "opencode.json")
	opencode2Path := filepath.Join(dir, "opencode2", "opencode.json")
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
		{integrations.NewOpencode2(integrations.Opencode2Options{Port: 8787, Models: integrations.DefaultPrismModels, ConfigPath: opencode2Path})},
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
	if len(body.Integrations) != 8 {
		t.Fatalf("expected 8 statuses, got %d", len(body.Integrations))
	}
	for i, want := range []integrations.ID{integrations.Codex, integrations.Grok, integrations.Omp, integrations.Claude, integrations.Pi, integrations.Opencode, integrations.Opencode2, integrations.Hermes} {
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

func TestHostScopedIntegrationsRouteToRemoteRegistry(t *testing.T) {
	dir := t.TempDir()
	_, registry := integrationEnv(t)
	registerSandbox(t, registry, dir)
	remote := integrations.NewRegistry()
	remoteDir := filepath.Join(dir, "remote")
	registerSandbox(t, remote, remoteDir)
	srv := New(&fakePool{}, nil, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}}, &fakeAuth{}, registry, nil, nil)
	table := NewHostRegistries(registry)
	table.SetRemote("workmac", remote)
	table.SetUnresolved("deadhost", "ssh: connection refused")
	srv.SetHostRegistries(table)
	hostTS := httptest.NewServer(srv.Handler())
	t.Cleanup(hostTS.Close)

	res, err := http.Get(hostTS.URL + "/api/v1/hosts/local/integrations")
	if err != nil {
		t.Fatal(err)
	}
	var localBody IntegrationsResponse
	if err := json.NewDecoder(res.Body).Decode(&localBody); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(localBody.Integrations) != 8 {
		t.Fatalf("local host: expected 8 statuses, got %d", len(localBody.Integrations))
	}

	res, err = http.Get(hostTS.URL + "/api/v1/hosts/workmac/integrations")
	if err != nil {
		t.Fatal(err)
	}
	var remoteBody IntegrationsResponse
	if err := json.NewDecoder(res.Body).Decode(&remoteBody); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(remoteBody.Integrations) != 8 {
		t.Fatalf("remote host: expected 8 statuses, got %d", len(remoteBody.Integrations))
	}
	for _, status := range remoteBody.Integrations {
		if status.TargetPath == nil || !strings.Contains(*status.TargetPath, "remote"+string(filepath.Separator)) {
			t.Fatalf("remote status routed to the local sandbox: %+v", status)
		}
	}

	res, err = http.Get(hostTS.URL + "/api/v1/hosts/deadhost/integrations")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unresolved host: status %d, want 503", res.StatusCode)
	}
	res, err = http.Get(hostTS.URL + "/api/v1/hosts/nope/integrations")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unknown host: status %d, want 503", res.StatusCode)
	}
}

type recordingLifecycle struct {
	ensured  []string
	removed  []string
	ensureEr func(id string) error
}

func (l *recordingLifecycle) Ensure(ctx context.Context, id string, address string) error {
	l.ensured = append(l.ensured, id+"="+address)
	if l.ensureEr != nil {
		return l.ensureEr(id)
	}
	return nil
}

func (l *recordingLifecycle) Remove(id string) {
	l.removed = append(l.removed, id)
}

func hostsCrudEnv(t *testing.T, lifecycle HostLifecycle) (*httptest.Server, *config.Manager, *HostRegistries) {
	t.Helper()
	m, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := integrations.NewRegistry()
	table := NewHostRegistries(registry)
	srv := New(&fakePool{}, m, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}}, &fakeAuth{}, registry, nil, nil)
	srv.SetHostRegistries(table)
	srv.SetHostLifecycle(lifecycle)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, m, table
}

func TestHostsCreatePersistsAndTriggersLifecycle(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	ts, m, table := hostsCrudEnv(t, lifecycle)
	gen := m.Get().Generation

	res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(fmt.Sprintf(`{"id":"workmac","address":"user@host","expectedGeneration":%d}`, gen)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created HostMutationResponse
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || created.Host.ID != "workmac" || created.Host.Status != "ok" {
		t.Fatalf("create: status %d, body %+v", res.StatusCode, created)
	}
	if created.Generation != gen+1 {
		t.Fatalf("generation did not advance: %d", created.Generation)
	}
	if len(lifecycle.ensured) != 1 || lifecycle.ensured[0] != "workmac=user@host" {
		t.Fatalf("lifecycle not triggered: %+v", lifecycle.ensured)
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
	_ = table
}

func TestHostsCreateRefusesReservedAndEmpty(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	ts, m, _ := hostsCrudEnv(t, lifecycle)
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
	if len(lifecycle.ensured) != 0 {
		t.Fatalf("lifecycle must not run for refused writes: %+v", lifecycle.ensured)
	}
}

func TestHostsCreateStaleGenerationConflicts(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	ts, _, _ := hostsCrudEnv(t, lifecycle)
	res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(`{"id":"workmac","address":"x","expectedGeneration":99}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("stale generation must conflict, got %d", res.StatusCode)
	}
	if len(lifecycle.ensured) != 0 {
		t.Fatal("lifecycle must not run on a refused write")
	}
}

func TestHostsCreateUnreachableHostIsHonest(t *testing.T) {
	lifecycle := &recordingLifecycle{ensureEr: func(id string) error {
		return fmt.Errorf("ssh %s: connection timed out", id)
	}}
	ts, m, _ := hostsCrudEnv(t, lifecycle)
	gen := m.Get().Generation

	res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(fmt.Sprintf(`{"id":"deadhost","address":"10.9.9.9","expectedGeneration":%d}`, gen)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created HostMutationResponse
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("config write must succeed even when the probe fails, got %d", res.StatusCode)
	}
	if created.Host.Status != "unresolved" || created.Host.Detail == "" {
		t.Fatalf("unreachable host must be reported unresolved with a reason, got %+v", created.Host)
	}
	if _, exists := m.Get().Config.Hosts["deadhost"]; !exists {
		t.Fatal("host must persist despite probe failure")
	}
}

func TestHostsDeleteRemovesConfigAndTearsDown(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	ts, m, table := hostsCrudEnv(t, lifecycle)
	gen := m.Get().Generation
	res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(fmt.Sprintf(`{"id":"workmac","address":"x","expectedGeneration":%d}`, gen)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	table.SetRemote("workmac", integrations.NewRegistry())

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
	if len(lifecycle.removed) != 1 || lifecycle.removed[0] != "workmac" {
		t.Fatalf("lifecycle teardown not triggered: %+v", lifecycle.removed)
	}
	for _, view := range table.List() {
		if view.ID == "workmac" {
			t.Fatal("deleted host still listed")
		}
	}
}

func TestHostsDeleteRefusesLocalAndUnknown(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	ts, _, _ := hostsCrudEnv(t, lifecycle)
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

func TestHostsReplaceFlipsToExternalAndTearsDown(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	ts, m, table := hostsCrudEnv(t, lifecycle)
	gen := m.Get().Generation
	res, err := http.Post(ts.URL+"/api/v1/hosts", "application/json",
		strings.NewReader(fmt.Sprintf(`{"id":"workmac","address":"x","expectedGeneration":%d}`, gen)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	table.SetRemote("workmac", integrations.NewRegistry())

	body := fmt.Sprintf(`{"address":"x","daemonPort":10200,"expectedGeneration":%d}`, m.Get().Generation)
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/hosts/workmac", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("replace: status %d", res2.StatusCode)
	}
	if got := m.Get().Config.Hosts["workmac"]; got.DaemonPort != 10200 || got.Address != "x" {
		t.Fatalf("config after replace: %+v", got)
	}
	if len(lifecycle.removed) != 1 || lifecycle.removed[0] != "workmac" {
		t.Fatalf("lifecycle removals after replace: %v", lifecycle.removed)
	}
	if _, ok, _ := table.Lookup("workmac"); ok {

		t.Fatal("external host must not resolve to a registry")
	}
	external := false
	for _, view := range table.List() {
		if view.ID == "workmac" && view.DaemonPort == 10200 && view.Status == "ok" {
			external = true
		}
	}
	if !external {
		t.Fatal("replaced host must list as ok with its daemon port")
	}
}

func TestHostsReplaceRefusesLocalUnknownAndBadPort(t *testing.T) {
	lifecycle := &recordingLifecycle{}
	ts, m, _ := hostsCrudEnv(t, lifecycle)
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
		req, err := http.NewRequest(http.MethodPut, ts.URL+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
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
		{"opencode", filepath.Join(dir, "opencode", "opencode.json"), "{\n  \"theme\": \"dark\",\n  \"provider\": {\n    \"prism\": {\n      \"name\": \"Router\",\n      \"npm\": \"@ai-sdk/openai-compatible\",\n      \"options\": {\n        \"baseURL\": \"http://localhost:8080/v1\"\n      },\n      \"models\": {}\n    }\n  }\n}\n", "http://127.0.0.1:8787/v1"},
		{"opencode2", filepath.Join(dir, "opencode2", "opencode.json"), "{\n  \"theme\": \"dark\",\n  \"providers\": {\n    \"prism\": {\n      \"name\": \"Router\",\n      \"package\": \"@opencode-ai/ai/providers/openai-compatible\",\n      \"settings\": {\n        \"baseURL\": \"http://localhost:8080/v1\"\n      },\n      \"models\": {}\n    }\n  }\n}\n", "http://127.0.0.1:8787/v1"},
		{"hermes", filepath.Join(dir, "hermes", "config.yaml"), "model:\n  default: nous/ox-alpha\nproviders:\n  prism:\n    api: http://localhost:8080/v1\n    api_key: user-key\n    api_mode: chat_completions\n    discover_models: true\n", "http://127.0.0.1:8787/v1"},
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
