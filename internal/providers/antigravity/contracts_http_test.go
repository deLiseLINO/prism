package antigravity

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeInvalidUsageKeepsPriorSnapshotHTTP(t *testing.T) {
	for _, invalid := range []string{`-1`, `1.5`, `"2"`, `null`, `9223372036854775808`} {
		t.Run(invalid, func(t *testing.T) {
			payload := "data: {\"response\":{\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":3,\"cachedContentTokenCount\":1}}}\n\n" + "data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":" + invalid + "}}}\n\n"
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
			defer up.Close()
			runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture"}}, up.Client(), up.URL, nil)
			sink := &recordingSink{}
			err := runner.Run(t.Context(), testRequest(), sink)
			var re provider.RunError
			failed, ok := lastTerminal(sink.events).(canon.TurnFailed)
			if !errors.As(err, &re) || re.Class != provider.ClassTransport || !ok || failed.Usage != (canon.Usage{InputTokens: 2, OutputTokens: 3, CachedInputTokens: 1, TotalTokens: 5}) {
				t.Fatalf("error=%v events=%v", err, sink.events)
			}
		})
	}
}

func TestNativeUnsupportedToolConstraintsRejectBeforeHTTP(t *testing.T) {
	for _, scenario := range []string{"parallel", "unknown named", "alternate forced"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, textStream()) }))
			defer up.Close()
			runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture"}}, up.Client(), up.URL, nil)
			req := testRequest()
			req.Request.Tools = []canon.Tool{canon.FunctionTool{Name: "read"}}
			switch scenario {
			case "parallel":
				off := false
				req.Request.Sampling.ParallelToolCalls = &off
			case "unknown named":
				req.Request.ToolChoice = canon.ToolNamed{Name: "missing"}
			case "alternate forced":
				req.Request.Model = "claude-edge"
				req.Request.ToolChoice = canon.ToolRequired{}
			}
			err := runner.Run(t.Context(), req, &recordingSink{})
			var re provider.RunError
			if !errors.As(err, &re) || re.Class != provider.ClassInvalidRequest || calls != 0 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
}
