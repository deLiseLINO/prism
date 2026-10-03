package responses

import (
	"encoding/json"
	"testing"
	"time"

	"prism/internal/canon"
)

var namespaceRoutes = map[canon.ToolName]canon.ToolRoute{
	"mcp__x__child": {Namespace: "mcp__x", Name: "child"},
	"mcp__x__free":  {Namespace: "mcp__x", Name: "free"},
}

func namespaceTurn() []canon.Event {
	return []canon.Event{
		canon.ItemStarted{Item: canon.FunctionCall{ID: "fc_1", CallID: "call_1", Name: "mcp__x__child"}},
		canon.ToolArgumentsDelta{ItemID: "fc_1", Bytes: []byte(`{"a":1}`)},
		canon.ItemFinished{Item: canon.FunctionCall{ID: "fc_1", CallID: "call_1", Name: "mcp__x__child", Arguments: []byte(`{"a":1}`)}},
		canon.ItemStarted{Item: canon.CustomToolCall{ID: "ct_1", CallID: "call_2", Name: "mcp__x__free"}},
		canon.CustomToolInputDelta{ItemID: "ct_1", Text: "go"},
		canon.ItemFinished{Item: canon.CustomToolCall{ID: "ct_1", CallID: "call_2", Name: "mcp__x__free", Input: "go"}},
		canon.ItemStarted{Item: canon.FunctionCall{ID: "fc_2", CallID: "call_3", Name: "shell"}},
		canon.ItemFinished{Item: canon.FunctionCall{ID: "fc_2", CallID: "call_3", Name: "shell", Arguments: []byte(`{}`)}},
		canon.TurnFinished{Status: canon.Completed()},
	}
}

func feed(t *testing.T, e *Egress) {
	t.Helper()
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, ev := range namespaceTurn() {
		if err := e.Frame(ev); err != nil {
			t.Fatalf("frame %T: %v", ev, err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

func assertNamespaced(t *testing.T, items []any) {
	t.Helper()
	if len(items) != 3 {
		t.Fatalf("items = %v", items)
	}
	fn := items[0].(map[string]any)
	if fn["name"] != "child" || fn["namespace"] != "mcp__x" || fn["type"] != "function_call" {
		t.Fatalf("function item = %v", fn)
	}
	custom := items[1].(map[string]any)
	if custom["name"] != "free" || custom["namespace"] != "mcp__x" || custom["type"] != "custom_tool_call" {
		t.Fatalf("custom item = %v", custom)
	}
	plain := items[2].(map[string]any)
	if plain["name"] != "shell" {
		t.Fatalf("plain item = %v", plain)
	}
	if _, has := plain["namespace"]; has {
		t.Fatalf("plain item gained namespace: %v", plain)
	}
}

func TestStreamedNamespacedCallsCarryNamespace(t *testing.T) {
	b := &lockBuffer{}
	feed(t, NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)), namespaceRoutes))
	var added, done []any
	var completed []any
	for _, f := range eventFrames(t, b) {
		m := dataMap(t, f)
		switch f.event {
		case "response.output_item.added":
			added = append(added, m["item"])
		case "response.output_item.done":
			done = append(done, m["item"])
		case "response.completed":
			completed = m["response"].(map[string]any)["output"].([]any)
		}
	}
	assertNamespaced(t, added)
	assertNamespaced(t, done)
	assertNamespaced(t, completed)
}

func TestBufferedNamespacedCallsCarryNamespace(t *testing.T) {
	b := &lockBuffer{}
	feed(t, NewBufferedWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)), namespaceRoutes))
	var resp map[string]any
	if err := json.Unmarshal([]byte(b.String()), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertNamespaced(t, resp["output"].([]any))
}
