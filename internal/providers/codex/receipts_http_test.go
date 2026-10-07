package codex

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeInvalidTerminalRetainsLastValidUsageHTTP(t *testing.T) {
	payload := joinFrames(`{"type":"response.in_progress","response":{"usage":{"input_tokens":7,"output_tokens":2,"input_tokens_details":{"cached_tokens":1}}}}`, `{"type":"response.completed","response":{"usage":{"input_tokens":-1},"output":[]}}`)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
	defer up.Close()
	runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "fixture"}}, Client: up.Client()}
	sink := &captureSink{}
	err := runner.Run(t.Context(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}}, sink)
	var re provider.RunError
	if !errors.As(err, &re) || re.Kind != provider.TerminalEmitted || len(sink.events) != 1 {
		t.Fatalf("error=%v events=%v", err, sink.events)
	}
	failure, ok := sink.events[0].(canon.TurnFailed)
	if !ok || failure.Usage != (canon.Usage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9, CachedInputTokens: 1}) {
		t.Fatalf("failed usage=%v", failure)
	}
}
