package customchat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

const trailingFinish = "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"

func runTrailing(t *testing.T, tail string) (*collector, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, trailingFinish+tail+"data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := &collector{}
	err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, c)
	return c, err
}

// Gateways follow the finish chunk with frames whose delta carries keys but no
// output. Those frames must not fail a turn that already completed.
func TestEmptyDeltaAfterFinishCompletes(t *testing.T) {
	tails := map[string]string{
		"role only":        `{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		"empty content":    `{"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		"empty reasoning":  `{"choices":[{"index":0,"delta":{"reasoning_content":"","reasoning":""}}]}`,
		"empty tool calls": `{"choices":[{"index":0,"delta":{"tool_calls":[]}}]}`,
		"null content":     `{"choices":[{"index":0,"delta":{"content":null}}]}`,
		"repeated finish":  `{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}`,
	}
	for name, frame := range tails {
		t.Run(name, func(t *testing.T) {
			c, err := runTrailing(t, "data: "+frame+"\n\n")
			if err != nil {
				t.Fatalf("Run returned %v for a completed turn", err)
			}
			if c.TerminalCount() != 1 {
				t.Fatalf("terminals = %d, want 1", c.TerminalCount())
			}
			if _, ok := c.events[len(c.events)-1].(canon.TurnFinished); !ok {
				t.Fatalf("last event = %T, want TurnFinished", c.events[len(c.events)-1])
			}
		})
	}
}

// Output that arrives after the finish chunk still fails the turn.
func TestOutputAfterFinishFails(t *testing.T) {
	tails := map[string]string{
		"content":   `{"choices":[{"index":0,"delta":{"content":"late"}}]}`,
		"reasoning": `{"choices":[{"index":0,"delta":{"reasoning_content":"late"}}]}`,
		"tool call": `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{}"}}]}}]}`,
	}
	for name, frame := range tails {
		t.Run(name, func(t *testing.T) {
			_, err := runTrailing(t, "data: "+frame+"\n\n")
			if err == nil {
				t.Fatal("Run succeeded although the upstream sent output after the finish reason")
			}
		})
	}
}
