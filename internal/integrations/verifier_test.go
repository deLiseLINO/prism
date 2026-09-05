package integrations

import (
	"strings"
	"testing"
)

func codexProbe(configPath string) Probe {
	return FencedProbe(Codex, CodexFence,
		func(crash bool) WriteOutcome {
			return WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort, CrashBeforeRename: crash})
		},
		func() WriteOutcome { return StripCodexConfig(configPath) },
		func() bool { return RecoverCodexConfig(configPath) },
	)
}

func grokProbe(configPath string) Probe {
	return FencedProbe(Grok, GrokFence,
		func(crash bool) WriteOutcome {
			return WriteGrokConfig(GrokOptions{ConfigPath: configPath, Port: testPort, Models: grokTestModels, CrashBeforeRename: crash})
		},
		func() WriteOutcome { return StripGrokConfig(configPath) },
		func() bool { return RecoverGrokConfig(configPath) },
	)
}

func ompProbeFor(modelsPath string) Probe {
	return OmpProbe(
		func(crash bool) WriteOutcome {
			return WriteOmpConfig(OmpOptions{ModelsPath: modelsPath, Port: testPort, Models: ompTestModels, CrashBeforeRename: crash})
		},
		func() WriteOutcome { return StripOmpConfig(modelsPath) },
		func() bool { return RecoverOmpConfig(modelsPath) },
		ProviderBaseUrl(testPort),
	)
}

func requireAllPass(t *testing.T, checks []Check) {
	t.Helper()
	for _, check := range checks {
		if !check.OK {
			t.Fatalf("%s failed: %s", check.Semantic, check.Detail)
		}
	}
}

func TestVerifierCodex(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	checks := VerifyIntegration(codexProbe(configPath), configPath, userToml)
	if checks[0].Semantic != "reapply-stable" || !checks[0].OK {
		t.Fatalf("reapply-stable: %+v", checks[0])
	}
	requireAllPass(t, checks)
}

func TestVerifierGrok(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", userToml)
	requireAllPass(t, VerifyIntegration(grokProbe(configPath), configPath, userToml))
}

func TestVerifierOmp(t *testing.T) {
	dir := t.TempDir()
	modelsPath := tempFile(t, dir, "models.yml", userModelYaml)
	requireAllPass(t, VerifyIntegration(ompProbeFor(modelsPath), modelsPath, userModelYaml))
}

func TestVerifierOmpEmptyDocument(t *testing.T) {
	dir := t.TempDir()
	modelsPath := tempFile(t, dir, "models.yml", "")
	requireAllPass(t, VerifyIntegration(ompProbeFor(modelsPath), modelsPath, ""))
}

func TestEolHandlingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	configPath := tempFile(t, dir, "config.toml", strings.ReplaceAll(userToml, "\n", "\r\n"))
	if outcome := WriteCodexConfig(CodexOptions{ConfigPath: configPath, Port: testPort}); outcome.Kind != OutcomeWritten {
		t.Fatalf("apply to crlf file: %+v", outcome)
	}
	content := readFile(t, configPath)
	if !contains(content, "\r\n") {
		t.Fatal("dominant CRLF EOL not restored")
	}
	if strings.Contains(strings.ReplaceAll(content, "\r\n", ""), "\n") {
		t.Fatal("mixed EOLs after apply")
	}
	if !contains(content, "base_url = \"http://127.0.0.1:8787/v1\"") {
		t.Fatal("managed block missing after crlf apply")
	}
}
