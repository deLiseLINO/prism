package execution

import (
	"net/http"
	"strings"
	"testing"
)

func TestNewForwardSetAllowlist(t *testing.T) {
	h := http.Header{}
	h.Set("X-Session-Id", "sess-1")
	h.Set("originator", "codex_cli_rs")
	h.Set("User-Agent", "prism/0.1")

	f, err := NewForwardSet(h)
	if err != nil {
		t.Fatalf("NewForwardSet(%v): %v", h, err)
	}
	if v, ok := f.Get(ForwardSessionID); !ok || v != "sess-1" {
		t.Errorf("Get(ForwardSessionID) = %q, %v; want %q, true", v, ok, "sess-1")
	}
	if v, ok := f.Get(ForwardOriginator); !ok || v != "codex_cli_rs" {
		t.Errorf("Get(ForwardOriginator) = %q, %v; want %q, true", v, ok, "codex_cli_rs")
	}
	if v, ok := f.Get(ForwardUserAgent); !ok || v != "prism/0.1" {
		t.Errorf("Get(ForwardUserAgent) = %q, %v; want %q, true", v, ok, "prism/0.1")
	}
}

func TestNewForwardSetEmpty(t *testing.T) {
	f, err := NewForwardSet(http.Header{})
	if err != nil {
		t.Fatalf("NewForwardSet(empty): %v", err)
	}
	if _, ok := f.Get(ForwardSessionID); ok {
		t.Error("Get on empty set returned ok")
	}
	if got := f.Redacted(); got != "" {
		t.Errorf("Redacted() = %q; want empty", got)
	}
}

func TestNewForwardSetRejectsForbidden(t *testing.T) {
	for _, name := range []string{"authorization", "x-api-key", "cookie"} {
		for _, casing := range []string{name, strings.ToUpper(name), strings.ToUpper(name[:1]) + name[1:]} {
			h := http.Header{}
			h.Set(casing, "secret-value")
			if _, err := NewForwardSet(h); err == nil {
				t.Errorf("NewForwardSet(%s=%q): expected error, got none", casing, "secret-value")
			}
		}
	}
}

func TestNewForwardSetRejectsUnknown(t *testing.T) {
	for _, name := range []string{"x-foo", "content-type", "accept", "x-codex-parent-thread-id", "x-openai-subagent"} {
		h := http.Header{}
		h.Set(name, "v")
		if _, err := NewForwardSet(h); err == nil {
			t.Errorf("NewForwardSet(%s): expected error, got none", name)
		}
	}
}

func TestNewForwardSetCaseInsensitive(t *testing.T) {
	for _, h := range []http.Header{
		{"user-agent": {"ua/1"}},
		{"User-Agent": {"ua/1"}},
		{"USER-AGENT": {"ua/1"}},
		{"X-SESSION-ID": {"s"}},
		{"ORIGINATOR": {"o"}},
	} {
		f, err := NewForwardSet(h)
		if err != nil {
			t.Fatalf("NewForwardSet(%v): %v", h, err)
		}
		if len(f.pairs) != 1 {
			t.Errorf("NewForwardSet(%v): %d pairs; want 1", h, len(f.pairs))
		}
	}
}

func TestNewForwardSetDuplicateCaseVariants(t *testing.T) {
	h := http.Header{}
	h.Add("x-session-id", "first")
	h.Add("X-Session-Id", "second")
	f, err := NewForwardSet(h)
	if err != nil {
		t.Fatalf("NewForwardSet(%v): %v", h, err)
	}
	if v, ok := f.Get(ForwardSessionID); !ok || v != "first" {
		t.Errorf("Get(ForwardSessionID) = %q, %v; want %q, true", v, ok, "first")
	}
}

