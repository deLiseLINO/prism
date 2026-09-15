package integrations

import (
	"path/filepath"
	"strings"
	"testing"
)

type registryFixture struct {
	codex     string
	grok      string
	omp       string
	claude    string
	pi        string
	opencode  string
	opencode2 string
	hermes    string
}

func newRegistryFixture(t *testing.T) registryFixture {
	t.Helper()
	dir := t.TempDir()
	return registryFixture{
		codex:     tempFile(t, dir, "codex.toml", userToml),
		grok:      tempFile(t, dir, "grok.toml", userToml),
		omp:       tempFile(t, dir, "models.yml", userModelYaml),
		claude:    tempFile(t, dir, "settings.json", "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash\"\n    ]\n  }\n}\n"),
		pi:        tempFile(t, dir, "pi-models.json", "{\n  \"providers\": {\n    \"edge-vps\": {\n      \"baseUrl\": \"http://127.0.0.1:9797/v1\",\n      \"api\": \"openai-completions\",\n      \"apiKey\": \"user-key\",\n      \"models\": []\n    }\n  }\n}\n"),
		opencode:  tempFile(t, dir, "opencode-v1.json", "{\n  \"theme\": \"dark\",\n  \"provider\": {\n    \"acme\": {\n      \"name\": \"ACME\",\n      \"npm\": \"@ai-sdk/openai-compatible\",\n      \"options\": {\n        \"baseURL\": \"http://localhost:8080/v1\"\n      },\n      \"models\": {}\n    }\n  }\n}\n"),
		opencode2: tempFile(t, dir, "opencode-v2.json", "{\n  \"theme\": \"dark\",\n  \"providers\": {\n    \"acme\": {\n      \"name\": \"ACME\",\n      \"package\": \"@opencode-ai/ai/providers/openai-compatible\",\n      \"settings\": {\n        \"baseURL\": \"http://localhost:8080/v1\"\n      },\n      \"models\": {}\n    }\n  }\n}\n"),
		hermes:    tempFile(t, dir, "hermes-config.yaml", "model:\n  default: nous/ox-alpha\nproviders:\n  acme:\n    api: http://localhost:8080/v1\n    api_key: user-key\n    api_mode: chat_completions\n    discover_models: true\n"),
	}
}

func wiredRegistry(f registryFixture) *Registry {
	registry := NewRegistry()
	registrations := []struct {
		module Module
	}{
		{NewCodex(CodexOptions{Port: testPort, ConfigPath: f.codex})},
		{NewGrok(GrokOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: f.grok})},
		{NewOmp(OmpOptions{Port: testPort, Models: DefaultPrismModels, ModelsPath: f.omp})},
		{NewClaude(ClaudeOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: f.claude})},
		{NewPi(PiOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: f.pi})},
		{NewOpencode(OpencodeOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: f.opencode})},
		{NewOpencode2(Opencode2Options{Port: testPort, Models: DefaultPrismModels, ConfigPath: f.opencode2})},
		{NewHermes(HermesOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: f.hermes})},
	}
	for _, registration := range registrations {
		if err := registry.Register(registration.module); err != nil {
			panic(err)
		}
	}
	return registry
}

func TestRegistryWiringRoundTrip(t *testing.T) {
	fixture := newRegistryFixture(t)
	registry := wiredRegistry(fixture)

	for _, status := range registry.Status() {
		if !status.Installed {
			t.Fatalf("%s not installed: %+v", status.ID, status)
		}
	}
	for _, id := range IDs {
		if result := registry.Apply(id); !result.OK {
			t.Fatalf("apply %s: %+v", id, result)
		}
	}
	for _, status := range registry.Status() {
		if !status.Managed {
			t.Fatalf("%s not managed after apply: %+v", status.ID, status)
		}
	}
	for _, id := range IDs {
		if result := registry.Rollback(id); !result.OK {
			t.Fatalf("rollback %s: %+v", id, result)
		}
	}
	for _, status := range registry.Status() {
		if status.Managed {
			t.Fatalf("%s still managed after rollback: %+v", status.ID, status)
		}
	}
}

func TestRegistryHonestTargetPaths(t *testing.T) {
	fixture := newRegistryFixture(t)
	registry := wiredRegistry(fixture)
	for _, id := range IDs {
		if result := registry.Apply(id); !result.OK {
			t.Fatalf("apply %s: %+v", id, result)
		}
	}
	statuses := registry.Status()
	if len(statuses) != len(IDs) {
		t.Fatalf("status count: %d", len(statuses))
	}
	targets := map[ID]string{
		Codex:     fixture.codex,
		Grok:      fixture.grok,
		Omp:       fixture.omp,
		Claude:    fixture.claude,
		Pi:        fixture.pi,
		Opencode:  fixture.opencode,
		Opencode2: fixture.opencode2,
		Hermes:    fixture.hermes,
	}
	gotIDs := []string{}
	for _, status := range statuses {
		gotIDs = append(gotIDs, string(status.ID))
		if !status.Installed {
			t.Errorf("%s not installed: %+v", status.ID, status)
		}
		if status.TargetPath == nil || *status.TargetPath != targets[status.ID] {
			t.Errorf("%s target path: %+v", status.ID, status)
		}
	}
	if strings.Join(gotIDs, ",") != "codex,grok,omp,claude,pi,opencode,opencode2,hermes" {
		t.Errorf("status order: %v", gotIDs)
	}
	if result := registry.Rollback(Codex); !result.OK {
		t.Fatalf("rollback codex: %+v", result)
	}
	for _, status := range registry.Status() {
		if status.ID == Codex && status.Managed {
			t.Fatalf("codex still managed: %+v", status)
		}
	}
}

func TestRegistryUnregistered(t *testing.T) {
	registry := NewRegistry()
	if result := registry.Apply(Codex); result.OK || result.Reason == "" {
		t.Fatalf("unregistered apply: %+v", result)
	}
	if result := registry.Rollback(Codex); result.OK || result.Reason == "" {
		t.Fatalf("unregistered rollback: %+v", result)
	}
	statuses := registry.Status()
	if len(statuses) != len(IDs) {
		t.Fatalf("status count: %d", len(statuses))
	}
	for _, status := range statuses {
		if status.Installed || status.Managed || status.TargetPath != nil || status.Detail != UnregisteredDetail {
			t.Fatalf("unregistered status: %+v", status)
		}
	}
}

func TestRegistryDuplicateRegister(t *testing.T) {
	dir := t.TempDir()
	registry := NewRegistry()
	if err := registry.Register(NewCodex(CodexOptions{Port: testPort, ConfigPath: filepath.Join(dir, "config.toml")})); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := registry.Register(NewCodex(CodexOptions{Port: testPort, ConfigPath: filepath.Join(dir, "config.toml")})); err == nil {
		t.Fatal("duplicate register accepted")
	} else if !contains(err.Error(), "already registered") {
		t.Fatalf("duplicate error: %v", err)
	}
}

func TestFenceMarkersDistinct(t *testing.T) {
	if CodexFence.Begin == GrokFence.Begin {
		t.Fatal("codex and grok fence begins collide")
	}
}

func TestValidID(t *testing.T) {
	for _, id := range IDs {
		if got, ok := ValidID(string(id)); !ok || got != id {
			t.Errorf("valid id %s rejected", id)
		}
	}
	for _, bad := range []string{"", "disable", "codexx"} {
		if _, ok := ValidID(bad); ok {
			t.Errorf("invalid id %q accepted", bad)
		}
	}
}
