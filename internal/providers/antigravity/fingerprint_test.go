package antigravity

import (
	"testing"
)

func TestRequestUserAgentByteEquality(t *testing.T) {
	const want = "antigravity/ide/2.5.5 (os_type=windows; arch=amd64; aidev_client; auth_method=oauth)"
	got := RequestUserAgent()
	if got != want {
		t.Fatalf("user agent mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestRequestUserAgentTokenOrder(t *testing.T) {
	ua := RequestUserAgent()
	order := []string{
		"antigravity/ide/2.5.5",
		"os_type=windows",
		"arch=amd64",
		"aidev_client",
		"auth_method=oauth",
	}
	pos := 0
	for _, token := range order {
		idx := indexOf(ua[pos:], token)
		if idx < 0 {
			t.Fatalf("token %q not found in order within %q", token, ua)
		}
		pos += idx + len(token)
	}
}

func TestStreamURL(t *testing.T) {
	const want = "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"
	if got := StreamURL(DefaultBaseURL); got != want {
		t.Fatalf("stream url = %q, want %q", got, want)
	}
	if got := StreamURL("https://cloudcode-pa.googleapis.com/"); got != "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("trailing slash not trimmed: %q", got)
	}
}

func TestSessionIDDeterministic(t *testing.T) {
	a := SessionID("", "hello world")
	b := SessionID("", "hello world")
	if a != b {
		t.Fatalf("session id not deterministic: %q vs %q", a, b)
	}
	if a[0] != '-' {
		t.Fatalf("session id %q lacks '-' prefix", a)
	}
	if SessionID("", "hello") == SessionID("", "world") {
		t.Fatal("distinct texts produced identical session ids")
	}
	if SessionID("t1", "hello") != SessionID("t1", "other") {
		t.Fatal("thread anchor must dominate first user text")
	}
	if SessionID("", "")[0] != '-' {
		t.Fatal("fallback session id lacks '-' prefix")
	}
}

func TestNewRequestIDShape(t *testing.T) {
	id := NewRequestID()
	if len(id) != len("agent-")+36 || id[:6] != "agent-" {
		t.Fatalf("request id %q does not match agent-<uuid> shape", id)
	}
}

func TestLikelyRealSignature(t *testing.T) {
	valid := []string{
		"AbCdEf123456+/7890_-sig",
		"0123456789abcdef",
		"aGVsbG8gd29ybGQgYmFzZTY0",
		"sig_signature_value_1234",
	}
	for _, sig := range valid {
		if !likelyRealSignature(sig) {
			t.Errorf("signature %q should be real", sig)
		}
	}
	invalid := []string{
		"",
		"short",
		"skip_thought_signature_validator",
		"fc_0123456789abcdef",
		"toolu_0123456789abcdef",
		"call_0123456789abcdef",
		"FUNC-0123456789abcdef",
		"has space in the sig",
		"unicode-\u00e9-0123456789",
	}
	for _, sig := range invalid {
		if likelyRealSignature(sig) {
			t.Errorf("signature %q should be rejected", sig)
		}
	}
}

func indexOf(s, sub string) int {
	for i := range len(s) - len(sub) + 1 {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
