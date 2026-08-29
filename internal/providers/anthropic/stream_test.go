package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/provider"
)

type collectingSink struct {
	events []canon.Event
}

func (s *collectingSink) Emit(ev canon.Event) error {
	s.events = append(s.events, ev)
	return nil
}

func terminalEvents(events []canon.Event) int {
	count := 0
	for _, ev := range events {
		switch ev.(type) {
		case canon.TurnFinished, canon.TurnFailed:
			count++
		}
	}
	return count
}

func ssePart(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

func sse(parts ...string) string {
	return strings.Join(parts, "")
}

func runAgainst(t *testing.T, payload string) (*collectingSink, *Runner) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	runner := New(Options{BaseURL: server.URL})
	sink := &collectingSink{}
	request := baseRequest()
	request.Model = "claude-sonnet-4-5"
	err := runner.Run(t.Context(), provider.RunRequest{
		Request: request,
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return sink, runner
}

func fullStreamPayload() string {
	return sse(
		ssePart("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`),
		ssePart("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`),
		ssePart("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
		ssePart("content_block_stop", `{"type":"content_block_stop","index":0}`),
		ssePart("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"thinking"}}`),
		ssePart("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"pondering"}}`),
		ssePart("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig-abc"}}`),
		ssePart("content_block_stop", `{"type":"content_block_stop","index":1}`),
		ssePart("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}`),
		ssePart("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`),
		ssePart("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`),
		ssePart("content_block_stop", `{"type":"content_block_stop","index":2}`),
		ssePart("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":25}}`),
		ssePart("message_stop", `{"type":"message_stop"}`),
	)
}

func TestFullStreamVocabulary(t *testing.T) {
	sink, _ := runAgainst(t, fullStreamPayload())
	if n := terminalEvents(sink.events); n != 1 {
		t.Fatalf("terminal events = %d, want exactly 1", n)
	}
	want := []canon.Event{
		canon.ItemStarted{Item: canon.Message{ID: "block-0", Role: canon.RoleAssistant}},
		canon.TextDelta{ItemID: "block-0", Text: "Hello"},
		canon.ItemFinished{Item: canon.Message{ID: "block-0", Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "Hello"}}}},
		canon.ItemStarted{Item: canon.ReasoningItem{ID: "block-1"}},
		canon.ReasoningDelta{ItemID: "block-1", Text: "pondering"},
		canon.ItemStateAvailable{ItemID: "block-1", State: canon.OpaqueRef{Store: stateStoreName, Key: "block-1"}},
		canon.ItemFinished{Item: canon.ReasoningItem{ID: "block-1", Content: "pondering", State: canon.OpaqueRef{Store: stateStoreName, Key: "block-1"}}},
		canon.ItemStarted{Item: canon.FunctionCall{ID: "toolu_1", CallID: "toolu_1", Name: "get_weather"}},
		canon.ToolArgumentsDelta{ItemID: "toolu_1", Bytes: []byte(`{"city":`)},
		canon.ToolArgumentsDelta{ItemID: "toolu_1", Bytes: []byte(`"Paris"}`)},
		canon.ItemFinished{Item: canon.FunctionCall{ID: "toolu_1", CallID: "toolu_1", Name: "get_weather", Arguments: []byte(`{"city":"Paris"}`)}},
		canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 115, OutputTokens: 25, CachedInputTokens: 10, TotalTokens: 140}},
	}
	if len(sink.events) != len(want) {
		t.Fatalf("events = %d, want %d", len(sink.events), len(want))
	}
	for i := range want {
		gotJSON, _ := json.Marshal(sink.events[i])
		wantJSON, _ := json.Marshal(want[i])
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("event[%d] = %s, want %s", i, gotJSON, wantJSON)
		}
	}
}

func TestThinkingSignatureCapturedInProviderStore(t *testing.T) {
	_, runner := runAgainst(t, fullStreamPayload())
	blob, ok := runner.state.get("block-1")
	if !ok {
		t.Fatalf("signature not stored for block-1")
	}
	if string(blob) != "sig-abc" {
		t.Fatalf("signature = %q", blob)
	}
}

func TestStopReasonMapping(t *testing.T) {
	cases := []struct {
		stop string
		kind canon.StatusKind
		why  canon.IncompleteReason
	}{
		{"end_turn", canon.StatusCompleted, 0},
		{"stop_sequence", canon.StatusCompleted, 0},
		{"tool_use", canon.StatusCompleted, 0},
		{"max_tokens", canon.StatusIncomplete, canon.IncompleteMaxOutputTokens},
		{"refusal", canon.StatusIncomplete, canon.IncompleteContentFilter},
		{"content_filter", canon.StatusIncomplete, canon.IncompleteContentFilter},
		{"weird", canon.StatusCompleted, 0},
	}
	for _, tc := range cases {
		t.Run(tc.stop, func(t *testing.T) {
			payload := sse(
				ssePart("message_start", `{"type":"message_start","message":{}}`),
				ssePart("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q}}`, tc.stop)),
				ssePart("message_stop", `{"type":"message_stop"}`),
			)
			sink, _ := runAgainst(t, payload)
			if n := terminalEvents(sink.events); n != 1 {
				t.Fatalf("terminal events = %d", n)
			}
			terminal := sink.events[len(sink.events)-1].(canon.TurnFinished)
			if terminal.Status.Kind() != tc.kind {
				t.Fatalf("kind = %d, want %d", terminal.Status.Kind(), tc.kind)
			}
			if tc.kind == canon.StatusIncomplete {
				reason, ok := terminal.Status.Reason()
				if !ok || reason != tc.why {
					t.Fatalf("reason = %d %t, want %d", reason, ok, tc.why)
				}
			}
		})
	}
}

func TestStreamEOFWithoutTerminal(t *testing.T) {
	payload := ssePart("message_start", `{"type":"message_start","message":{}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(payload))
	}))
	defer server.Close()
	runner := New(Options{BaseURL: server.URL})
	err := runner.Run(t.Context(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, &collectingSink{})
	var re *provider.RunError
	if err == nil {
		t.Fatalf("expected RunError, got nil")
	}
	if !errors.As(err, &re) {
		t.Fatalf("error = %T, want *provider.RunError", err)
	}
	if re.Kind != provider.TerminalOmitted || re.Class != provider.ClassTransport {
		t.Fatalf("run error = kind %d class %d", re.Kind, re.Class)
	}
}

func TestMalformedToolArgumentsFailTurn(t *testing.T) {
	payload := sse(
		ssePart("message_start", `{"type":"message_start","message":{}}`),
		ssePart("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_x","name":"f"}}`),
		ssePart("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{oops"}}`),
		ssePart("content_block_stop", `{"type":"content_block_stop","index":0}`),
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	}))
	defer server.Close()
	runner := New(Options{BaseURL: server.URL})
	sink := &collectingSink{}
	err := runner.Run(t.Context(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, sink)
	if err == nil {
		t.Fatalf("expected RunError, got nil")
	}
	var re *provider.RunError
	if !errors.As(err, &re) {
		t.Fatalf("error = %T", err)
	}
	if re.Class != provider.ClassServer || re.Kind != provider.TerminalOmitted {
		t.Fatalf("class %d kind %d", re.Class, re.Kind)
	}
	if terminalEvents(sink.events) != 0 {
		t.Fatalf("terminal emitted before failure")
	}
}

func TestUpstreamSSEErrorEvent(t *testing.T) {
	payload := sse(
		ssePart("message_start", `{"type":"message_start","message":{}}`),
		ssePart("error", `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`),
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	}))
	defer server.Close()
	runner := New(Options{BaseURL: server.URL})
	err := runner.Run(t.Context(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, &collectingSink{})
	var re *provider.RunError
	if !errors.As(err, &re) {
		t.Fatalf("error = %T, want *provider.RunError", err)
	}
	if re.Class != provider.ClassServer {
		t.Fatalf("class = %d", re.Class)
	}
	if !strings.Contains(re.Unwrap().Error(), "busy") {
		t.Fatalf("cause = %v", re.Cause)
	}
}
