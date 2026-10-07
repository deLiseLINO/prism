package customresponses

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/stream"
)

func TestTerminalSnapshotCompletesOpenItems(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.output_item.added","item":{"type":"message","id":"m","role":"assistant","content":[]}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"m","delta":"hello"}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"hello world"}]},{"type":"reasoning","id":"r","summary":[],"encrypted_content":"opaque-state"},{"type":"custom_tool_call","id":"c","call_id":"call-c","name":"patch","input":"raw\npatch"}],"usage":{"input_tokens":9007199254740993,"output_tokens":2,"total_tokens":9007199254740995}}}`+"\n\n")
	}))
	defer upstream.Close()
	sink := &collector{}
	var attempts []provider.NetworkAttempt
	err := New(nil, Options{}).Run(t.Context(), provider.RunRequest{Target: provider.Target{Provider: "edge", Wire: provider.WireResponses, BaseURL: upstream.URL}, Request: canon.Request{Model: "m", Stream: true}, AttemptObserver: func(attempt provider.NetworkAttempt) { attempts = append(attempts, attempt) }}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].StatusCode != 200 || attempts[0].StartedAt.IsZero() || attempts[0].FinishedAt.Before(attempts[0].StartedAt) {
		t.Fatalf("attempts=%+v", attempts)
	}
	tracker := stream.NewTracker()
	var items []canon.Item
	for _, event := range sink.All() {
		if err := tracker.Apply(event); err != nil {
			t.Fatalf("%T: %v", event, err)
		}
		if finished, ok := event.(canon.ItemFinished); ok {
			items = append(items, finished.Item)
		}
	}
	if len(items) != 3 || items[0].(canon.Message).Content[0].(canon.TextContent).Text != "hello world" || items[1].(canon.ReasoningItem).State != (canon.OpaqueRef{Store: canon.StoreWire, Key: "opaque-state"}) || items[2].(canon.CustomToolCall).Input != "raw\npatch" {
		t.Fatalf("items=%+v", items)
	}
	terminal := sink.All()[len(sink.All())-1].(canon.TurnFinished)
	if terminal.Usage.InputTokens != 9007199254740993 || terminal.Usage.TotalTokens != 9007199254740995 {
		t.Fatalf("usage=%+v", terminal.Usage)
	}
}

func TestMalformedCompletionNeverPublishesSuccess(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, payload := range []string{
			`{}`, `null`, `{"status":"in_progress","output":[]}`, `{"status":"completed","output":null}`,
			`{"status":"completed","output":[{"type":"message","id":"m","content":[{"type":"output_text","text":1}]}]}`,
			`{"status":"completed","output":[{"type":"function_call","id":"f","call_id":"c","name":"lookup","arguments":"{bad"}]}`,
			`{"status":"completed","output":[],"usage":{"input_tokens":-1}}`,
			`{"status":"completed","output":[],"usage":{"input_tokens":2,"input_tokens_details":{"cached_tokens":1.5}}}`,
			`{"status":"completed","output":[],"usage":{"input_tokens":null,"output_tokens":1}}`,
			`{"status":"completed","output":[],"usage":{"input_tokens":9223372036854775807,"output_tokens":1}}`,
			`{"status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":3}}`,
			`{"status":"completed","output":[],"usage":{"input_tokens":"bad","output_tokens":1}}`,
			`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"custom_tool_call","id":"f","call_id":"c","name":"patch","input":"partial","status":"in_progress"}]}`,
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", streaming, payload), func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						name := "response.completed"
						if strings.Contains(payload, `"incomplete_details"`) {
							name = "response.incomplete"
						}
						fmt.Fprintf(w, "data: {\"type\":%q,\"response\":%s}\n\n", name, payload)
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, payload)
					}
				}))
				defer upstream.Close()
				sink := &collector{}
				err := New(nil, Options{}).Run(context.Background(), provider.RunRequest{Target: provider.Target{Provider: "edge", Wire: provider.WireResponses, BaseURL: upstream.URL}, Request: canon.Request{Model: "m", Stream: streaming}}, sink)
				if err == nil {
					t.Fatalf("accepted malformed completion: %+v", sink.All())
				}
				for _, event := range sink.All() {
					switch event.(type) {
					case canon.ItemFinished, canon.TurnFinished:
						t.Fatalf("malformed completion published %T", event)
					}
				}
			})
		}
	}
}

func TestEmptyCustomInputReplaysAsPresentString(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Input []map[string]json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Input) != 2 || string(body.Input[0]["input"]) != `""` || string(body.Input[1]["output"]) != `""` {
			t.Errorf("input=%+v", body.Input)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"completed","output":[]}`)
	}))
	defer upstream.Close()
	err := New(nil, Options{}).Run(t.Context(), provider.RunRequest{Target: provider.Target{Provider: "edge", Wire: provider.WireResponses, BaseURL: upstream.URL}, Request: canon.Request{Model: "m", Input: []canon.Item{canon.CustomToolCall{CallID: "c", Name: "patch"}, canon.CustomToolOutput{CallID: "c"}}}}, &collector{})
	if err != nil {
		t.Fatal(err)
	}
}
