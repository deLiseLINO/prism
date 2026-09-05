package codex

import (
	"strings"
	"testing"

	"prism/internal/canon"
)

func decodeStream(t *testing.T, sse string) ([]canon.Event, *Decoder) {
	t.Helper()
	var events []canon.Event
	dec := NewDecoder(func(ev canon.Event) error {
		events = append(events, ev)
		return nil
	})
	if err := dec.Decode(strings.NewReader(sse)); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return events, dec
}

func TestSSEVocabularyToCanonEvents(t *testing.T) {
	sse := joinFrames(
		`{"type":"response.created","response":{"id":"r-1"}}`,
		`{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"think"}]}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs-1","delta":"think"}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"think"}]}}`,
		`{"type":"response.output_item.added","item":{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":""}]}}`,
		`{"type":"response.output_text.delta","item_id":"msg-1","delta":"Hello "}`,
		`{"type":"response.output_text.delta","item_id":"msg-1","delta":"world"}`,
		`{"type":"response.output_item.done","item":{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}}`,
		`{"type":"response.completed","response":{"id":"r-1","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7,"input_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":3}}}}`,
	)
	events, dec := decodeStream(t, sse)
	if !dec.Done() {
		t.Fatalf("decoder not done after completed")
	}
	if len(events) != 8 {
		t.Fatalf("events = %d, want 8\n%#v", len(events), events)
	}
	if _, ok := events[0].(canon.ItemStarted); !ok {
		t.Fatalf("event[0] = %T, want ItemStarted", events[0])
	}
	delta := events[1].(canon.ReasoningDelta)
	if delta.ItemID != "rs-1" || delta.Text != "think" {
		t.Fatalf("reasoning delta = %+v", delta)
	}
	text := events[4].(canon.TextDelta)
	if text.ItemID != "msg-1" || text.Text != "Hello " {
		t.Fatalf("text delta = %+v", text)
	}
	finished := events[6].(canon.ItemFinished)
	msg, ok := finished.Item.(canon.Message)
	if !ok || msg.ID != "msg-1" {
		t.Fatalf("item finished = %#v", finished.Item)
	}
	terminal := events[7].(canon.TurnFinished)
	if terminal.Status.Kind() != canon.StatusCompleted {
		t.Fatalf("status = %v", terminal.Status.Kind())
	}
	wantUsage := canon.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7, CachedInputTokens: 1, ReasoningTokens: 3}
	if terminal.Usage != wantUsage {
		t.Fatalf("usage = %+v want %+v", terminal.Usage, wantUsage)
	}
}

func TestSSEFunctionCallAndCustomToolDeltas(t *testing.T) {
	sse := joinFrames(
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc-1","call_id":"c1","name":"lookup","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc-1","delta":"{\"q\":"}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc-1","call_id":"c1","name":"lookup","arguments":"{\"q\":1}"}}`,
		`{"type":"response.output_item.added","item":{"type":"custom_tool_call","id":"ctc-1","call_id":"c2","name":"web","input":""}}`,
		`{"type":"response.custom_tool_call_input.delta","item_id":"ctc-1","delta":"query"}`,
		`{"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"ctc-1","call_id":"c2","name":"web","input":"query"}}`,
		`{"type":"response.completed","response":{"id":"r-1"}}`,
	)
	events, dec := decodeStream(t, sse)
	if !dec.Done() {
		t.Fatalf("decoder not done")
	}
	argsDelta := events[1].(canon.ToolArgumentsDelta)
	if argsDelta.ItemID != "fc-1" || string(argsDelta.Bytes) != `{"q":` {
		t.Fatalf("arguments delta = %+v", argsDelta)
	}
	ctc := events[4].(canon.CustomToolInputDelta)
	if ctc.ItemID != "ctc-1" || ctc.Text != "query" {
		t.Fatalf("custom input delta = %+v", ctc)
	}
	fc := events[2].(canon.ItemFinished).Item.(canon.FunctionCall)
	if fc.Name != "lookup" || string(fc.Arguments) != `{"q":1}` {
		t.Fatalf("function call = %+v", fc)
	}
}

func TestSSEIncompleteTerminal(t *testing.T) {
	sse := joinFrames(
		`{"type":"response.incomplete","response":{"id":"r-1","incomplete_details":{"reason":"max_output_tokens"}}}`,
	)
	events, dec := decodeStream(t, sse)
	if !dec.Done() {
		t.Fatalf("decoder not done")
	}
	terminal := events[0].(canon.TurnFinished)
	if terminal.Status.Kind() != canon.StatusIncomplete {
		t.Fatalf("status kind = %v", terminal.Status.Kind())
	}
	reason, ok := terminal.Status.Reason()
	if !ok || reason != canon.IncompleteMaxOutputTokens {
		t.Fatalf("reason = %v ok = %v", reason, ok)
	}
}

