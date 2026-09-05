package integrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var piUserSeed = "{\n  \"providers\": {\n    \"edge-vps\": {\n      \"baseUrl\": \"http://127.0.0.1:9797/v1\",\n      \"api\": \"openai-completions\",\n      \"apiKey\": \"user-key\",\n      \"models\": []\n    }\n  }\n}\n"

func TestPiPathResolution(t *testing.T) {
	if got, err := PiConfigPath(Env{}, "/home/u"); err != nil || got != "/home/u/.pi/agent/models.json" {
		t.Fatalf("default path: %s %v", got, err)
	}
	if got, err := PiConfigPath(Env{"PI_CODING_AGENT_DIR": "/agent"}, "/home/u"); err != nil || got != "/agent/models.json" {
		t.Fatalf("override path: %s %v", got, err)
	}
	if got, err := PiConfigPath(Env{"PI_CODING_AGENT_DIR": "~/agent"}, "/home/u"); err != nil || got != "/home/u/agent/models.json" {
		t.Fatalf("tilde path: %s %v", got, err)
	}
	if _, err := PiConfigPath(Env{"PI_CODING_AGENT_DIR": "relative"}, "/home/u"); err == nil {
		t.Fatal("relative override accepted")
	}
}

func TestPiApplyWritesPrismProvider(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "models.json", piUserSeed)
	integration := NewPi(PiOptions{Port: testPort, Models: grokTestModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	got := readText(t, path)
	assertValidJSON(t, got)
	if !strings.Contains(got, `"prism": {`) {
		t.Fatalf("prism provider missing:\n%s", got)
	}
	if !strings.Contains(got, `"baseUrl": "http://127.0.0.1:8787/v1"`) {
		t.Fatalf("base url missing:\n%s", got)
	}
	if !strings.Contains(got, `"edge-vps"`) {
		t.Fatalf("user provider lost:\n%s", got)
	}
	if !strings.Contains(got, `"input": [`) {
		t.Fatalf("input modalities missing:\n%s", got)
	}
	if !strings.Contains(got, `"contextWindow": 400000`) {
		t.Fatalf("context window missing:\n%s", got)
	}
}

func TestPiRollbackRoundTrip(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "models.json", piUserSeed)
	integration := NewPi(PiOptions{Port: testPort, Models: ompTestModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	if result := integration.Rollback(); !result.OK {
		t.Fatalf("rollback: %+v", result)
	}
	if readText(t, path) != piUserSeed {
		t.Fatalf("rollback did not restore user bytes:\nGOT:\n%s\nWANT:\n%s", readText(t, path), piUserSeed)
	}
}

func TestPiStatusLifecycle(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "models.json", piUserSeed)
	integration := NewPi(PiOptions{Port: testPort, Models: ompTestModels, ConfigPath: path, Home: dir, Env: Env{"PI_CODING_AGENT_DIR": dir}})
	status := integration.Status()
	if !status.Installed || status.Managed {
		t.Fatalf("fresh status: %+v", status)
	}
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	status = integration.Status()
	if !status.Managed || status.Endpoint == nil || *status.Endpoint != "http://127.0.0.1:8787/v1" {
		t.Fatalf("managed status: %+v", status)
	}
}

func TestPiApplyRefusesFlowStyle(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "models.json", `{"providers": {"x": 1}}`+"\n")
	integration := NewPi(PiOptions{Port: testPort, Models: ompTestModels, ConfigPath: path})
	if result := integration.Apply(); result.OK {
		t.Fatalf("flow-style apply unexpectedly succeeded")
	}
	if readText(t, path) != `{"providers": {"x": 1}}`+"\n" {
		t.Fatalf("refused apply changed bytes")
	}
}

func TestPiVerifierSemantics(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "models.json", piUserSeed)
	probe := JSONBlockProbe(Pi, "providers", "models.json", "baseUrl", ProviderBaseUrl(testPort),
		func(crash bool) WriteOutcome {
			outcome, err := ApplyConfigTransform(path, piTransform(testPort, ompTestModels), crash)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("pi apply", err)}
			}
			return outcome
		},
		func() WriteOutcome {
			outcome, err := ApplyConfigTransform(path, piRollbackTransform(), false)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("pi rollback", err)}
			}
			return outcome
		},
		func() bool { return RecoverPiConfig(path) },
	)
	for _, check := range VerifyIntegration(probe, path, piUserSeed) {
		if !check.OK {
			t.Errorf("%s: %s", check.Semantic, check.Detail)
		}
	}
}

func TestPiAgentDirOverrideDetection(t *testing.T) {
	dir := tempDir(t)
	agentDir := filepath.Join(dir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := tempFile(t, dir, "models.json", piUserSeed)
	integration := NewPi(PiOptions{Port: testPort, Models: ompTestModels, ConfigPath: path, Home: "/nonexistent", Env: Env{"PI_CODING_AGENT_DIR": agentDir}})
	if status := integration.Status(); !status.Installed {
		t.Fatalf("override detection dir not honored: %+v", status)
	}
}
