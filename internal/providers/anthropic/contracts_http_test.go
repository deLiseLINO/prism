package anthropic

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeTerminalReturnsBeforeHTTPBodyEOF(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "failed"}[failed], func(t *testing.T) {
			release, closed := make(chan struct{}), make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload := fullStreamPayload()
				if failed {
					payload = strings.TrimSuffix(payload, ssePart("message_stop", `{"type":"message_stop"}`)) + ssePart("error", `{"type":"error","error":{"type":"permission_error","message":"not allowed"}}`)
				}
				io.WriteString(w, payload)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(closed)
				case <-release:
				}
			}))
			defer up.Close()
			defer close(release)
			sink := &collectingSink{}
			done := make(chan error, 1)
			go func() {
				done <- New(Options{HTTP: up.Client()}).Run(t.Context(), provider.RunRequest{Request: baseRequest(), Target: provider.Target{BaseURL: up.URL}}, sink)
			}()
			select {
			case err := <-done:
				if failed {
					var re *provider.RunError
					if !errors.As(err, &re) || re.Kind != provider.TerminalEmitted || re.Class != provider.ClassForbidden || re.Reported == nil {
						t.Fatalf("failure = %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("terminal waits for HTTP EOF")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("body not closed")
			}
			if terminalEvents(sink.events) != 1 {
				t.Fatalf("events = %v", sink.events)
			}
		})
	}
}

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
