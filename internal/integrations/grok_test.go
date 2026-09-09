package integrations

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGrokConfigPaths(t *testing.T) {
	if got := GrokConfigPath(Env{"GROK_HOME": "/custom/grok"}, "/home/u"); got != "/custom/grok/config.toml" {
		t.Errorf("GROK_HOME override: got %q", got)
	}
	if got := GrokHome(Env{}, "/home/u"); got != "/home/u/.grok" {
		t.Errorf("default grok home: got %q", got)
	}
	if got := GrokConfigPath(Env{"GROK_HOME": ""}, "/home/u"); got != "/home/u/.grok/config.toml" {
		t.Errorf("empty GROK_HOME: got %q", got)
	}
	if got := GrokHome(Env{"GROK_HOME": "/env/grok"}, "/home/u"); got != "/env/grok" {
		t.Errorf("grok home override: got %q", got)
	}
}

func TestGrokAliasAllocation(t *testing.T) {
	if got := GrokAlias("gpt-5.2-codex", map[string]bool{}); got != "prism-gpt-5-2-codex" {
		t.Errorf("sanitized alias: got %q", got)
	}
	if got := GrokAlias("a b/c", map[string]bool{"prism-a-b-c": true}); got != "prism-a-b-c-2" {
		t.Errorf("reserved alias: got %q", got)
	}
}

func TestGrokManagedBlock(t *testing.T) {
	block := GrokManagedBlock(testPort, grokTestModels)
	for _, want := range []string{
		"[model.prism-gpt-5-2-codex]",
		"[model.prism-gemini-3-pro]",
		"name = \"prism GPT-5.2 Codex\"",
		"name = \"prism Gemini 3 Pro\"",
		"base_url = \"http://127.0.0.1:8787/v1\"",
		"context_window = 400000",
		"api_key = \"prism-loopback\"",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("missing %q in:\n%s", want, block)
		}
	}
	if strings.Contains(block, `name = "GPT-5.2`) {
		t.Errorf("unprefixed model name in:\n%s", block)
	}
}

func TestGrokApplyInjectsFencedTables(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeWritten {
		t.Fatalf("apply: %+v", outcome)
	}
	content := readFile(t, configPath)
	if !strings.Contains(content, "top_setting = \"keep\"") || !strings.Contains(content, "[model.prism-gpt-5-2-codex]") {
		t.Fatalf("content:\n%s", content)
	}
	if strings.Index(content, "top_setting") >= strings.Index(content, GrokFence.Begin) {
		t.Fatal("fence not after user bytes")
	}
}

func TestGrokApplyIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels})
	afterFirst := readFile(t, configPath)
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("second apply: %+v", outcome)
	}
	if got := readFile(t, configPath); got != afterFirst {
		t.Fatal("second apply changed the file")
	}
}

func TestGrokIntegrationAppliesLiveModelsSource(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	live := []Model{{ID: "router/glm-5.3", Name: "router/glm-5.3"}}
	g := NewGrok(GrokOptions{ConfigPath: configPath, Port: testPort, Models: DefaultPrismModels, ModelsSource: func() []Model { return live }})
	if res := g.Apply(); !res.OK {
		t.Fatalf("apply: %+v", res)
	}
	content := readFile(t, configPath)
	if !strings.Contains(content, "[model.prism-router-glm-5-3]") || strings.Contains(content, "[model.prism-gpt-5-2-codex]") {
		t.Fatalf("live model missing or static model leaked:\n%s", content)
	}
}

func TestGrokReapplyRewritesOwnManagedBlock(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels})
	// Any content difference inside the fence — user edit or prism's own newer
	// output — is rewritten in place; the region is prism-owned.
	edited := replaceOne(readFile(t, configPath), "[model.prism-gpt-5-2-codex]", "[model.prism-gpt-5-2-codex]\nuser_field = 1")
	writeFileOrDie(t, configPath, edited)
	outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels})
	if outcome.Kind != OutcomeWritten {
		t.Fatalf("apply over edit: %+v", outcome)
	}
	got := readFile(t, configPath)
	if !strings.Contains(got, userToml) || strings.Contains(got, "user_field") {
		t.Fatalf("rewrite lost user bytes or kept foreign edit:\n%s", got)
	}
	if !strings.Contains(got, "[model.prism-gpt-5-2-codex]") {
		t.Fatalf("managed block missing after rewrite:\n%s", got)
	}
}

func TestGrokRollbackRemovesExactlyManagedBlock(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels})
	if outcome := StripGrokConfig(configPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("rollback: %+v", outcome)
	}
	if got := readFile(t, configPath); got != userToml {
		t.Fatalf("rollback did not restore user bytes:\n%q", got)
	}
}

