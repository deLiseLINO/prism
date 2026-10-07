package customresponses

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestInvalidUsageKeepsLastValidSnapshot(t *testing.T) {
	for _, ending := range []string{
		`data: {bad` + "\n\n",
		`data: {"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":1.5}}}` + "\n\n",
		"",
	} {
		t.Run(ending, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"response.in_progress","response":{"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cached_tokens":4}}}}`+"\n\n"+ending)
			}))
			defer upstream.Close()
			sink := &collector{}
			var attempt provider.NetworkAttempt
			err := New(nil, Options{}).Run(t.Context(), provider.RunRequest{Target: provider.Target{Provider: "edge", Wire: provider.WireResponses, BaseURL: upstream.URL}, Request: canon.Request{Model: "m", Stream: true}, AttemptObserver: func(value provider.NetworkAttempt) { attempt = value }}, sink)
			if err == nil || attempt.Err == nil || attempt.StatusCode != 200 {
				t.Fatalf("error=%v attempt=%+v", err, attempt)
			}
			if sink.TerminalCount() != 1 {
				t.Fatalf("events=%+v", sink.All())
			}
			terminal := sink.All()[len(sink.All())-1].(canon.TurnFailed)
			if terminal.Usage != (canon.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12, CachedInputTokens: 4}) {
				t.Fatalf("usage=%+v", terminal.Usage)
			}
		})
	}
}
