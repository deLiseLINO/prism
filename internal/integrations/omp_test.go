package integrations

import (
	"path/filepath"
	"strings"
	"testing"
)

func ompLeaf(port int) string {
	return strings.Join([]string{
		"  prism:",
		"    baseUrl: http://127.0.0.1:" + itoa(port) + "/v1",
		"    api: openai-completions",
		"    apiKey: prism-loopback",
		"    models:",
		"      - id: gpt-5.2-codex",
		"        name: GPT-5.2 Codex",
		"        input:",
		"          - text",
		"        reasoning: true",
		"        thinking:",
		"          mode: effort",
		"          efforts:",
		"            - minimal",
		"            - low",
		"            - medium",
		"            - high",
		"            - xhigh",
		"          defaultLevel: medium",
		"      - id: gemini-3-pro",
		"        name: Gemini 3 Pro",
		"        input:",
		"          - text",
		"        reasoning: true",
		"        thinking:",
		"          mode: effort",
		"          efforts:",
		"            - minimal",
		"            - low",
		"            - medium",
		"            - high",
		"            - xhigh",
		"          defaultLevel: medium",
	}, "\n")
}

func TestOmpAgentDirResolution(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want string
	}{
		{"default", Env{}, "/home/u/.omp/agent"},
		{"pi config dir name", Env{"PI_CONFIG_DIR": "custom-omp"}, "/home/u/custom-omp/agent"},
		{"leading slash pi config dir", Env{"PI_CONFIG_DIR": "/custom"}, "/home/u/custom/agent"},
		{"coding agent dir absolute", Env{"PI_CODING_AGENT_DIR": "/abs/agent"}, "/abs/agent"},
		{"coding agent dir tilde", Env{"PI_CODING_AGENT_DIR": "~/agent"}, "/home/u/agent"},
		{"coding agent dir tilde alone", Env{"PI_CODING_AGENT_DIR": "~"}, "/home/u"},
		{"omp profile", Env{"OMP_PROFILE": "work"}, "/home/u/.omp/profiles/work/agent"},
		{"profile shadows coding dir", Env{"OMP_PROFILE": "work", "PI_CODING_AGENT_DIR": "/abs/agent"}, "/home/u/.omp/profiles/work/agent"},
		{"profile with custom config dir", Env{"OMP_PROFILE": "work", "PI_CONFIG_DIR": "custom-omp"}, "/home/u/custom-omp/profiles/work/agent"},
		{"pi profile fallback", Env{"PI_PROFILE": "legacy"}, "/home/u/.omp/profiles/legacy/agent"},
		{"omp profile wins over pi profile", Env{"OMP_PROFILE": "work", "PI_PROFILE": "legacy"}, "/home/u/.omp/profiles/work/agent"},
		{"default profile unset", Env{"OMP_PROFILE": "default"}, "/home/u/.omp/agent"},
		{"blank profile unset", Env{"OMP_PROFILE": "  "}, "/home/u/.omp/agent"},
	}
	for _, tc := range cases {
		got, err := OmpAgentDir(tc.env, "/home/u")
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestOmpAgentDirRefusesInvalidInputs(t *testing.T) {
	for _, env := range []Env{
		{"PI_CODING_AGENT_DIR": "rel/agent"},
		{"OMP_PROFILE": ".."},
		{"OMP_PROFILE": "a/b"},
		{"OMP_PROFILE": "work."},
	} {
		if _, err := OmpAgentDir(env, "/home/u"); err == nil {
			t.Errorf("expected error for %v", env)
		}
	}
	if _, err := OmpAgentDir(Env{"PI_CODING_AGENT_DIR": "rel/agent"}, "/home/u"); err == nil || !contains(err.Error(), "must be absolute or start with ~") {
		t.Errorf("coding dir error: %v", err)
	}
	for _, env := range []Env{{"OMP_PROFILE": ".."}, {"OMP_PROFILE": "a/b"}, {"OMP_PROFILE": "work."}} {
		if _, err := OmpAgentDir(env, "/home/u"); err == nil || !contains(err.Error(), "invalid OMP profile") {
			t.Errorf("profile error for %v: %v", env, err)
		}
	}
}

func TestOmpModelsConfigPathFallback(t *testing.T) {
	dir := t.TempDir()
	if got := OmpModelsConfigPath(dir, func(string) bool { return false }); got != filepath.Join(dir, "models.yml") {
		t.Errorf("no files: got %q", got)
	}
	if got := OmpModelsConfigPath(dir, func(p string) bool { return strings.HasSuffix(p, "models.yaml") }); got != filepath.Join(dir, "models.yaml") {
		t.Errorf("yaml fallback: got %q", got)
	}
	if got := OmpModelsConfigPath(dir, func(string) bool { return true }); got != filepath.Join(dir, "models.yml") {
		t.Errorf("yml present: got %q", got)
	}
}

func TestOmpApplyCreatesLeafInFreshDocument(t *testing.T) {
	dir := t.TempDir()
	modelsPath := tempFile(t, dir, "models.yml", "")
	if outcome := WriteOmpConfig(OmpOptions{ModelsPath: modelsPath, Port: testPort, Models: ompTestModels}); outcome.Kind != OutcomeWritten {
		t.Fatalf("apply: %+v", outcome)
	}
	want := "providers:\n" + ompLeaf(testPort) + "\n"
	if got := readFile(t, modelsPath); got != want {
		t.Fatalf("fresh document:\n%q\nwant:\n%q", got, want)
	}
}

func TestOmpApplyPatchesOnlyPrismLeaf(t *testing.T) {
	dir := t.TempDir()
	modelsPath := tempFile(t, dir, "models.yml", userModelYaml)
	if outcome := WriteOmpConfig(OmpOptions{ModelsPath: modelsPath, Port: testPort, Models: ompTestModels}); outcome.Kind != OutcomeWritten {
		t.Fatalf("apply: %+v", outcome)
	}
	want := userModelYaml + ompLeaf(testPort) + "\n"
	if got := readFile(t, modelsPath); got != want {
		t.Fatalf("patched document:\n%q\nwant:\n%q", got, want)
	}
}

func TestOmpIntegrationAppliesLiveModelsSource(t *testing.T) {
	dir := t.TempDir()
	modelsPath := tempFile(t, dir, "models.yml", userModelYaml)
	live := []Model{{ID: "router/glm-5.3", Name: "router/glm-5.3"}}
	o := NewOmp(OmpOptions{ModelsPath: modelsPath, Port: testPort, Models: DefaultPrismModels, ModelsSource: func() []Model { return live }})
	if res := o.Apply(); !res.OK {
		t.Fatalf("apply: %+v", res)
	}
	content := readFile(t, modelsPath)
	if !strings.Contains(content, "id: router/glm-5.3") {
		t.Fatalf("live model missing from prism leaf:\n%s", content)
	}
}

func TestOmpApplyIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	modelsPath := tempFile(t, dir, "models.yml", userModelYaml)
	WriteOmpConfig(OmpOptions{ModelsPath: modelsPath, Port: testPort, Models: ompTestModels})
	afterFirst := readFile(t, modelsPath)
	if outcome := WriteOmpConfig(OmpOptions{ModelsPath: modelsPath, Port: testPort, Models: ompTestModels}); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("second apply: %+v", outcome)
	}
	if got := readFile(t, modelsPath); got != afterFirst {
		t.Fatal("second apply changed the file")
	}
}

