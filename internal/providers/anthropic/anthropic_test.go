package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/execution"
	"prism/internal/provider"
)

func TestHTTPErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		class  provider.ErrorClass
	}{
		{"bad request", http.StatusBadRequest, provider.ClassInvalidRequest},
		{"unauthorized", http.StatusUnauthorized, provider.ClassUnauthorized},
		{"forbidden", http.StatusForbidden, provider.ClassForbidden},
		{"not found", http.StatusNotFound, provider.ClassNotFound},
		{"request timeout", http.StatusRequestTimeout, provider.ClassTimeout},
		{"too large", http.StatusRequestEntityTooLarge, provider.ClassContextLength},
		{"rate limited", http.StatusTooManyRequests, provider.ClassRateLimited},
		{"server", http.StatusInternalServerError, provider.ClassServer},
		{"overloaded", 529, provider.ClassServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
			}))
			defer server.Close()
			runner := New(Options{BaseURL: server.URL})
			sink := &collectingSink{}
			err := runner.Run(t.Context(), provider.RunRequest{
				Request: baseRequest(),
				Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
			}, sink)
			var re *provider.RunError
			if !errors.As(err, &re) {
				t.Fatalf("error = %T, want *provider.RunError", err)
			}
			if re.Class != tc.class {
				t.Fatalf("class = %d, want %d", re.Class, tc.class)
			}
			if re.Kind != provider.TerminalOmitted {
				t.Fatalf("kind = %d", re.Kind)
			}
			if !strings.Contains(re.Unwrap().Error(), "boom") {
				t.Fatalf("cause missing upstream message: %v", re.Cause)
			}
			if terminalEvents(sink.events) != 0 {
				t.Fatalf("terminal emitted on failure")
			}
		})
	}
}

func TestRateLimitRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	runner := New(Options{BaseURL: server.URL})
	err := runner.Run(t.Context(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, &collectingSink{})
	var re *provider.RunError
	if !errors.As(err, &re) {
		t.Fatalf("error = %T", err)
	}
	if re.RetryAfter != 7e9 {
		t.Fatalf("retry after = %v", re.RetryAfter)
	}
}

func TestTransportErrorClass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	runner := New(Options{BaseURL: server.URL})
	err := runner.Run(t.Context(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, &collectingSink{})
	var re *provider.RunError
	if !errors.As(err, &re) {
		t.Fatalf("error = %T", err)
	}
	if re.Class != provider.ClassTransport {
		t.Fatalf("class = %d", re.Class)
	}
}

func TestSignatureReplayFromProviderState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fullStreamPayload()))
	}))
	defer server.Close()
	runner := New(Options{BaseURL: server.URL})
	sink := &collectingSink{}
	err := runner.Run(t.Context(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, sink)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	var captured []byte
	replayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, readAll(t, r))
		messages, _ := body["messages"].([]any)
		assistant, _ := messages[1].(map[string]any)
		blocks, _ := assistant["content"].([]any)
		thinking, _ := blocks[0].(map[string]any)
		captured, _ = json.Marshal(thinking)
		w.Write([]byte(sse(
			ssePart("message_start", `{"type":"message_start","message":{}}`),
			ssePart("message_stop", `{"type":"message_stop"}`),
		)))
	}))
	defer replayServer.Close()
	request := baseRequest()
	request.Input = append(request.Input, canon.ReasoningItem{
		ID:      "block-1",
		Content: "pondering",
		State:   canon.OpaqueRef{Store: stateStoreName, Key: "block-1"},
	})
	err = runner.Run(t.Context(), provider.RunRequest{
		Request: request,
		Target:  provider.Target{BaseURL: replayServer.URL, APIKeyRef: "k"},
	}, &collectingSink{})
	if err != nil {
		t.Fatalf("replay run: %v", err)
	}
	var block map[string]any
	if err := json.Unmarshal(captured, &block); err != nil {
		t.Fatalf("block = %s", captured)
	}
	if block["type"] != "thinking" || block["thinking"] != "pondering" || block["signature"] != "sig-abc" {
		t.Fatalf("replayed thinking block = %v", block)
	}
}

