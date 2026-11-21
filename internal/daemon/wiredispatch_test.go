package daemon

import (
	"net/http"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestSessionKeyOrder(t *testing.T) {
	forward, err := execution.NewForwardSet(http.Header{"X-Session-Id": {"omp-1"}})
	if err != nil {
		t.Fatal(err)
	}
	user := func(text string) canon.Request {
		return canon.Request{Input: []canon.Item{
			canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: text}}},
		}}
	}
	header := sessionKey(provider.RunRequest{Request: user("q"), Facts: execution.Facts{Forward: forward, Session: "other"}})
	facts := sessionKey(provider.RunRequest{Request: user("q"), Facts: execution.Facts{Session: "codex-1"}})
	first := sessionKey(provider.RunRequest{Request: user("q")})
	if header != "session:omp-1" || facts != "session:codex-1" || !strings.HasPrefix(first, "first-user:") {
		t.Fatalf("keys %q %q %q", header, facts, first)
	}
	if again := sessionKey(provider.RunRequest{Request: user("q")}); again != first {
		t.Fatalf("first-user key not stable: %q vs %q", again, first)
	}
	if sessionKey(provider.RunRequest{Request: user("other")}) == first {
		t.Fatal("different first messages share a key")
	}
	if got := sessionKey(provider.RunRequest{}); got != "" {
		t.Fatalf("request without a session got %q", got)
	}
}
