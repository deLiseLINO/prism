package anthropic

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeJSONFallbackPreservesExactUsageAndArguments(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"message","id":"m","content":[{"type":"tool_use","id":"call","name":"read","input":{"n":9007199254740993,"x":1e-12}}],"stop_reason":"tool_use","usage":{"input_tokens":9.007199254740993e15,"output_tokens":2,"cache_read_input_tokens":null}}`)
	}))
	defer up.Close()
	sink := &collectingSink{}
	err := New(Options{HTTP: up.Client()}).Run(t.Context(), provider.RunRequest{Request: baseRequest(), Target: provider.Target{BaseURL: up.URL}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	var call canon.FunctionCall
	var usage canon.Usage
	for _, ev := range sink.events {
		switch ev := ev.(type) {
		case canon.ItemFinished:
			if item, ok := ev.Item.(canon.FunctionCall); ok {
				call = item
			}
		case canon.TurnFinished:
			usage = ev.Usage
		}
	}
	if string(call.Arguments) != `{"n":9007199254740993,"x":1e-12}` || usage.InputTokens != 9007199254740993 || usage.TotalTokens != 9007199254740995 {
		t.Fatalf("call=%v usage=%v", call, usage)
	}
}

func TestNativeMalformedAndTruncatedResponsesFail(t *testing.T) {
	for _, body := range []string{
		`{"type":"message","content":[],"usage":{"input_tokens":1}}`,
		`{"type":"message","content":{},"stop_reason":"end_turn"}`,
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n",
		"event: message_stop\ndata: invalid\n\n",
	} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if body[0] == '{' {
				w.Header().Set("Content-Type", "application/json")
			}
			io.WriteString(w, body)
		}))
		sink := &collectingSink{}
		err := New(Options{HTTP: up.Client()}).Run(t.Context(), provider.RunRequest{Request: baseRequest(), Target: provider.Target{BaseURL: up.URL}}, sink)
		up.Close()
		var re *provider.RunError
		if !errors.As(err, &re) {
			t.Fatalf("malformed response succeeded: %q", body)
		}
		for _, ev := range sink.events {
			if _, ok := ev.(canon.TurnFinished); ok {
				t.Fatalf("malformed response completed: %q", body)
			}
		}
	}
}