func TestGrokApplyRollbackByteIdenticalWithoutFinalLF(t *testing.T) {
	seeds := []string{
		"# user configuration\ntop_setting = \"keep\"\n\n[profile.default]\nmodel = \"gpt-5.2\"",
		userToml,
		strings.ReplaceAll("# user configuration\ntop_setting = \"keep\"\n\n[profile.default]\nmodel = \"gpt-5.2\"", "\n", "\r\n"),
	}
	for _, seed := range seeds {
		dir := t.TempDir()
		configPath := tempFile(t, dir, "config.toml", seed)
		if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeWritten {
			t.Fatalf("apply %q: %+v", seed, outcome)
		}
		if outcome := StripGrokConfig(configPath); outcome.Kind != OutcomeWritten {
			t.Fatalf("rollback %q: %+v", seed, outcome)
		}
		if got := readFile(t, configPath); got != seed {
			t.Fatalf("apply then rollback not byte-identical for %q:\nwant: %q\ngot:  %q", seed, seed, got)
		}
	}
}

func TestGrokApplyMigratesLegacyTablesAlongsideCanonicalFence(t *testing.T) {
	dir := t.TempDir()
	legacy := grokLegacyPrismTables()
	configPath := tempFile(t, dir, "config.toml", userToml+"\n"+legacy)
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeWritten {
		t.Fatalf("seed apply: %+v", outcome)
	}
	canonical := readFile(t, configPath)

	merged := canonical + legacy
	writeFileOrDie(t, configPath, merged)
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeWritten {
		t.Fatalf("fence+legacy apply: %+v", outcome)
	}
	if got := readFile(t, configPath); got != canonical {
		t.Fatalf("fence+legacy apply did not migrate to canonical state:\nwant: %q\ngot:  %q", canonical, got)
	}
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("second apply: %+v", outcome)
	}
}

func TestGrokApplyMigratesLegacyTablesBeforeCanonicalFence(t *testing.T) {
	dir := t.TempDir()
	legacy := grokLegacyPrismTables()
	configPath := tempFile(t, dir, "config.toml", userToml+legacy)
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeWritten {
		t.Fatalf("seed apply: %+v", outcome)
	}
	canonical := readFile(t, configPath)

	merged := strings.Replace(canonical, GrokFence.Begin, legacy+GrokFence.Begin, 1)
	writeFileOrDie(t, configPath, merged)
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeWritten {
		t.Fatalf("legacy-before-fence apply: %+v", outcome)
	}
	got := readFile(t, configPath)
	if got != canonical {
		t.Fatalf("legacy-before-fence apply did not migrate to canonical state:\nwant: %q\ngot:  %q", canonical, got)
	}
	if strings.Count(got, GrokFence.Begin) != 1 || strings.Count(got, GrokFence.End) != 1 {
		t.Fatalf("fence corrupted by migration:\n%q", got)
	}
}

func TestGrokApplyMigratesLegacyPrismTableWithEmittedAlias(t *testing.T) {
	dir := t.TempDir()
	legacy := "[model.prism-gpt-5-2-codex]\nmodel = \"gpt-5.2-codex\"\nbase_url = \"http://127.0.0.1:18798/v1\"\napi_key = \"prism-loopback\"\n\n"
	configPath := tempFile(t, dir, "config.toml", userToml+"\n"+legacy)
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeWritten {
		t.Fatalf("legacy+emitted-alias apply: %+v", outcome)
	}
	got := readFile(t, configPath)
	unfenced := got
	if i := strings.Index(got, GrokFence.Begin); i >= 0 {
		unfenced = got[:i]
	}
	if contains(unfenced, "18798") || strings.Count(got, "prism-loopback") != 2 {
		t.Fatalf("legacy table with emitted alias survived or fence corrupted:\n%s", got)
	}
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("second apply after migration: %+v", outcome)
	}
}

func grokLegacyPrismTables() string {
	return strings.Join([]string{
		"[model.prism-router-glm-5-3]",
		"model = \"router/glm-5.3\"",
		"base_url = \"http://127.0.0.1:18798/v1\"",
		"api_backend = \"responses\"",
		"api_key = \"prism-loopback\"",
		"",
		"[model.prism-router-glm-5-3.extra_headers]",
		"x-prism-grok = \"1\"",
		"",
	}, "\n")
}

func TestGrokCollisionRefusal(t *testing.T) {
	dir := t.TempDir()
	userTable := "[model.prism-gpt-5-2-codex]\nmodel = \"gpt-5.2-codex\"\n"
	configPath := tempFile(t, dir, "config.toml", userToml+"\n"+userTable)
	outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels})
	if outcome.Kind != OutcomeRefused || !contains(outcome.Reason, "collides with a user-owned model table") {
		t.Fatalf("collision apply: %+v", outcome)
	}
	if !outcome.Retryable {
		t.Fatalf("collision refusal must be retryable: %+v", outcome)
	}
	if got := readFile(t, configPath); got != userToml+"\n"+userTable {
		t.Fatal("collision apply wrote bytes")
	}
	if contains(readFile(t, configPath), GrokFence.Begin) {
		t.Fatal("fence present after refusal")
	}
}

