package integrations

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexConfigPaths(t *testing.T) {
	if got := CodexConfigPath(Env{"CODEX_HOME": "/custom/codex"}, "/home/u"); got != "/custom/codex/config.toml" {
		t.Errorf("CODEX_HOME override: got %q", got)
	}
	if got := CodexConfigPath(Env{"CODEX_HOME": ""}, "/home/u"); got != "/home/u/.codex/config.toml" {
		t.Errorf("empty CODEX_HOME: got %q", got)
	}
	if got := CodexHome(Env{}, "/home/u"); got != "/home/u/.codex" {
		t.Errorf("default codex home: got %q", got)
	}
	if got := CodexConfigPath(Env{}, "/home/u"); got != "/home/u/.codex/config.toml" {
		t.Errorf("default codex config: got %q", got)
	}
	if got := CodexConfigPath(Env{"CODEX_HOME": "   "}, "/home/u"); got != "/home/u/.codex/config.toml" {
		t.Errorf("blank CODEX_HOME: got %q", got)
	}
	if got := CodexHome(Env{"CODEX_HOME": "/env/codex"}, "/home/u"); got != "/env/codex" {
		t.Errorf("codex home override: got %q", got)
	}
}

func TestCodexApplyInjectsFencedProviderTable(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome.Kind != OutcomeWritten {
		t.Fatalf("apply: %+v", outcome)
	}
	content := readFile(t, configPath)
	for _, want := range []string{CodexFence.Begin, CodexFence.End, "base_url = \"http://127.0.0.1:8787/v1\"", "top_setting = \"keep\"", "[profile.default]"} {
		if !contains(content, want) {
			t.Errorf("missing %q in:\n%s", want, content)
		}
	}
}

func TestCodexApplyIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	afterFirst := readFile(t, configPath)
	if outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort}); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("second apply: %+v", outcome)
	}
	if got := readFile(t, configPath); got != afterFirst {
		t.Fatal("second apply changed the file")
	}
}

func TestCodexReapplyRewritesOwnManagedBlock(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	edited := replaceOne(readFile(t, configPath), CodexFence.Begin, CodexFence.Begin+"\nuser_edit = true")
	writeFileOrDie(t, configPath, edited)
	outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome.Kind != OutcomeWritten {
		t.Fatalf("apply over edit: %+v", outcome)
	}
	got := readFile(t, configPath)
	if strings.Contains(got, "user_edit") {
		t.Fatalf("foreign edit kept after rewrite:\n%s", got)
	}
	for _, want := range []string{`top_setting = "keep"`, "[profile.default]", `model = "gpt-5.2"`} {
		if !contains(got, want) {
			t.Fatalf("user line %q lost after rewrite:\n%s", want, got)
		}
	}
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("rollback after fence rewrite: %+v", outcome)
	}
	if got := readFile(t, configPath); got != userToml {
		t.Fatalf("rollback did not restore user bytes verbatim:\n%q", got)
	}
}

func TestCodexRollbackRemovesExactlyManagedBlock(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("rollback: %+v", outcome)
	}
	if got := readFile(t, configPath); got != userToml {
		t.Fatalf("rollback did not restore user bytes:\n%q", got)
	}
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("second rollback: %+v", outcome)
	}
}

