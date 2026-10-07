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

// Upstreams that close the stream right after the finish chunk (no [DONE])
// must still complete the turn without a run error.
func TestStreamClosedAfterFinishWithoutDoneCompletes(t *testing.T) {
	bodies := map[string]string{
		"finish then close": "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"finish, usage-only chunk, close": "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			c := &collector{}
			err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, c)
			if err != nil {
				t.Fatalf("Run returned %v for a completed turn", err)
			}
			if c.TerminalCount() != 1 {
				t.Fatalf("terminals = %d, want 1", c.TerminalCount())
			}
			if fin := c.events[len(c.events)-1].(canon.TurnFinished); name == "finish, usage-only chunk, close" && fin.Usage.InputTokens != 5 {
				t.Fatalf("usage = %+v, want the trailing usage chunk", fin.Usage)
			}
		})
	}
}
