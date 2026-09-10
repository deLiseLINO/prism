package integrations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudePathResolution(t *testing.T) {
	env := Env{}
	if got := ClaudeSettingsPath(env, "/home/u"); got != "/home/u/.claude/settings.json" {
		t.Fatalf("default settings path: %s", got)
	}
	override := Env{"CLAUDE_CONFIG_DIR": "/cfg"}
	if got := ClaudeSettingsPath(override, "/home/u"); got != "/cfg/settings.json" {
		t.Fatalf("override settings path: %s", got)
	}
}

func TestClaudeAlias(t *testing.T) {
	if got := ClaudeAlias("codex/gpt-5.2-codex"); got != "claude-prism-codex--gpt-5.2-codex" {
		t.Fatalf("alias: %s", got)
	}
	if got := ClaudeAlias("bare-model"); got != "claude-prism-codex--bare-model" {
		t.Fatalf("bare alias: %s", got)
	}
}

func TestClaudeApplyWritesEnvSlots(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "settings.json", "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash\"\n    ]\n  }\n}\n")
	integration := NewClaude(ClaudeOptions{Port: testPort, Models: []Model{
		{ID: "codex/gpt-5.2-codex", Name: "GPT-5.2 Codex"},
		{ID: "ag/gemini-3-pro", Name: "Gemini 3 Pro"},
	}, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	got := readText(t, path)
	if !strings.Contains(got, `"ANTHROPIC_BASE_URL": "http://127.0.0.1:8787"`) {
		t.Fatalf("base url missing:\n%s", got)
	}
	if !strings.Contains(got, `"ANTHROPIC_MODEL": "claude-prism-ag--gemini-3-pro"`) {
		t.Fatalf("default model alias missing (deterministic smallest id):\n%s", got)
	}
	if !strings.Contains(got, `"permissions"`) {
		t.Fatalf("user member lost:\n%s", got)
	}
	assertValidJSON(t, got)

	again := NewClaude(ClaudeOptions{Port: testPort, Models: []Model{
		{ID: "codex/gpt-5.2-codex", Name: "GPT-5.2 Codex"},
		{ID: "ag/gemini-3-pro", Name: "Gemini 3 Pro"},
	}, ConfigPath: path})
	if result := again.Apply(); !result.OK {
		t.Fatalf("second apply: %+v", result)
	}
	if readText(t, path) != got {
		t.Fatalf("second apply changed bytes")
	}
}

func TestClaudeRollbackRoundTrip(t *testing.T) {
	dir := tempDir(t)
	seed := "{\n  \"env\": {\n    \"CUSTOM_TOOL\": \"keep\"\n  }\n}\n"
	path := tempFile(t, dir, "settings.json", seed)
	integration := NewClaude(ClaudeOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	if result := integration.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if readText(t, path) != seed {
		t.Fatalf("rollback did not restore user bytes:\nGOT:\n%s\nWANT:\n%s", readText(t, path), seed)
	}
}

func TestClaudeApplyRefusesUserOwnedEnv(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "settings.json", "{\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"https://my-gateway.example\"\n  }\n}\n")
	integration := NewClaude(ClaudeOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: path})
	result := integration.Apply()
	if result.OK {
		t.Fatalf("user-owned env apply unexpectedly succeeded")
	}
	if !strings.Contains(result.Reason, "ANTHROPIC_BASE_URL") {
		t.Fatalf("refusal does not name the key: %s", result.Reason)
	}
	if !result.Retryable {
		t.Fatalf("user-owned refusal must be retryable: %+v", result)
	}
}

func TestClaudeForcedApplyDisplacesEnvAndRollbackRestores(t *testing.T) {
	dir := tempDir(t)
	seed := "{\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"https://my-gateway.example\"\n  }\n}\n"
	path := tempFile(t, dir, "settings.json", seed)
	integration := NewClaude(ClaudeOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: path})
	if result := integration.ApplyForced(); !result.OK {
		t.Fatalf("forced apply: %+v", result)
	}
	if got := readText(t, path); !strings.Contains(got, `"ANTHROPIC_BASE_URL": "http://127.0.0.1:8787"`) {
		t.Fatalf("forced apply did not take over the env slot:\n%s", got)
	}
	if result := integration.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if readText(t, path) != seed {
		t.Fatalf("rollback did not restore the displaced env value:\nGOT:\n%s\nWANT:\n%s", readText(t, path), seed)
	}
}

func TestClaudeStatusLifecycle(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "settings.json", "{\n}\n")
	integration := NewClaude(ClaudeOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: path})
	status := integration.Status()
	if !status.Installed || status.Managed {
		t.Fatalf("fresh status: %+v", status)
	}
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	status = integration.Status()
	if !status.Managed || status.Endpoint == nil || *status.Endpoint != "http://127.0.0.1:8787" {
		t.Fatalf("managed status: %+v", status)
	}
	if status.Drift {
		t.Fatalf("unexpected drift: %+v", status)
	}
}

func TestClaudeStatusDrift(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "settings.json", "{\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:9999\",\n    \"ANTHROPIC_AUTH_TOKEN\": \"prism-loopback\"\n  }\n}\n")
	integration := NewClaude(ClaudeOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: path})
	status := integration.Status()
	if !status.Managed || !status.Drift {
		t.Fatalf("expected drift: %+v", status)
	}
}