func TestSignatureReplayFromCanonField(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, readAll(t, r))
		messages, _ := body["messages"].([]any)
		assistant, _ := messages[1].(map[string]any)
		blocks, _ := assistant["content"].([]any)
		captured, _ = json.Marshal(blocks[0])
		w.Write([]byte(sse(
			ssePart("message_start", `{"type":"message_start","message":{}}`),
			ssePart("message_stop", `{"type":"message_stop"}`),
		)))
	}))
	defer server.Close()
	runner := New(Options{})
	request := baseRequest()
	request.Input = append(request.Input, canon.ReasoningItem{
		ID:        "r1",
		Content:   "pondering",
		Signature: "inline-sig",
	})
	err := runner.Run(t.Context(), provider.RunRequest{
		Request: request,
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, &collectingSink{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var block map[string]any
	if err := json.Unmarshal(captured, &block); err != nil {
		t.Fatalf("block = %s", captured)
	}
	if block["signature"] != "inline-sig" {
		t.Fatalf("signature = %v", block["signature"])
	}
}

func TestUnsupportedItemsRejected(t *testing.T) {
	cases := []canon.Item{
		canon.CustomToolCall{ID: "c1", CallID: "c1", Name: "t"},
		canon.LocalShellCall{ID: "s1", CallID: "s1", Command: "ls"},
		canon.CompactionMarker{ID: "cm1", Kind: canon.CompactionAuto},
		canon.ToolSearchCall{ID: "ts1", CallID: "ts1", Query: "q"},
	}
	for _, item := range cases {
		runner := New(Options{})
		request := baseRequest()
		request.Input = append(request.Input, item)
		err := runner.Run(t.Context(), provider.RunRequest{
			Request: request,
			Target:  provider.Target{BaseURL: "https://api.anthropic.com", APIKeyRef: "k"},
		}, &collectingSink{})
		var re *provider.RunError
		if !errors.As(err, &re) {
			t.Fatalf("%T error = %T", item, err)
		}
		if re.Class != provider.ClassInvalidRequest {
			t.Fatalf("%T class = %d", item, re.Class)
		}
	}
}

func TestPromptCacheKeyLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sse(
			ssePart("message_start", `{"type":"message_start","message":{}}`),
			ssePart("message_stop", `{"type":"message_stop"}`),
		)))
	}))
	defer server.Close()
	runner := New(Options{BaseURL: server.URL, Log: logger})
	for i := 0; i < 2; i++ {
		err := runner.Run(t.Context(), provider.RunRequest{
			Request: baseRequest(),
			Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
		}, &collectingSink{})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	count := strings.Count(buf.String(), promptCacheKeyNote)
	if count != 1 {
		t.Fatalf("prompt cache note logged %d times, want 1", count)
	}
}

func TestTargetBaseURLWins(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(sse(
			ssePart("message_start", `{"type":"message_start","message":{}}`),
			ssePart("message_stop", `{"type":"message_stop"}`),
		)))
	}))
	defer server.Close()
	runner := New(Options{BaseURL: "https://api.anthropic.com"})
	err := runner.Run(context.Background(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL + "/v1", APIKeyRef: "k"},
	}, &collectingSink{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestSessionIDHeaderForwarded(t *testing.T) {
	forward, err := execution.NewForwardSet(http.Header{"X-Session-Id": []string{"sess-42"}})
	if err != nil {
		t.Fatalf("NewForwardSet: %v", err)
	}
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values("X-Session-Id")
		w.Write([]byte(sse(
			ssePart("message_start", `{"type":"message_start","message":{}}`),
			ssePart("message_stop", `{"type":"message_stop"}`),
		)))
	}))
	defer server.Close()
	runner := New(Options{BaseURL: server.URL})
	err = runner.Run(context.Background(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
		Facts:   execution.Facts{Forward: forward},
	}, &collectingSink{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != 1 || got[0] != "sess-42" {
		t.Fatalf("x-session-id = %v, want [sess-42]", got)
	}
}

func TestSessionIDHeaderAbsentWithoutFacts(t *testing.T) {
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values("X-Session-Id")
		w.Write([]byte(sse(
			ssePart("message_start", `{"type":"message_start","message":{}}`),
			ssePart("message_stop", `{"type":"message_stop"}`),
		)))
	}))
	defer server.Close()
	runner := New(Options{BaseURL: server.URL})
	err := runner.Run(context.Background(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, &collectingSink{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("x-session-id = %v, want none", got)
	}
}

func TestSessionIDHeaderEmptyValueNotForwarded(t *testing.T) {
	forward, err := execution.NewForwardSet(http.Header{"X-Session-Id": []string{"   "}})
	if err != nil {
		t.Fatalf("NewForwardSet: %v", err)
	}
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values("X-Session-Id")
		w.Write([]byte(sse(
			ssePart("message_start", `{"type":"message_start","message":{}}`),
			ssePart("message_stop", `{"type":"message_stop"}`),
		)))
	}))
	defer server.Close()
	runner := New(Options{BaseURL: server.URL})
	err = runner.Run(context.Background(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
		Facts:   execution.Facts{Forward: forward},
	}, &collectingSink{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("x-session-id = %v, want none for blank value", got)
	}
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return buf.Bytes()
}