func TestGrokForcedApplyRenamesCollisionAndRollbackRestores(t *testing.T) {
	dir := t.TempDir()
	userTable := "[model.prism-gpt-5-2-codex]\nmodel = \"gpt-5.2-codex\"\n"
	seed := userToml + "\n" + userTable
	configPath := tempFile(t, dir, "config.toml", seed)
	g := NewGrok(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels})
	if result := g.Apply(); result.OK || !result.Retryable {
		t.Fatalf("plain apply must refuse retryable: %+v", result)
	}
	if result := g.ApplyForced(); !result.OK {
		t.Fatalf("forced apply: %+v", result)
	}
	got := readFile(t, configPath)
	if !contains(got, "[model.prism-gpt-5-2-codex-user]") || !contains(got, "[model.prism-gpt-5-2-codex]") {
		t.Fatalf("forced apply must keep the renamed user table and the managed table:\n%s", got)
	}
	if result := g.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if got := readFile(t, configPath); got != seed {
		t.Fatalf("rollback did not restore the user table verbatim:\nGOT:\n%q\nWANT:\n%q", got, seed)
	}
}

func TestGrokApplyMigratesLegacyPrismTables(t *testing.T) {
	dir := t.TempDir()
	legacy := strings.Join([]string{
		"[model.prism-router-glm-5-3]",
		"model = \"router/glm-5.3\"",
		"base_url = \"http://127.0.0.1:18798/v1\"",
		"api_backend = \"responses\"",
		"api_key = \"prism-loopback\"",
		"",
		"[model.prism-router-glm-5-3.extra_headers]",
		"x-prism-grok = \"1\"",
		"",
		"[model.user-owned]",
		"model = \"user-model\"",
		"api_key = \"sk-user\"",
		"",
	}, "\n")
	configPath := tempFile(t, dir, "config.toml", userToml+legacy)
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeWritten {
		t.Fatalf("legacy apply: %+v", outcome)
	}
	content := readFile(t, configPath)
	unfenced := content
	if i := strings.Index(content, GrokFence.Begin); i >= 0 {
		unfenced = content[:i]
	}
	if contains(unfenced, "prism-router-glm-5-3") || contains(unfenced, "prism-loopback") {
		t.Fatalf("legacy tables not removed:\n%s", content)
	}
	if !contains(content, "[model.user-owned]") || !contains(content, "sk-user") {
		t.Fatalf("user table clobbered:\n%s", content)
	}
	if !contains(content, GrokFence.Begin) || !contains(content, "[model.prism-gpt-5-2-codex]") {
		t.Fatalf("fenced block missing:\n%s", content)
	}
}

func TestGrokDoesNotTreatOwnFenceAsUserOwned(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeWritten {
		t.Fatalf("first apply: %+v", outcome)
	}
	if outcome := WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels}); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("reapply: %+v", outcome)
	}
}

func TestGrokIntegrationModuleStatus(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent")
	missing := NewGrok(GrokOptions{Port: testPort, Models: grokTestModels, Env: Env{}, Home: absent})
	status := missing.Status()
	if status.Installed || status.Managed || status.TargetPath == nil || *status.TargetPath != GrokConfigPath(Env{}, absent) || status.Detail != "not installed" {
		t.Fatalf("missing home status: %+v", status)
	}

	configPath := tempFile(t, dir, "grok-config.toml", userToml)
	installed := NewGrok(GrokOptions{Port: testPort, Models: grokTestModels, ConfigPath: configPath})
	status = installed.Status()
	if !status.Installed || status.Managed || status.Detail != "installed; no prism-managed bytes present" {
		t.Fatalf("installed-unmanaged status: %+v", status)
	}
}

func TestGrokIntegrationModuleEndpointAndDrift(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	integration := NewGrok(GrokOptions{Port: testPort, Models: grokTestModels, ConfigPath: configPath})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	status := integration.Status()
	if !status.Managed || status.Endpoint == nil || *status.Endpoint != "http://127.0.0.1:8787/v1" || status.Drift {
		t.Fatalf("managed status: %+v", status)
	}
	drifted := NewGrok(GrokOptions{Port: testPort + 1, Models: grokTestModels, ConfigPath: configPath})
	status = drifted.Status()
	if !status.Drift || !contains(status.Detail, "drifted") {
		t.Fatalf("drifted status: %+v", status)
	}
}

func TestGrokIntegrationModuleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	integration := NewGrok(GrokOptions{Port: testPort, Models: grokTestModels, ConfigPath: configPath})
	if result := integration.Apply(); !result.OK || result.ID != Grok {
		t.Fatalf("apply: %+v", result)
	}
	if !contains(readFile(t, configPath), "[model.prism-gemini-3-pro]") {
		t.Fatal("grok table missing after apply")
	}
	if result := integration.Rollback(); !result.OK || result.ID != Grok {
		t.Fatalf("rollback: %+v", result)
	}
	if got := readFile(t, configPath); got != userToml {
		t.Fatal("rollback did not restore user bytes")
	}
}