func TestCodexApplyWritesModelCatalog(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	models := []Model{{ID: "router/glm-5.3-flash", Name: "router/glm-5.3-flash", ContextWindow: 128000}}
	outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort, Models: models})
	if outcome.Kind != OutcomeWritten {
		t.Fatalf("apply: %+v", outcome)
	}
	content := readFile(t, configPath)
	for _, want := range []string{prismCatalogMarker, "model_catalog_json = " + tomlString(CodexCatalogPath(configPath))} {
		if !contains(content, want) {
			t.Errorf("missing %q in:\n%s", want, content)
		}
	}
	var catalog codexCatalog
	if err := json.Unmarshal([]byte(readFile(t, CodexCatalogPath(configPath))), &catalog); err != nil {
		t.Fatalf("catalog not valid JSON: %v", err)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("catalog models = %d", len(catalog.Models))
	}
	entry := catalog.Models[0]
	if entry.Slug != "router/glm-5.3-flash" || entry.ShellType != "shell_command" || entry.ContextWindow != 128000 {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.MultiAgentVersion != nil || len(entry.ExperimentalSupportedTools) != 0 {
		t.Fatalf("entry advertises tools the chat wire cannot carry: %+v", entry)
	}
	if entry.PrismCapabilityProvenance.Provider != "router" || entry.PrismCapabilityProvenance.ModelID != "glm-5.3-flash" {
		t.Fatalf("provenance = %+v", entry.PrismCapabilityProvenance)
	}
	if outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort, Models: models}); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("second apply: %+v", outcome)
	}
}

func TestCodexApplyRefusesForeignCatalogKey(t *testing.T) {
	dir := t.TempDir()
	foreign := "model = \"gpt-5.2\"\nmodel_catalog_json = \"/other/catalog.json\"\n\n[features]\nfast_mode = false\n"
	configPath := tempFile(t, dir, "config.toml", foreign)
	outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort, Models: DefaultPrismModels})
	if outcome.Kind != OutcomeRefused || !contains(outcome.Reason, "model catalog") {
		t.Fatalf("apply: %+v", outcome)
	}
	if got := readFile(t, configPath); got != foreign {
		t.Fatal("refused apply touched the file")
	}
	if localFileExists(CodexCatalogPath(configPath)) {
		t.Fatal("refused apply left an orphan catalog file")
	}
}

func TestCodexRollbackRemovesCatalog(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort, Models: DefaultPrismModels})
	if !localFileExists(CodexCatalogPath(configPath)) {
		t.Fatal("catalog file missing after apply")
	}
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("rollback: %+v", outcome)
	}
	if contains(readFile(t, configPath), "model_catalog_json") {
		t.Fatal("rollback left the catalog key")
	}
	if localFileExists(CodexCatalogPath(configPath)) {
		t.Fatal("rollback left the catalog file")
	}
	if got := readFile(t, configPath); got != userToml {
		t.Fatalf("rollback did not restore user bytes:\n%q", got)
	}
}

func TestCodexCrashSafety(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	crashed := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort, CrashBeforeRename: true})
	if crashed.Kind != OutcomeCrashed {
		t.Fatalf("crashing apply: %+v", crashed)
	}
	if got := readFile(t, configPath); got != userToml {
		t.Fatal("target changed before the rename")
	}
	if !localFileExists(StagedPath(configPath)) {
		t.Fatal("staged temp missing after crash")
	}
	if !RecoverCodexConfig(LocalIO{}, configPath) {
		t.Fatal("recovery found no staged temp")
	}
	if localFileExists(StagedPath(configPath)) {
		t.Fatal("staged temp survived recovery")
	}
	if got := readFile(t, configPath); got != userToml {
		t.Fatal("recovery altered the last complete state")
	}
	if outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort}); outcome.Kind != OutcomeWritten {
		t.Fatalf("apply after recovery: %+v", outcome)
	}
}

func TestCodexDamagedFenceRefusesByName(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	damaged := userToml + CodexFence.Begin + "\n[model_providers.stale]\n"
	writeFileOrDie(t, configPath, damaged)
	if outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort}); outcome.Kind != OutcomeRefused || !contains(outcome.Reason, "damaged") {
		t.Fatalf("apply on damaged fence: %+v", outcome)
	}
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeRefused || !contains(outcome.Reason, "damaged") {
		t.Fatalf("rollback on damaged fence: %+v", outcome)
	}
	if got := readFile(t, configPath); got != damaged {
		t.Fatal("damaged file was touched")
	}
}

