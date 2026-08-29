package codex

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"prism/internal/account"
	"prism/internal/execution"
	"prism/internal/provider"
)

type fixedCreds struct{ cred Credential }

func (f fixedCreds) Credential(id account.AccountID) (Credential, error) { return f.cred, nil }

type goldenFingerprint struct {
	Fingerprint struct {
		Method  string   `json:"method"`
		URL     string   `json:"url"`
		Headers []Header `json:"headers"`
	} `json:"fingerprint"`
}

func loadFingerprintGolden(t *testing.T) goldenFingerprint {
	t.Helper()
	raw, err := os.ReadFile("fixtures/fingerprint-v1.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var golden goldenFingerprint
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return golden
}

func TestFingerprintMatchesGolden(t *testing.T) {
	golden := loadFingerprintGolden(t)
	facts := execution.Facts{
		RequestID: "req-1",
		Forward:   forwardSetForTest(t, "x-session-id: sess-1", "originator: codex_cli", "user-agent: codex/1.0"),
	}
	fp, err := BuildFingerprint(fingerprintInput{
		Facts:      facts,
		Credential: Credential{AccessToken: "tok-1", ChatGPTAccountID: "acct-1"},
	})
	if err != nil {
		t.Fatalf("BuildFingerprint: %v", err)
	}
	if fp.Method != golden.Fingerprint.Method {
		t.Fatalf("method = %q want %q", fp.Method, golden.Fingerprint.Method)
	}
	if fp.URL != golden.Fingerprint.URL {
		t.Fatalf("url = %q want %q", fp.URL, golden.Fingerprint.URL)
	}
	want := golden.Fingerprint.Headers
	if len(fp.Headers) != len(want) {
		t.Fatalf("header count = %d want %d\n got: %v\nwant: %v", len(fp.Headers), len(want), fp.Headers, want)
	}
	for i := range want {
		if fp.Headers[i] != want[i] {
			t.Fatalf("header[%d] = %+v want %+v", i, fp.Headers[i], want[i])
		}
	}
}

func TestFingerprintXClientRequestIDGenerated(t *testing.T) {
	facts := execution.Facts{Forward: forwardSetForTest(t)}
	fp, err := BuildFingerprint(fingerprintInput{
		Facts:      facts,
		Credential: Credential{AccessToken: "tok"},
	})
	if err != nil {
		t.Fatalf("BuildFingerprint: %v", err)
	}
	var got string
	for _, h := range fp.Headers {
		if h.Name == HeaderClientRequestID {
			got = h.Value
		}
	}
	if len(got) != 36 || got[8] != '-' || got[13] != '-' || got[14] != '4' {
		t.Fatalf("generated x-client-request-id = %q, want uuid v4 shape", got)
	}
}

func TestFingerprintCustomBaseURL(t *testing.T) {
	fp, err := BuildFingerprint(fingerprintInput{
		Target:     provider.Target{BaseURL: "https://chatgpt.com/backend-api/codex/"},
		Credential: Credential{AccessToken: "tok"},
	})
	if err != nil {
		t.Fatalf("BuildFingerprint: %v", err)
	}
	if fp.URL != ResponsesURL {
		t.Fatalf("url = %q", fp.URL)
	}
}

func forwardSetForTest(t *testing.T, pairs ...string) execution.ForwardSet {
	t.Helper()
	h := make(map[string][]string, len(pairs))
	for _, p := range pairs {
		name, value, _ := strings.Cut(p, ":")
		h[name] = []string{strings.TrimSpace(value)}
	}
	set, err := execution.NewForwardSet(h)
	if err != nil {
		t.Fatalf("NewForwardSet: %v", err)
	}
	return set
}

func TestRegisterAddsCodexRunner(t *testing.T) {
	registry := provider.NewRegistry()
	if err := Register(registry, fixedCreds{}, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := registry.Lookup(ProviderID); !ok {
		t.Fatalf("Lookup(%q) missing", ProviderID)
	}
	if err := Register(registry, fixedCreds{}, nil); err == nil {
		t.Fatalf("duplicate Register succeeded")
	}
}

func TestForwardHeadersReplicated(t *testing.T) {
	want := []string{
		"authorization",
		"chatgpt-account-id",
		"openai-beta",
		"originator",
		"session_id",
		"session-id",
		"thread-id",
		"x-client-request-id",
		"x-codex-beta-features",
		"x-codex-installation-id",
		"x-codex-parent-thread-id",
		"x-codex-turn-metadata",
		"x-codex-turn-state",
		"x-codex-window-id",
		"x-oai-attestation",
		"x-openai-subagent",
		"x-responsesapi-include-timing-metrics",
	}
	if len(forwardHeaders) != len(want) {
		t.Fatalf("FORWARD_HEADERS len = %d want %d", len(forwardHeaders), len(want))
	}
	for i := range want {
		if forwardHeaders[i] != want[i] {
			t.Fatalf("FORWARD_HEADERS[%d] = %q want %q", i, forwardHeaders[i], want[i])
		}
	}
}
