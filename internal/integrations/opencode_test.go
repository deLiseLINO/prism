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

func TestOpencodeApplyWritesPrismProvider(t *testing.T) {
	dir := tempDir(t)
	path := tempFile(t, dir, "opencode.json", opencodeUserSeed)
	integration := NewOpencode(OpencodeOptions{Port: testPort, Models: ompTestModels, ConfigPath: path})
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
			outcome, err := ApplyConfigTransform(path, opencodeTransform(testPort, ompTestModels), crash)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode apply", err)}
			}
			return outcome
		},
		func() WriteOutcome {
			outcome, err := ApplyConfigTransform(path, opencodeRollbackTransform(), false)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode rollback", err)}
			}
			return outcome
		},
		func() bool { return RecoverOpencodeConfig(path) },
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
	integration := NewOpencode2(Opencode2Options{Port: testPort, Models: ompTestModels, ConfigPath: path})
	if result := integration.Apply(); !result.OK {
		t.Fatalf("apply: %+v", result)
	}
	got := readText(t, path)
	assertValidJSON(t, got)
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
			outcome, err := ApplyConfigTransform(path, opencode2Transform(testPort, ompTestModels), crash)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode2 apply", err)}
			}
			return outcome
		},
		func() WriteOutcome {
			outcome, err := ApplyConfigTransform(path, opencode2RollbackTransform(), false)
			if err != nil {
				return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode2 rollback", err)}
			}
			return outcome
		},
		func() bool { return RecoverOpencode2Config(path) },
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