var prismRoutedToml = "# user configuration\n" +
	"model = \"router/glm-5.3-flash\"\n" +
	"# Managed by prism: Codex routes through the local proxy.\n" +
	"openai_base_url = \"http://127.0.0.1:8080/v1\"\n" +
	"\n" +
	"[features]\n" +
	"fast_mode = false\n"

var prismMarker = "# Managed by prism: Codex routes through the local proxy."

func TestCodexApplyTakesOverPrismRouting(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", prismRoutedToml)
	outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome.Kind != OutcomeWritten {
		t.Fatalf("takeover apply: %+v", outcome)
	}
	got := readFile(t, configPath)
	for _, want := range []string{
		prismRoutingMarker,
		`openai_base_url = "http://127.0.0.1:8787/v1"`,
		`model = "router/glm-5.3-flash"`,
		"[features]",
		"# displaced: " + prismMarker,
		`# displaced: openai_base_url = "http://127.0.0.1:8080/v1"`,
		CodexFence.Begin,
	} {
		if !contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if contains(got, prismMarker+"\n"+`openai_base_url = "http://127.0.0.1:8080/v1"`) {
		t.Errorf("displaced pair still routing:\n%s", got)
	}
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("rollback after takeover: %+v", outcome)
	}
	if got := readFile(t, configPath); got != prismRoutedToml {
		t.Fatalf("rollback did not restore the displaced prism pair verbatim:\n%q", got)
	}
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("second rollback: %+v", outcome)
	}
}

func TestCodexApplyRefusesUserOwnedRootBaseUrl(t *testing.T) {
	for name, seed := range map[string]string{
		"bare":            "# user configuration\nopenai_base_url = \"http://127.0.0.1:9999/v1\"\n\n[features]\nfast_mode = false\n",
		"foreign comment": "# user configuration\n# my own proxy\nopenai_base_url = \"http://127.0.0.1:9999/v1\"\n\n[features]\nfast_mode = false\n",
	} {
		dir := t.TempDir()
		configPath := tempFile(t, dir, "config.toml", seed)
		outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
		if outcome.Kind != OutcomeRefused || !contains(outcome.Reason, "user-owned") {
			t.Fatalf("%s: expected user-owned refusal, got %+v", name, outcome)
		}
		if got := readFile(t, configPath); got != seed {
			t.Fatalf("%s: refused apply touched the file:\n%s", name, got)
		}
	}
}

func TestCodexApplyRefusesExternalModelProvider(t *testing.T) {
	dir := t.TempDir()
	seed := "# user configuration\nmodel_provider = \"cursorapi\"\n\n[features]\nfast_mode = false\n"
	configPath := tempFile(t, dir, "config.toml", seed)
	outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome.Kind != OutcomeRefused || !contains(outcome.Reason, "cursorapi") {
		t.Fatalf("external provider apply: %+v", outcome)
	}
	if !outcome.Retryable {
		t.Fatalf("external provider refusal must be retryable: %+v", outcome)
	}
	if got := readFile(t, configPath); got != seed {
		t.Fatalf("refused apply touched the file:\n%s", got)
	}
}

func TestCodexForcedApplySwitchesExternalProviderAndRollbackRestores(t *testing.T) {
	dir := t.TempDir()
	seed := "# user configuration\nmodel_provider = \"cursorapi\"\n\n[features]\nfast_mode = false\n"
	configPath := tempFile(t, dir, "config.toml", seed)
	c := NewCodex(CodexOptions{ConfigPath: configPath, Port: testPort})
	if result := c.Apply(); result.OK || !result.Retryable {
		t.Fatalf("plain apply must refuse retryable: %+v", result)
	}
	if result := c.ApplyForced(); !result.OK {
		t.Fatalf("forced apply: %+v", result)
	}
	if got := readFile(t, configPath); !contains(got, "model_provider = \"prism\"") || contains(got, "cursorapi\"") && !contains(got, "# displaced:") {
		t.Fatalf("forced apply must switch the provider and journal the displaced line:\n%s", got)
	}
	if result := c.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if got := readFile(t, configPath); got != seed {
		t.Fatalf("rollback did not restore the provider selection verbatim:\n%q", got)
	}
}

