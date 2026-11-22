package customchat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

const (
	faultStream     = "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"MALFORMED_RESPONSE\"}]}\n\n"
	goodStream      = "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	lateFaultStream = "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"malformed_response\"}]}\n\n"
	faultJSON = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"malformed_function_call"}]}`
	goodJSON  = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`
)

// A finish reason that names a model fault is sent again once while the
// client has received nothing. Output already emitted is never replayed.
func TestModelFaultFinishIsSentAgainOnce(t *testing.T) {
	cases := []struct {
		name         string
		stream       bool
		bodies       []string
		wantRequests int
		wantFailure  string
	}{
		{name: "stream fault then success", stream: true, bodies: []string{faultStream, goodStream}, wantRequests: 2},
		{name: "aggregate fault then success", bodies: []string{faultJSON, goodJSON}, wantRequests: 2},
		{name: "stream fault twice", stream: true, bodies: []string{faultStream, faultStream}, wantRequests: 2,
			wantFailure: `upstream model returned a faulty response (finish reason "MALFORMED_RESPONSE")`},
		{name: "aggregate fault twice", bodies: []string{faultJSON, faultJSON}, wantRequests: 2,
			wantFailure: `upstream model returned a faulty response (finish reason "malformed_function_call")`},
		{name: "stream fault after output", stream: true, bodies: []string{lateFaultStream, goodStream}, wantRequests: 1,
			wantFailure: `upstream model returned a faulty response (finish reason "malformed_response")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := tc.bodies[requests]
				requests++
				if strings.HasPrefix(body, "data:") {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			c := &collector{}
			err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(tc.stream), Target: testTarget(srv.URL)}, c)
			if requests != tc.wantRequests {
				t.Fatalf("upstream requests = %d, want %d", requests, tc.wantRequests)
			}
			if c.TerminalCount() != 1 {
				t.Fatalf("terminals = %d, want 1", c.TerminalCount())
			}
			last := c.events[len(c.events)-1]
			if tc.wantFailure == "" {
				if err != nil {
					t.Fatalf("Run returned %v after the second response succeeded", err)
				}
				if _, ok := last.(canon.TurnFinished); !ok {
					t.Fatalf("terminal = %T, want TurnFinished", last)
				}
				return
			}
			failed, ok := last.(canon.TurnFailed)
			if !ok {
				t.Fatalf("terminal = %T, want TurnFailed", last)
			}
			if failed.Failure.Message != tc.wantFailure {
				t.Fatalf("failure message = %q, want %q", failed.Failure.Message, tc.wantFailure)
			}
			if err == nil {
				t.Fatal("Run returned nil for a failed turn")
			}
		})
	}
}
