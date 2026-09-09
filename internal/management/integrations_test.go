package management

import (
	"encoding/json"
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
	srv := New(&fakePool{}, m, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}}, &fakeAuth{}, registry, nil)
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
		{"pi", filepath.Join(dir, "pi", "models.json"), "{\n  \"providers\": {\n    \"edge-vps\": {\n      \"baseUrl\": \"http://127.0.0.1:9797/v1\",\n      \"api\": \"openai-completions\",\n      \"apiKey\": \"user-key\",\n      \"models\": []\n    }\n  }\n}\n", "http://127.0.0.1:8787/v1"},
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