func TestCodexForcedApplyDisplacesUserOwnedRoutingAndCatalog(t *testing.T) {
	dir := t.TempDir()
	seed := "# user configuration\nopenai_base_url = \"http://127.0.0.1:9999/v1\"\nmodel_catalog_json = \"/other/catalog.json\"\n\n[features]\nfast_mode = false\n"
	configPath := tempFile(t, dir, "config.toml", seed)
	c := NewCodex(CodexOptions{ConfigPath: configPath, Port: testPort, Models: DefaultPrismModels})
	if result := c.ApplyForced(); !result.OK {
		t.Fatalf("forced apply: %+v", result)
	}
	got := readFile(t, configPath)
	if !contains(got, `openai_base_url = "http://127.0.0.1:8787/v1"`) || !contains(got, "# displaced:") {
		t.Fatalf("forced apply must displace the user-owned pairs:\n%s", got)
	}
	if result := c.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if got := readFile(t, configPath); got != seed {
		t.Fatalf("rollback did not restore user pairs verbatim:\n%q", got)
	}
}

func TestCodexApplyRefusesMultipleRootBaseUrls(t *testing.T) {
	dir := t.TempDir()
	seed := "openai_base_url = \"http://127.0.0.1:1/v1\"\nopenai_base_url = \"http://127.0.0.1:2/v1\"\n\n[features]\n"
	configPath := tempFile(t, dir, "config.toml", seed)
	outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome.Kind != OutcomeRefused || !contains(outcome.Reason, "multiple root openai_base_url") {
		t.Fatalf("multiple keys apply: %+v", outcome)
	}
	if got := readFile(t, configPath); got != seed {
		t.Fatal("refused apply touched the file")
	}
}

func TestCodexApplyProviderOnlyWhenModelProviderPrism(t *testing.T) {
	dir := t.TempDir()
	seed := "# user configuration\nmodel_provider = \"prism\"\n\n[features]\nfast_mode = false\n"
	configPath := tempFile(t, dir, "config.toml", seed)
	outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome.Kind != OutcomeWritten {
		t.Fatalf("provider-only apply: %+v", outcome)
	}
	got := readFile(t, configPath)
	if contains(got, "openai_base_url") || contains(got, prismRoutingMarker) || contains(got, routingJournalHeader) {
		t.Fatalf("provider-only apply wrote routing bytes:\n%s", got)
	}
	if !contains(got, CodexFence.Begin) {
		t.Fatalf("provider table missing:\n%s", got)
	}
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("provider-only rollback: %+v", outcome)
	}
	if got := readFile(t, configPath); got != seed {
		t.Fatalf("provider-only rollback did not restore the seed:\n%q", got)
	}
}

func TestCodexApplyProviderOnlyRemovesStaleRoutingPair(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	withProviderSelection := "model_provider = \"prism\"\n" + readFile(t, configPath)
	writeFileOrDie(t, configPath, withProviderSelection)
	outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome.Kind != OutcomeWritten {
		t.Fatalf("re-apply under provider selection: %+v", outcome)
	}
	got := readFile(t, configPath)
	if contains(got, "openai_base_url") || contains(got, prismRoutingMarker) || contains(got, routingJournalHeader) {
		t.Fatalf("stale routing pair survived provider-only re-apply:\n%s", got)
	}
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("rollback: %+v", outcome)
	}
	if got := readFile(t, configPath); got != "model_provider = \"prism\"\n"+userToml {
		t.Fatalf("rollback did not restore user bytes with the provider selection:\n%q", got)
	}
}

func TestCodexReapplyAfterTakeoverIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", prismRoutedToml)
	WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	afterFirst := readFile(t, configPath)
	if outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort}); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("second apply after takeover: %+v", outcome)
	}
	if got := readFile(t, configPath); got != afterFirst {
		t.Fatal("second apply after takeover changed the file")
	}
}

