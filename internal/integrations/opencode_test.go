package integrations

import (
	"encoding/json"
	"strings"
	"testing"
)

var opencodeUserSeed = "{\n  \"theme\": \"dark\",\n  \"provider\": {\n    \"acme\": {\n      \"name\": \"ACME\",\n      \"npm\": \"@ai-sdk/openai-compatible\",\n      \"options\": {\n        \"baseURL\": \"http://localhost:8080/v1\"\n      },\n      \"models\": {}\n    }\n  }\n}\n"

func TestOpencodePathResolution(t *testing.T) {
	if got := OpencodeConfigPath(Env{}, "/home/u"); got != "/home/u/.config/opencode/opencode.json" {
		t.Fatalf("default path: %s", got)
	}
	xdg := Env{"XDG_CONFIG_HOME": "/cfg"}
	if got := OpencodeConfigPath(xdg, "/home/u"); got != "/cfg/opencode/opencode.json" {
		t.Fatalf("xdg path: %s", got)
	}
}

var opencodeWindowModels = []Model{
	{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", ContextWindow: 400000},
	{ID: "gemini-3-pro", Name: "Gemini 3 Pro"},
}

var opencodeEffortModels = []Model{
	{ID: "router/glm-5.3", Name: "router/glm-5.3", ContextWindow: 256000, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"}, DefaultReasoningEffort: "high"},
	{ID: "router/glm-5.3-flash", Name: "router/glm-5.3-flash"},
}

func TestOpencodeApplyWritesReasoningVariants(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencodeUserSeed)
	integration := NewOpencode(OpencodeOptions{Port: testPort, Models: opencodeEffortModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	got := readText(t, path)
	assertValidJSON(t, got)
	for _, want := range []string{
		`"reasoning": true`,
		`"variants": {`,
		`"high": {`,
		`"reasoningEffort": "high"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("variant member %q missing:\n%s", want, got)
		}
	}
	// flash model declares no ladder: the daemon still steers the full chat
	// vocabulary for it, so the picker must offer every rung
	flashAt := strings.Index(got, `"router/glm-5.3-flash"`)
	if flashAt == -1 {
		t.Fatalf("flash model missing:\n%s", got)
	}
	tail := got[flashAt:]
	for _, rung := range chatEffortVocabulary {
		if !strings.Contains(tail, `"reasoningEffort": "`+rung+`"`) {
			t.Fatalf("undeclared model must carry rung %q:\n%s", rung, tail)
		}
	}
}

func TestOpencodeApplyWritesPrismProvider(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencodeUserSeed)
	integration := NewOpencode(OpencodeOptions{Port: testPort, Models: opencodeWindowModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	got := readText(t, path)
	assertValidJSON(t, got)
	if !strings.Contains(got, `"npm": "@ai-sdk/openai-compatible"`) {
		t.Fatalf("npm adapter missing:\n%s", got)
	}
	if !strings.Contains(got, `"baseURL": "http://127.0.0.1:8787/v1"`) {
		t.Fatalf("base url missing:\n%s", got)
	}
	if !strings.Contains(got, `"gpt-5.2-codex": {`) {
		t.Fatalf("model entry missing:\n%s", got)
	}
	if !strings.Contains(got, `"acme"`) || !strings.Contains(got, `"theme"`) {
		t.Fatalf("user members lost:\n%s", got)
	}
}

// A model with a known context window renders a limit pair; the comma between
// name and limit is load-bearing for the file to stay valid JSON.
func TestOpencodeApplyWithContextWindowStaysValidJSON(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencodeUserSeed)
	integration := NewOpencode(OpencodeOptions{Port: testPort, Models: []Model{
		{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", ContextWindow: 400000},
		{ID: "gemini-3-pro", Name: "Gemini 3 Pro"},
	}, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	got := readText(t, path)
	assertValidJSON(t, got)
	if !strings.Contains(got, `"name": "GPT-5.2 Codex",`) {
		t.Fatalf("name line must carry the comma before limit:\n%s", got)
	}
}

func TestOpencodeApplyWithEffortsButNoWindowStaysValidJSON(t *testing.T) {
	// a model with efforts but no known window must still render valid JSON:
	// the name line carries the comma that closes onto the reasoning block
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencodeUserSeed)
	module := NewOpencode(OpencodeOptions{Port: testPort, Models: []Model{
		{ID: "m", Name: "M", ReasoningEfforts: []string{"high"}},
	}, ConfigPath: path})
	if result := module.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	got := readText(t, path)
	assertValidJSON(t, got)
	if !strings.Contains(got, `"name": "M",`) {
		t.Fatalf("name line must carry the comma before reasoning:\n%s", got)
	}
}

func TestOpencodeRollbackRoundTrip(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencodeUserSeed)
	integration := NewOpencode(OpencodeOptions{Port: testPort, Models: ompTestModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	if result := integration.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if readText(t, path) != opencodeUserSeed {
		t.Fatalf("rollback did not restore user bytes:\nGOT:\n%s\nWANT:\n%s", readText(t, path), opencodeUserSeed)
	}
}

func TestOpencodeStatusLifecycle(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencodeUserSeed)
	integration := NewOpencode(OpencodeOptions{Port: testPort, Models: ompTestModels, ConfigPath: path})
	if status := integration.Status(); !status.Installed || status.Managed {
		t.Fatalf("fresh status: %+v", status)
	}
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	status := integration.Status()
	if !status.Managed || status.Endpoint == nil || *status.Endpoint != "http://127.0.0.1:8787/v1" {
		t.Fatalf("managed status: %+v", status)
	}
}

func TestOpencodeVerifierSemantics(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencodeUserSeed)
	probe := JSONBlockProbe(Opencode, "provider", "opencode.json", "baseURL", ProviderBaseUrl(testPort),
		func(crash bool) WriteOutcome {
			outcome, err := ApplyConfigTransform(LocalIO{}, path, opencodeTransform(testPort, ompTestModels), crash)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode apply", err)}
			}
			return outcome
		},
		func() WriteOutcome {
			outcome, err := ApplyConfigTransform(LocalIO{}, path, opencodeRollbackTransform(), false)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode rollback", err)}
			}
			return outcome
		},
		func() bool { return RecoverOpencodeConfig(LocalIO{}, path) },
	)
	for _, check := range VerifyIntegration(probe, path, opencodeUserSeed) {
		if !check.OK {
			t.Errorf("%s: %s", check.Semantic, check.Detail)
		}
	}
}

func TestOpencodeApplyWritesLoadableV1Provider(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencodeUserSeed)
	integration := NewOpencode(OpencodeOptions{Port: testPort, Models: opencodeEffortModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	var doc struct {
		Providers map[string]any `json:"providers"`
		Provider  map[string]struct {
			Npm     string            `json:"npm"`
			Options map[string]string `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(readText(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	prism, ok := doc.Provider["prism"]
	if !ok || prism.Npm != "@ai-sdk/openai-compatible" || prism.Options["baseURL"] != ProviderBaseUrl(testPort) {
		t.Fatalf("provider.prism not loadable: %+v", doc.Provider)
	}
	if _, ok := doc.Provider["acme"]; !ok {
		t.Fatalf("user provider lost: %+v", doc.Provider)
	}
}

func TestOutputBudgetLeavesInputRoomInSmallWindows(t *testing.T) {
	for window, want := range map[int]int{8000: 4000, 64000: 32000, 400000: 32000} {
		if got := maxTokensFor(window); got != want {
			t.Errorf("maxTokensFor(%d) = %d, want %d", window, got, want)
		}
	}
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencodeUserSeed)
	integration := NewOpencode(OpencodeOptions{Port: testPort, Models: []Model{{ID: "r/small", Name: "r/small", ContextWindow: 8000}}, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	if got := readText(t, path); !strings.Contains(got, `"output": 4000`) || strings.Contains(got, `"output": 8000`) {
		t.Fatalf("output cap must leave input room:\n%s", got)
	}
}
