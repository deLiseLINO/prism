package integrations

import (
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
	if !strings.Contains(got, userToml) {
		t.Fatalf("rewrite lost user bytes:\n%s", got)
	}
	if strings.Contains(got, "user_edit") {
		t.Fatalf("foreign edit kept after rewrite:\n%s", got)
	}
}

func TestCodexRollbackRemovesExactlyManagedBlock(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort})
	if outcome := StripCodexConfig(configPath); outcome.Kind != OutcomeWritten {
		t.Fatalf("rollback: %+v", outcome)
	}
	if got := readFile(t, configPath); got != userToml {
		t.Fatalf("rollback did not restore user bytes:\n%q", got)
	}
	if outcome := StripCodexConfig(configPath); outcome.Kind != OutcomeUnchanged {
		t.Fatalf("second rollback: %+v", outcome)
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
	if !FileExists(StagedPath(configPath)) {
		t.Fatal("staged temp missing after crash")
	}
	if !RecoverCodexConfig(configPath) {
		t.Fatal("recovery found no staged temp")
	}
	if FileExists(StagedPath(configPath)) {
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
	if outcome := StripCodexConfig(configPath); outcome.Kind != OutcomeRefused || !contains(outcome.Reason, "damaged") {
		t.Fatalf("rollback on damaged fence: %+v", outcome)
	}
	if got := readFile(t, configPath); got != damaged {
		t.Fatal("damaged file was touched")
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
