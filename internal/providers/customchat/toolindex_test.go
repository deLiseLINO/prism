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

func finishedCalls(t *testing.T, sse string) []canon.FunctionCall {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()
	c := &collector{}
	if err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, c); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var calls []canon.FunctionCall
	for _, ev := range c.events {
		if f, ok := ev.(canon.ItemFinished); ok {
			if fc, ok := f.Item.(canon.FunctionCall); ok {
				calls = append(calls, fc)
			}
		}
	}
	return calls
}

func frames(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: " + c + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

const finishToolCalls = `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`

func TestStreamToolCallsWithoutIndex(t *testing.T) {
	t.Run("single call split across chunks", func(t *testing.T) {
		calls := finishedCalls(t, frames(
			`{"choices":[{"delta":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"1}"}}]}}]}`,
			finishToolCalls))
		if len(calls) != 1 || string(calls[0].Arguments) != `{"a":1}` || calls[0].Name != "f" {
			t.Fatalf("calls = %+v", calls)
		}
	})
	t.Run("parallel calls each with a distinct id", func(t *testing.T) {
		calls := finishedCalls(t, frames(
			`{"choices":[{"delta":{"tool_calls":[{"id":"c1","function":{"name":"f","arguments":"{\"a\":1}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"c2","function":{"name":"g","arguments":"{\"b\":2}"}}]}}]}`,
			finishToolCalls))
		if len(calls) != 2 || calls[0].CallID != "c1" || calls[1].CallID != "c2" || string(calls[1].Arguments) != `{"b":2}` {
			t.Fatalf("calls = %+v", calls)
		}
	})
	t.Run("same index reused for a second call with a new id", func(t *testing.T) {
		calls := finishedCalls(t, frames(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c2","function":{"name":"g","arguments":"{}"}}]}}]}`,
			finishToolCalls))
		if len(calls) != 2 || calls[0].Name != "f" || calls[1].Name != "g" {
			t.Fatalf("calls = %+v", calls)
		}
	})
	t.Run("late identity without index", func(t *testing.T) {
		calls := finishedCalls(t, frames(
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{\"a\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"late","function":{"name":"lookup","arguments":"1}"}}]}}]}`,
			finishToolCalls))
		if len(calls) != 1 || calls[0].CallID != "late" || calls[0].Name != "lookup" || string(calls[0].Arguments) != `{"a":1}` {
			t.Fatalf("calls=%+v", calls)
		}
	})
}
