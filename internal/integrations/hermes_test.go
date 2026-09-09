package integrations

import (
	"strings"
	"testing"
)

var hermesUserSeed = "model:\n  default: nous/ox-alpha\nproviders:\n  prism:\n    api: http://localhost:8080/v1\n    api_key: user-key\n    api_mode: chat_completions\n    discover_models: true\n"

func TestHermesPathResolution(t *testing.T) {
	if got := HermesConfigPath(Env{}, "/home/u"); got != "/home/u/.hermes/config.yaml" {
		t.Fatalf("default path: %s", got)
	}
	override := Env{"HERMES_HOME": "/hermes"}
	if got := HermesConfigPath(override, "/home/u"); got != "/hermes/config.yaml" {
		t.Fatalf("override path: %s", got)
	}
}

func TestHermesApplyWritesPrismProvider(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "config.yaml", hermesUserSeed)
	integration := NewHermes(HermesOptions{Port: testPort, Models: ompTestModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	got := readText(t, path)
	if !strings.Contains(got, "prism:") {
		t.Fatalf("prism provider missing:\n%s", got)
	}
	if !strings.Contains(got, "api: http://127.0.0.1:8787/v1") {
		t.Fatalf("api url missing:\n%s", got)
	}
	if !strings.Contains(got, "api_mode: chat_completions") {
		t.Fatalf("api mode missing:\n%s", got)
	}
	if !strings.Contains(got, "discover_models: false") {
		t.Fatalf("discover_models missing:\n%s", got)
	}
	if !strings.Contains(got, "- gpt-5.2-codex") {
		t.Fatalf("model entry missing:\n%s", got)
	}
	if !strings.Contains(got, "prism:") || !strings.Contains(got, "default: nous/ox-alpha") {
		t.Fatalf("user members lost:\n%s", got)
	}
}

func TestHermesRollbackRoundTrip(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "config.yaml", hermesUserSeed)
	integration := NewHermes(HermesOptions{Port: testPort, Models: ompTestModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	if result := integration.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if readText(t, path) != hermesUserSeed {
		t.Fatalf("rollback did not restore user bytes:\nGOT:\n%s\nWANT:\n%s", readText(t, path), hermesUserSeed)
	}
}

func TestHermesStatusLifecycle(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "config.yaml", hermesUserSeed)
	integration := NewHermes(HermesOptions{Port: testPort, Models: ompTestModels, ConfigPath: path, Home: dir})
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

func TestHermesApplyRefusesFlowStyleLeaf(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "config.yaml", "providers: {prism: 1}\n")
	integration := NewHermes(HermesOptions{Port: testPort, Models: ompTestModels, ConfigPath: path})
	if result := integration.Apply(); result.OK {
		t.Fatalf("flow-style apply unexpectedly succeeded")
	}
	if readText(t, path) != "providers: {prism: 1}\n" {
		t.Fatalf("refused apply changed bytes")
	}
}

func TestHermesVerifierSemantics(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "config.yaml", hermesUserSeed)
	probe := YAMLProviderProbe(Hermes, "config.yaml", "api", ProviderBaseUrl(testPort),
		func(crash bool) WriteOutcome {
			outcome, err := ApplyConfigTransform(path, hermesTransform(testPort, ompTestModels), crash)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("hermes apply", err)}
			}
			return outcome
		},
		func() WriteOutcome {
			outcome, err := ApplyConfigTransform(path, hermesRollbackTransform(), false)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("hermes rollback", err)}
			}
			return outcome
		},
		func() bool { return RecoverHermesConfig(path) },
	)
	for _, check := range VerifyIntegration(probe, path, hermesUserSeed) {
		if !check.OK {
			t.Errorf("%s: %s", check.Semantic, check.Detail)
		}
	}
}