func TestOmpReapplyRewritesOwnPrismLeaf(t *testing.T) {
	dir := t.TempDir()
	modelsPath := tempFile(t, dir, "models.yml", userModelYaml)
	WriteOmpConfig(OmpOptions{ModelsPath: modelsPath, Port: testPort, Models: ompTestModels})
	// Any content difference in the prism leaf — user edit or prism's own newer
	// output — is rewritten in place; the leaf is prism-owned.
	edited := replaceOne(readFile(t, modelsPath), "baseUrl: http://127.0.0.1:8787/v1", "baseUrl: http://127.0.0.1:8787/v1-mutated")
	writeFileOrDie(t, modelsPath, edited)
	outcome := WriteOmpConfig(OmpOptions{ModelsPath: modelsPath, Port: testPort, Models: ompTestModels})
	if outcome.Kind != OutcomeWritten {
		t.Fatalf("apply over edit: %+v", outcome)
	}
	got := readFile(t, modelsPath)
	if !strings.Contains(got, userModelYaml) || strings.Contains(got, "mutated") {
		t.Fatalf("rewrite lost user bytes or kept foreign edit:\n%s", got)
	}
	if !strings.Contains(got, "baseUrl: http://127.0.0.1:8787/v1") {
		t.Fatalf("prism leaf missing after rewrite:\n%s", got)
	}
}

