package codex

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeAssignedOrderAndFinalToolReceiptsHTTP(t *testing.T) {
	payload := joinFrames(
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"custom_tool_call","id":"custom","call_id":"c2","name":"write","input":""}}`,
		`{"type":"response.custom_tool_call_input.done","item_id":"custom","input":"raw\n9007199254740993"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"custom_tool_call","id":"custom","call_id":"c2","name":"write","input":""}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"function","call_id":"c1","name":"read","arguments":""}}`,
		`{"type":"response.function_call_arguments.done","item_id":"function","arguments":"{\"n\":9007199254740993}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"function","call_id":"c1","name":"read","arguments":""}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"function","call_id":"c1","name":"read","arguments":""},{"type":"custom_tool_call","id":"custom","call_id":"c2","name":"write","input":""}]}}`,
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
	defer up.Close()
	runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "fixture"}}, Client: up.Client()}
	sink := &captureSink{}
	if err := runner.Run(t.Context(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}}, sink); err != nil {
		t.Fatal(err)
	}
	var starts []canon.ItemID
	var function canon.FunctionCall
	var custom canon.CustomToolCall
	for _, ev := range sink.events {
		switch ev := ev.(type) {
		case canon.ItemStarted:
			starts = append(starts, outputItemID(ev.Item))
		case canon.ItemFinished:
			switch item := ev.Item.(type) {
			case canon.FunctionCall:
				function = item
			case canon.CustomToolCall:
				custom = item
			}
		}
	}
	if !reflect.DeepEqual(starts, []canon.ItemID{"function", "custom"}) || string(function.Arguments) != `{"n":9007199254740993}` || custom.Input != "raw\n9007199254740993" {
		t.Fatalf("starts=%v function=%v custom=%v", starts, function, custom)
	}
}

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
