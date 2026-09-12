package integrations

import (
	"strings"
	"testing"
)

var opencodeUserSeed = "{\n  \"theme\": \"dark\",\n  \"provider\": {\n    \"prism\": {\n      \"name\": \"Router\",\n      \"npm\": \"@ai-sdk/openai-compatible\",\n      \"options\": {\n        \"baseURL\": \"http://localhost:8080/v1\"\n      },\n      \"models\": {}\n    }\n  }\n}\n"

var opencode2UserSeed = "{\n  \"theme\": \"dark\",\n  \"providers\": {\n    \"prism\": {\n      \"name\": \"Router\",\n      \"package\": \"@opencode-ai/ai/providers/openai-compatible\",\n      \"settings\": {\n        \"baseURL\": \"http://localhost:8080/v1\"\n      },\n      \"models\": {}\n    }\n  }\n}\n"

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

func TestOpencode2ApplyWritesReasoningVariants(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencode2UserSeed)
	integration := NewOpencode2(Opencode2Options{Port: testPort, Models: opencodeEffortModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	got := readText(t, path)
	assertValidJSON(t, got)
	// v2 merges variant bodies onto the wire verbatim, so the effort key must
	// be in its snake_case wire spelling.
	for _, want := range []string{
		`"variants": [`,
		`"id": "high"`,
		`"reasoning_effort": "high"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("variant member %q missing:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"reasoningEffort"`) {
		t.Fatalf("v2 must not emit the AI SDK camelCase spelling:\n%s", got)
	}
	flashAt := strings.Index(got, `"router/glm-5.3-flash"`)
	if flashAt == -1 {
		t.Fatalf("flash model missing:\n%s", got)
	}
	// the undeclared model rides the daemon's full chat vocabulary too
	tail := got[flashAt:]
	for _, rung := range chatEffortVocabulary {
		if !strings.Contains(tail, `"reasoning_effort": "`+rung+`"`) {
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
	if !strings.Contains(got, `"prism"`) || !strings.Contains(got, `"theme"`) {
		t.Fatalf("user members lost:\n%s", got)
	}
	if strings.Contains(got, `"providers"`) {
		t.Fatalf("v1 apply must not touch the v2 providers key:\n%s", got)
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
	for _, tc := range []struct {
		name string
		seed string
	}{
		{"v1", opencodeUserSeed},
		{"v2", opencode2UserSeed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempDir(t)
			path := tempFile(t, dir, "opencode.json", tc.seed)
			var module Module = NewOpencode(OpencodeOptions{Port: testPort, Models: []Model{
				{ID: "m", Name: "M", ReasoningEfforts: []string{"high"}},
			}, ConfigPath: path})
			if tc.name == "v2" {
				module = NewOpencode2(Opencode2Options{Port: testPort, Models: []Model{
					{ID: "m", Name: "M", ReasoningEfforts: []string{"high"}},
				}, ConfigPath: path})
			}
			if result := module.Apply(); !result.OK {
				t.Fatalf("apply: %+v", result)
			}
			got := readText(t, path)
			assertValidJSON(t, got)
			if !strings.Contains(got, `"name": "M",`) {
				t.Fatalf("name line must carry the comma before reasoning:\n%s", got)
			}
		})
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

func TestOpencode2ApplyWritesPrismProvider(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencode2UserSeed)
	integration := NewOpencode2(Opencode2Options{Port: testPort, Models: opencodeWindowModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	got := readText(t, path)
	assertValidJSON(t, got)
	if !strings.Contains(got, `"gpt-5.2-codex": {`) {
		t.Fatalf("v2 model entry missing:\n%s", got)
	}
	if !strings.Contains(got, `"package": "@opencode-ai/ai/providers/openai-compatible"`) {
		t.Fatalf("package adapter missing:\n%s", got)
	}
	if !strings.Contains(got, `"baseURL": "http://127.0.0.1:8787/v1"`) {
		t.Fatalf("base url missing:\n%s", got)
	}
	if !strings.Contains(got, `"settings": {`) {
		t.Fatalf("settings block missing:\n%s", got)
	}
	if !strings.Contains(got, `"prism"`) || !strings.Contains(got, `"theme"`) {
		t.Fatalf("user members lost:\n%s", got)
	}
	if strings.Contains(got, `"npm"`) {
		t.Fatalf("v2 apply must not emit v1 npm wiring:\n%s", got)
	}
}

// The v2 leaf rides the same models renderer, so the comma bug regresses both
// generations; pin it here too.
func TestOpencode2ApplyWithContextWindowStaysValidJSON(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencode2UserSeed)
	integration := NewOpencode2(Opencode2Options{Port: testPort, Models: []Model{
		{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", ContextWindow: 400000},
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

func TestOpencode2RollbackRoundTrip(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencode2UserSeed)
	integration := NewOpencode2(Opencode2Options{Port: testPort, Models: ompTestModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	if result := integration.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if readText(t, path) != opencode2UserSeed {
		t.Fatalf("rollback did not restore user bytes:\nGOT:\n%s\nWANT:\n%s", readText(t, path), opencode2UserSeed)
	}
}

func TestOpencode2VerifierSemantics(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencode2UserSeed)
	probe := JSONBlockProbe(Opencode2, "providers", "opencode.json", "baseURL", ProviderBaseUrl(testPort),
		func(crash bool) WriteOutcome {
			outcome, err := ApplyConfigTransform(LocalIO{}, path, opencode2Transform(testPort, ompTestModels), crash)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode2 apply", err)}
			}
			return outcome
		},
		func() WriteOutcome {
			outcome, err := ApplyConfigTransform(LocalIO{}, path, opencode2RollbackTransform(), false)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode2 rollback", err)}
			}
			return outcome
		},
		func() bool { return RecoverOpencode2Config(LocalIO{}, path) },
	)
	for _, check := range VerifyIntegration(probe, path, opencode2UserSeed) {
		if !check.OK {
			t.Errorf("%s: %s", check.Semantic, check.Detail)
		}
	}
}

func TestOpencodeGenerationsShareFileWithoutCollision(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencode2UserSeed)
	v1 := NewOpencode(OpencodeOptions{Port: testPort, Models: ompTestModels, ConfigPath: path})
	v2 := NewOpencode2(Opencode2Options{Port: testPort, Models: ompTestModels, ConfigPath: path})
	if result := v1.Apply(); !result.OK {
		t.Fatalf("v1 apply: %+v", result)
	}
	if result := v2.Apply(); !result.OK {
		t.Fatalf("v2 apply: %+v", result)
	}
	got := readText(t, path)
	assertValidJSON(t, got)
	if strings.Count(got, `"prism": {`) != 2 {
		t.Fatalf("both generation blocks must coexist:\n%s", got)
	}
	if result := v2.Rollback(); !result.OK {
		t.Fatalf("v2 rollback: %+v", result)
	}
	got = readText(t, path)
	assertValidJSON(t, got)
	if strings.Count(got, `"prism": {`) != 1 {
		t.Fatalf("v2 rollback must leave exactly the v1 prism block:\n%s", got)
	}
	if !strings.Contains(got, `"npm"`) {
		t.Fatalf("v1 block disturbed by v2 rollback:\n%s", got)
	}
}