func TestCodexRoutingOwnershipSurvivesMarkerStripping(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", prismRoutedToml)
	WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	stripped := strings.Replace(readFile(t, configPath), prismRoutingMarker+"\n", "", 1)
	writeFileOrDie(t, configPath, stripped)
	outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome.Kind != OutcomeWritten {
		t.Fatalf("re-apply over stripped marker: %+v", outcome)
	}
	got := readFile(t, configPath)
	if !contains(got, prismRoutingMarker) {
		t.Fatalf("marker not restored by ownership-by-value:\n%s", got)
	}
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("rollback after stripping: %+v", outcome)
	}
	if got := readFile(t, configPath); got != prismRoutedToml {
		t.Fatalf("rollback did not restore the displaced pair after stripping:\n%q", got)
	}
}

func TestCodexApplyRoutingPortDriftRewritesPair(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort + 1}); outcome.Kind != OutcomeWritten {
		t.Fatalf("drift apply: %+v", outcome)
	}
	got := readFile(t, configPath)
	if contains(got, fmt.Sprintf("127.0.0.1:%d", testPort)) {
		t.Fatalf("stale endpoint survived drift:\n%s", got)
	}
	for _, want := range []string{
		`openai_base_url = "http://127.0.0.1:8788/v1"`,
		`base_url = "http://127.0.0.1:8788/v1"`,
		"# written: http://127.0.0.1:8788/v1",
	} {
		if !contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if outcome := StripCodexConfig(LocalIO{}, configPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("rollback after drift: %+v", outcome)
	}
	if got := readFile(t, configPath); got != userToml {
		t.Fatalf("rollback after drift did not restore user bytes:\n%q", got)
	}
}

func TestCodexIntegrationModuleStatus(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "missing-home")
	integration := NewCodex(CodexOptions{Port: testPort, Env: Env{}, Home: home})
	status := integration.Status()
	if status.Installed || status.Managed || status.TargetPath == nil || *status.TargetPath != CodexConfigPath(Env{}, home) || status.Detail != "not installed" {
		t.Fatalf("missing home status: %+v", status)
	}

	configPath := tempFile(t, dir, "config.toml", userToml)
	installed := NewCodex(CodexOptions{Port: testPort, ConfigPath: configPath})
	status = installed.Status()
	if !status.Installed || status.Managed || status.Detail != "installed; no prism-managed bytes present" {
		t.Fatalf("installed-unmanaged status: %+v", status)
	}
}

func TestCodexIntegrationModuleEndpointAndDrift(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	integration := NewCodex(CodexOptions{Port: testPort, ConfigPath: configPath})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	status := integration.Status()
	if !status.Managed || status.Endpoint == nil || *status.Endpoint != "http://127.0.0.1:8787/v1" || status.Drift || status.Detail != "managed by prism" {
		t.Fatalf("managed status: %+v", status)
	}
	drifted := NewCodex(CodexOptions{Port: testPort + 1, ConfigPath: configPath})
	status = drifted.Status()
	if !status.Managed || !status.Drift || !contains(status.Detail, "drifted") {
		t.Fatalf("drifted status: %+v", status)
	}
}

func TestCodexIntegrationModuleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	integration := NewCodex(CodexOptions{Port: testPort, ConfigPath: configPath})
	if result := integration.Apply(); !result.OK || result.ID != Codex {
		t.Fatalf("apply: %+v", result)
	}
	if !contains(readFile(t, configPath), CodexFence.Begin) {
		t.Fatal("fence missing after apply")
	}
	if result := integration.Rollback(); !result.OK || result.ID != Codex {
		t.Fatalf("rollback: %+v", result)
	}
	if got := readFile(t, configPath); got != userToml {
		t.Fatal("rollback did not restore user bytes")
	}
}
