package codex

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeTerminalSparserThanClosedItemKeepsServedOutput(t *testing.T) {
	cases := map[string][]string{
		"message": {
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":0,"item_id":"m","delta":"hi"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"m","role":"assistant","content":[]}]}}`,
		},
		"function call": {
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"f","call_id":"c","name":"n","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"f","delta":"{}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"f","call_id":"c","name":"n","arguments":"{}"}}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"f","call_id":"c","name":"n","arguments":""}]}}`,
		},
		"reasoning": {
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"r","summary":[]}}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"r","delta":"think"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"r","summary":[{"type":"summary_text","text":"think"}],"encrypted_content":"x"}}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"r","summary":[],"encrypted_content":"x"}]}}`,
		},
	}
	for name, frames := range cases {
		t.Run(name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, joinFrames(frames...))
			}))
			defer up.Close()
			runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "fixture"}}, Client: up.Client()}
			sink := &captureSink{}
			if err := runner.Run(t.Context(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}}, sink); err != nil {
				t.Fatal(err)
			}
			var finished []canon.Item
			for _, event := range sink.events {
				if f, ok := event.(canon.ItemFinished); ok {
					finished = append(finished, f.Item)
				}
			}
			if len(finished) != 1 {
				t.Fatalf("finished=%+v", finished)
			}
			switch item := finished[0].(type) {
			case canon.Message:
				if item.Content[0].(canon.TextContent).Text != "hi" {
					t.Fatalf("message=%+v", item)
				}
			case canon.FunctionCall:
				if string(item.Arguments) != "{}" {
					t.Fatalf("call=%+v", item)
				}
			case canon.ReasoningItem:
				if len(item.Summary) != 1 || item.Summary[0].Text != "think" || item.State.Key != "x" {
					t.Fatalf("reasoning=%+v", item)
				}
			}
			if _, ok := sink.events[len(sink.events)-1].(canon.TurnFinished); !ok {
				t.Fatalf("last=%T", sink.events[len(sink.events)-1])
			}
		})
	}
}
