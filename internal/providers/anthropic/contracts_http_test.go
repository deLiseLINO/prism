package anthropic

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeUsageRetainsLastValidObservation(t *testing.T) {
	for _, invalid := range []string{`-1`, `1.5`, `"2"`, `9223372036854775808`, `null`} {
		t.Run(invalid, func(t *testing.T) {
			payload := ssePart("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}`) + ssePart("message_delta", `{"type":"message_delta","usage":{"output_tokens":5}}`) + ssePart("message_delta", `{"type":"message_delta","usage":{"output_tokens":`+invalid+`}}`) + ssePart("message_stop", `{"type":"message_stop"}`)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
			defer up.Close()
			sink := &collectingSink{}
			err := New(Options{HTTP: up.Client()}).Run(t.Context(), provider.RunRequest{Request: baseRequest(), Target: provider.Target{BaseURL: up.URL}}, sink)
			var re *provider.RunError
			if !errors.As(err, &re) || re.Kind != provider.TerminalEmitted || re.Class != provider.ClassTransport {
				t.Fatalf("error = %v", err)
			}
			failure, ok := sink.events[len(sink.events)-1].(canon.TurnFailed)
			want := canon.Usage{InputTokens: 9, OutputTokens: 5, CachedInputTokens: 3, CacheWriteInputTokens: 4, TotalTokens: 14}
			if !ok || failure.Usage != want || terminalEvents(sink.events) != 1 {
				t.Fatalf("events = %v", sink.events)
			}
		})
	}
	t.Run("nullable cache and input delta", func(t *testing.T) {
		payload := ssePart("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":9.007199254740993e15,"cache_read_input_tokens":null,"cache_creation_input_tokens":null}}}`) + ssePart("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":null,"output_tokens":2,"cache_read_input_tokens":null,"cache_creation_input_tokens":null}}`) + ssePart("message_stop", `{"type":"message_stop"}`)
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
		defer up.Close()
		sink := &collectingSink{}
		err := New(Options{HTTP: up.Client()}).Run(t.Context(), provider.RunRequest{Request: baseRequest(), Target: provider.Target{BaseURL: up.URL}}, sink)
		terminal, ok := sink.events[len(sink.events)-1].(canon.TurnFinished)
		if err != nil || !ok || terminal.Usage != (canon.Usage{InputTokens: 9007199254740993, OutputTokens: 2, TotalTokens: 9007199254740995}) || terminalEvents(sink.events) != 1 {
			t.Fatalf("error=%v events=%v", err, sink.events)
		}
	})
}

func TestNativeStructuredSchemaAndForcedThinkingHTTP(t *testing.T) {
	const schema = `{"type":"object","properties":{"count":{"minimum":9007199254740993,"multipleOf":1e-12}},"required":["count"],"additionalProperties":false}`
	captured := make(chan []byte, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured <- raw
		io.WriteString(w, fullStreamPayload())
	}))
	defer up.Close()
	req := baseRequest()
	req.Tools = []canon.Tool{canon.FunctionTool{Name: "read", Parameters: []byte(schema)}}
	req.ToolChoice = canon.ToolNamed{Name: "read"}
	req.Reasoning.Effort = canon.EffortHigh
	req.Text.Format = &canon.TextFormat{Type: "json_schema", Schema: []byte(schema)}
	err := New(Options{HTTP: up.Client()}).Run(t.Context(), provider.RunRequest{Request: req, Target: provider.Target{BaseURL: up.URL}}, &collectingSink{})
	if err != nil {
		t.Fatal(err)
	}
	body := string(<-captured)
	if strings.Contains(body, `"thinking"`) || !strings.Contains(body, `"output_config":{"format":{"type":"json_schema","schema":`+schema) || !strings.Contains(body, `9007199254740993`) || !strings.Contains(body, `1e-12`) || !strings.Contains(body, `"tool_choice":{"type":"tool","name":"read"}`) {
		t.Fatalf("request = %s", body)
	}
}