func TestNewForwardSetCopiesValues(t *testing.T) {
	h := http.Header{}
	h.Set("originator", "codex_cli_rs")
	f, err := NewForwardSet(h)
	if err != nil {
		t.Fatalf("NewForwardSet: %v", err)
	}
	h.Set("originator", "tampered")
	if v, ok := f.Get(ForwardOriginator); !ok || v != "codex_cli_rs" {
		t.Errorf("Get after mutation = %q, %v; want %q, true", v, ok, "codex_cli_rs")
	}
}

func TestForwardSetRedactedNeverLeaksValues(t *testing.T) {
	h := http.Header{}
	h.Set("X-Session-Id", "attestation-token-super-secret")
	h.Set("originator", "codex_cli_rs")
	h.Set("User-Agent", "prism/0.1")
	f, err := NewForwardSet(h)
	if err != nil {
		t.Fatalf("NewForwardSet: %v", err)
	}
	redacted := f.Redacted()
	for _, want := range []string{"x-session-id", "originator", "user-agent"} {
		if !strings.Contains(redacted, want) {
			t.Errorf("Redacted() = %q; want name %q present", redacted, want)
		}
	}
	for _, leak := range []string{"attestation-token-super-secret", "codex_cli_rs", "prism/0.1"} {
		if strings.Contains(redacted, leak) {
			t.Errorf("Redacted() = %q leaks value %q", redacted, leak)
		}
	}
}

func TestForwardSetRedactedDeterministic(t *testing.T) {
	h := http.Header{}
	h.Set("originator", "o")
	h.Set("X-Session-Id", "s")
	h.Set("User-Agent", "u")
	f, err := NewForwardSet(h)
	if err != nil {
		t.Fatalf("NewForwardSet: %v", err)
	}
	if got, want := f.Redacted(), "originator=<redacted>;user-agent=<redacted>;x-session-id=<redacted>"; got != want {
	}
}

func TestForbiddenHeadersNeverSurviveIntoFacts(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer leaked-token")
	h.Set("X-Api-Key", "sk-leaked")
	h.Set("Cookie", "session=leaked")
	f, err := NewForwardSet(h)
	if err == nil {
		t.Fatalf("NewForwardSet with forbidden headers: expected error, got set %v", f)
	}
	empty, err := NewForwardSet(http.Header{})
	if err != nil {
		t.Fatalf("NewForwardSet(empty): %v", err)
	}
	facts := Facts{
		RequestID: "r1",
		Client:    ClientCodex,
		Session:   "s1",
		Thread:    "t1",
		Forward:   empty,
	}
	if got, ok := facts.Forward.Get(ForwardSessionID); ok && got != "" {
		t.Errorf("Facts.Forward contains forbidden value %q", got)
	}
	for _, leak := range []string{"Bearer leaked-token", "sk-leaked", "session=leaked"} {
		if strings.Contains(facts.Forward.Redacted(), leak) {
			t.Errorf("Facts redaction leaks %q", leak)
		}
	}
}

func TestClientClosedSet(t *testing.T) {
	clients := []Client{ClientCodex, ClientGrok, ClientOMP, ClientAnthropic}
	seen := make(map[Client]bool)
	for _, c := range clients {
		if c == 0 {
			t.Error("zero Client value exists; closed set must start at 1")
		}
		if seen[c] {
			t.Errorf("duplicate Client value %d", c)
		}
		seen[c] = true
	}
	if got := len(clients); got != 4 {
		t.Errorf("Client set has %d members; want 4", got)
	}
}

func TestForwardNameClosedSet(t *testing.T) {
	names := []ForwardName{ForwardSessionID, ForwardOriginator, ForwardUserAgent}
	seen := make(map[ForwardName]bool)
	for _, n := range names {
		if n == 0 {
			t.Error("zero ForwardName value exists; closed set must start at 1")
		}
		if seen[n] {
			t.Errorf("duplicate ForwardName value %d", n)
		}
		seen[n] = true
	}
	if got := len(names); got != 3 {
		t.Errorf("ForwardName set has %d members; want 3", got)
	}
}