func TestOmpApplyIsFailClosed(t *testing.T) {
	dir := t.TempDir()

	flow := tempFile(t, dir, "flow.yml", "providers: {}\n")
	if outcome := WriteOmpConfig(OmpOptions{ModelsPath: flow, Port: testPort, Models: ompTestModels}); outcome.Kind != OutcomeRefused {
		t.Fatalf("flow-style providers: %+v", outcome)
	}
	if got := readFile(t, flow); got != "providers: {}\n" {
		t.Fatal("flow file changed")
	}

	duplicate := tempFile(t, dir, "duplicate.yml", "providers:\n  a: {}\n---\nproviders:\n  b: {}\n")
	if outcome := WriteOmpConfig(OmpOptions{ModelsPath: duplicate, Port: testPort, Models: ompTestModels}); outcome.Kind != OutcomeRefused {
		t.Fatalf("duplicate top-level providers: %+v", outcome)
	}

	tabs := tempFile(t, dir, "tabs.yml", "providers:\n\topenai:\n\t\t models: []\n")
	if outcome := WriteOmpConfig(OmpOptions{ModelsPath: tabs, Port: testPort, Models: ompTestModels}); outcome.Kind != OutcomeRefused {
		t.Fatalf("tab indentation: %+v", outcome)
	}
	if got := readFile(t, tabs); got != "providers:\n\topenai:\n\t\t models: []\n" {
		t.Fatal("tabs file changed")
	}
}

func TestOmpRollbackRemovesLeafAndPrunesEmptyContainer(t *testing.T) {
	dir := t.TempDir()
	modelsPath := tempFile(t, dir, "models.yml", userModelYaml)
	WriteOmpConfig(OmpOptions{ModelsPath: modelsPath, Port: testPort, Models: ompTestModels})
	if outcome := StripOmpConfig(modelsPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("rollback: %+v", outcome)
	}
	if got := readFile(t, modelsPath); got != userModelYaml {
		t.Fatalf("rollback did not restore user yaml:\n%q", got)
	}

	fresh := tempFile(t, dir, "fresh.yml", "")
	WriteOmpConfig(OmpOptions{ModelsPath: fresh, Port: testPort, Models: ompTestModels})
	if outcome := StripOmpConfig(fresh); outcome.Kind != OutcomeWritten {
		t.Fatalf("fresh rollback: %+v", outcome)
	}
	if got := readFile(t, fresh); got != "" {
		t.Fatalf("fresh rollback left %q", got)
	}
}

func TestOmpIntegrationModuleStatus(t *testing.T) {
	dir := t.TempDir()
	absentAgent := filepath.Join(dir, "absent-agent")
	missing := NewOmp(OmpOptions{Port: testPort, Models: ompTestModels, AgentDir: absentAgent})
	status := missing.Status()
	if status.Installed || status.Managed || status.TargetPath == nil || *status.TargetPath != filepath.Join(absentAgent, "models.yml") || status.Detail != "not installed" {
		t.Fatalf("missing status: %+v", status)
	}

	agentDir := filepath.Join(dir, "agent")
	modelsPath := tempFile(t, agentDir, "models.yml", userModelYaml)
	installed := NewOmp(OmpOptions{Port: testPort, Models: ompTestModels, AgentDir: agentDir})
	status = installed.Status()
	if !status.Installed || status.Managed || status.TargetPath == nil || *status.TargetPath != modelsPath || status.Detail != "installed; no prism-managed bytes present" {
		t.Fatalf("installed-unmanaged status: %+v", status)
	}
}

func TestOmpIntegrationModuleEndpointAndDrift(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agent")
	tempFile(t, agentDir, "models.yml", userModelYaml)
	integration := NewOmp(OmpOptions{Port: testPort, Models: ompTestModels, AgentDir: agentDir})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	status := integration.Status()
	if !status.Managed || status.Endpoint == nil || *status.Endpoint != "http://127.0.0.1:8787/v1" || status.Drift {
		t.Fatalf("managed status: %+v", status)
	}
	drifted := NewOmp(OmpOptions{Port: testPort + 1, Models: ompTestModels, AgentDir: agentDir})
	status = drifted.Status()
	if !status.Drift || !contains(status.Detail, "drifted") {
		t.Fatalf("drifted status: %+v", status)
	}
}

func TestOmpIntegrationModuleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agent")
	modelsPath := tempFile(t, agentDir, "models.yml", userModelYaml)
	integration := NewOmp(OmpOptions{Port: testPort, Models: ompTestModels, AgentDir: agentDir})
	if result := integration.Apply(); !result.OK || result.ID != Omp {
		t.Fatalf("apply: %+v", result)
	}
	if !contains(readFile(t, modelsPath), "prism:") {
		t.Fatal("prism leaf missing after apply")
	}
	if result := integration.Rollback(); !result.OK || result.ID != Omp {
		t.Fatalf("rollback: %+v", result)
	}
	if got := readFile(t, modelsPath); got != userModelYaml {
		t.Fatal("rollback did not restore user yaml")
	}
}