func TestSSEFailedTerminal(t *testing.T) {
	sse := joinFrames(
		`{"type":"response.failed","response":{"error":{"message":"boom"}}}`,
	)
	events, dec := decodeStream(t, sse)
	if !dec.Done() {
		t.Fatalf("decoder not done")
	}
	failed := events[0].(canon.TurnFailed)
	if failed.Failure.Message != "boom" {
		t.Fatalf("failure message = %q", failed.Failure.Message)
	}
}

func TestSSEPrismR1EnvelopeContinuation(t *testing.T) {
	envelope := prismReasoningPrefix + "eyJzaWciOiJzaWctMSIsInR4dCI6ImhpZGRlbiJ9"
	sse := joinFrames(
		`{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"s"}]}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs-1","encrypted_content":"`+envelope+`"}}`,
		`{"type":"response.completed","response":{"id":"r-1"}}`,
	)
	events, dec := decodeStream(t, sse)
	if !dec.Done() {
		t.Fatalf("decoder not done")
	}
	state := events[1].(canon.ItemStateAvailable)
	if state.ItemID != "rs-1" {
		t.Fatalf("state item = %q", state.ItemID)
	}
	if state.State.Store != reasoningStorePRISMR1 {
		t.Fatalf("state store = %q want prismr1", state.State.Store)
	}
	if !strings.Contains(state.State.Key, `"sig":"sig-1"`) || !strings.Contains(state.State.Key, `"txt":"hidden"`) {
		t.Fatalf("envelope payload = %q", state.State.Key)
	}
	finished := events[2].(canon.ItemFinished)
	item := finished.Item.(canon.ReasoningItem)
	if item.State.Store != reasoningStorePRISMR1 {
		t.Fatalf("finished item state = %+v", item.State)
	}
}

func TestSSEDuplicateTerminalWarns(t *testing.T) {
	sse := joinFrames(
		`{"type":"response.completed","response":{"id":"r-1"}}`,
		`{"type":"response.completed","response":{"id":"r-2"}}`,
	)
	_, dec := decodeStream(t, sse)
	if !containsString(dec.Warnings(), "duplicate_terminal") {
		t.Fatalf("warnings = %v", dec.Warnings())
	}
}

func TestSSEUnknownTypeWarnsAndContinues(t *testing.T) {
	sse := joinFrames(
		`{"type":"response.custom_tool_call_output.delta","item_id":"x","delta":"y"}`,
		`{"type":"response.completed","response":{"id":"r-1"}}`,
	)
	events, dec := decodeStream(t, sse)
	if !dec.Done() {
		t.Fatalf("decoder not done")
	}
	if !containsString(dec.Warnings(), "unknown_sse_type:response.custom_tool_call_output.delta") {
		t.Fatalf("warnings = %v", dec.Warnings())
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}

func TestSSEMalformedPayloadWarns(t *testing.T) {
	sse := joinFrames(`{"type":"response.completed","response":{"id":"r-1"}}`, `not-json`)
	_, dec := decodeStream(t, sse)
	if !containsString(dec.Warnings(), "malformed_sse_payload") {
		t.Fatalf("warnings = %v", dec.Warnings())
	}
}

func TestSSECRLFFraming(t *testing.T) {
	sse := "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"delta\":\"x\"}\r\n\r\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\r\n\r\n"
	events, dec := decodeStream(t, sse)
	if !dec.Done() {
		t.Fatalf("decoder not done")
	}
	if len(events) != 2 {
		t.Fatalf("events = %d", len(events))
	}
}

func TestSSEBareErrorFrameKeepsMessage(t *testing.T) {
	sse := joinFrames(`{"type":"error","code":"server_error","message":"upstream exploded"}`)
	events, dec := decodeStream(t, sse)
	if !dec.Done() {
		t.Fatalf("decoder not done")
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	failed, ok := events[0].(canon.TurnFailed)
	if !ok {
		t.Fatalf("event = %T, want canon.TurnFailed", events[0])
	}
	if failed.Failure.Message != "upstream exploded" {
		t.Fatalf("message = %q, want upstream text", failed.Failure.Message)
	}
}

func joinFrames(frames ...string) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("data: ")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	return b.String()
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