func TestClaudeVerifierSemantics(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "settings.json", "{\n  \"env\": {\n    \"CUSTOM_TOOL\": \"keep\"\n  }\n}\n")
	probe := JSONScalarKeysProbe(Claude, "env", "settings.json", "ANTHROPIC_BASE_URL", ClaudeBaseURL(testPort),
		func(crash bool) WriteOutcome {
			outcome, err := ApplyConfigTransform(path, claudeTransform(testPort, DefaultPrismModels, false), crash)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("claude apply", err)}
			}
			return outcome
		},
		func() WriteOutcome {
			outcome, err := ApplyConfigTransform(path, claudeRollbackTransform(), false)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("claude rollback", err)}
			}
			return outcome
		},
		func() bool { return RecoverClaudeConfig(path) },
	)
	for _, check := range VerifyIntegration(probe, path, "{\n  \"env\": {\n    \"CUSTOM_TOOL\": \"keep\"\n  }\n}\n") {
		if !check.OK {
			t.Errorf("%s: %s", check.Semantic, check.Detail)
		}
	}
}
func TestClaudeApplySeedsGatewayCache(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "settings.json", "{\n}\n")
	now := int64(1788582266205)
	integration := NewClaude(ClaudeOptions{Port: testPort, Models: []Model{
		{ID: "codex/gpt-5.2-codex", Name: "GPT-5.2 Codex"},
	}, ConfigPath: path, NowMS: func() int64 { return now }})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	cachePath := filepath.Join(dir, "cache", "gateway-models.json")
	cache := readText(t, cachePath)
	var parsed struct {
		BaseURL   string `json:"baseUrl"`
		FetchedAt int64  `json:"fetchedAt"`
		Models    []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(cache), &parsed); err != nil {
		t.Fatalf("cache is not JSON: %v\n%s", err, cache)
	}
	if parsed.BaseURL != "http://127.0.0.1:8787" || parsed.FetchedAt != now {
		t.Fatalf("cache header = %+v", parsed)
	}
	if len(parsed.Models) != 1 || parsed.Models[0].ID != "claude-prism-codex--gpt-5.2-codex" || parsed.Models[0].DisplayName != "GPT-5.2 Codex" {
		t.Fatalf("cache models = %+v", parsed.Models)
	}

	// A cache already pointing at this endpoint is left untouched: the CLI
	// refreshes it itself and re-applies must not reset it.
	if err := os.WriteFile(cachePath, []byte(strings.Replace(cache, "GPT-5.2 Codex", "refreshed", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if result := integration.Apply(); !result.OK {
		t.Fatalf("second apply: %+v", result)
	}
	if !strings.Contains(readText(t, cachePath), "refreshed") {
		t.Fatal("second apply clobbered the CLI-maintained cache")
	}

	if result := integration.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatal("rollback left the prism-seeded cache behind")
	}
}

func TestClaudeApplyRefusesForeignGatewayCache(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "settings.json", "{}\n")
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := `{"baseUrl":"https://other-gateway.example","fetchedAt":1,"models":[]}`
	if err := os.WriteFile(filepath.Join(cacheDir, "gateway-models.json"), []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	integration := NewClaude(ClaudeOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: path, NowMS: func() int64 { return 2 }})
	result := integration.Apply()
	if result.OK {
		t.Fatal("apply over a foreign gateway cache should refuse")
	}
	if !strings.Contains(result.Reason, "other-gateway.example") {
		t.Fatalf("refusal must name the foreign endpoint: %+v", result)
	}
	if readText(t, path) != "{}\n" {
		t.Fatal("refused apply must not touch settings.json")
	}
	if readText(t, filepath.Join(cacheDir, "gateway-models.json")) != foreign {
		t.Fatal("refused apply must not touch the foreign cache")
	}
}

func TestClaudeForcedApplyDisplacesForeignCacheAndRollbackRestores(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "settings.json", "{\n  \"env\": {\n    \"CUSTOM_TOOL\": \"keep\"\n  }\n}\n")
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := `{"baseUrl":"https://other-gateway.example","fetchedAt":1,"models":[]}`
	cachePath := filepath.Join(cacheDir, "gateway-models.json")
	if err := os.WriteFile(cachePath, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	integration := NewClaude(ClaudeOptions{Port: testPort, Models: DefaultPrismModels, ConfigPath: path, NowMS: func() int64 { return 2 }})
	result := integration.Apply()
	if result.OK || !result.Retryable {
		t.Fatalf("plain apply over a foreign cache must refuse retryable: %+v", result)
	}
	if result := integration.ApplyForced(); !result.OK {
		t.Fatalf("forced apply: %+v", result)
	}
	if got := readText(t, cachePath); !strings.Contains(got, `"baseUrl":"http://127.0.0.1:8787"`) {
		t.Fatalf("forced apply did not take over the cache:\n%s", got)
	}
	if result := integration.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if readText(t, cachePath) != foreign {
		t.Fatalf("rollback did not restore the foreign cache:\nGOT:\n%s\nWANT:\n%s", readText(t, cachePath), foreign)
	}
}
