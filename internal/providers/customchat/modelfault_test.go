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

// A hyphenated or upper-case finish reason means the same as the OpenAI one.
// A reason with no meaning fails as an upstream error, not an overload that
// clients retry without end.
func TestFinishReasonSpellings(t *testing.T) {
	cases := []struct {
		name, reason string
		wantReason   canon.IncompleteReason
		wantFailed   bool
	}{
		{name: "hyphen", reason: "content-filter", wantReason: canon.IncompleteContentFilter},
		{name: "upper case", reason: "CONTENT_FILTER", wantReason: canon.IncompleteContentFilter},
		{name: "unknown", reason: "sideways", wantFailed: true},
	}
	for _, tc := range cases {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s stream=%t", tc.name, stream), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"no\"},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", tc.reason)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"no"},"finish_reason":%q}]}`, tc.reason)
				}))
				defer srv.Close()
				c := &collector{}
				err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(stream), Target: testTarget(srv.URL)}, c)
				last := c.events[len(c.events)-1]
				if tc.wantFailed {
					failed, ok := last.(canon.TurnFailed)
					if !ok || err == nil {
						t.Fatalf("terminal = %T, err = %v; want TurnFailed and an error", last, err)
					}
					if failed.Failure.Reason != canon.FailUpstreamTransport {
						t.Fatalf("failure reason = %d, want FailUpstreamTransport", failed.Failure.Reason)
					}
					return
				}
				finished, ok := last.(canon.TurnFinished)
				if !ok || err != nil {
					t.Fatalf("terminal = %T, err = %v; want TurnFinished", last, err)
				}
				if r, incomplete := finished.Status.Reason(); !incomplete || r != tc.wantReason {
					t.Fatalf("status reason = %d (incomplete %t), want %d", r, incomplete, tc.wantReason)
				}
			})
		}
	}
}
