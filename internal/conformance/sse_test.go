package conformance

import (
	"strings"
	"testing"
)

func TestNormalizeSseBytesEventInference(t *testing.T) {
	raw := "data:{\"type\":\"response.output_text.delta\",\"delta\":\"A\"}\n\ndata: null\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"B\"}\n\ndata:{\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\"}}\n\n"
	events, err := NormalizeSseBytes([]byte(raw), "openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events %d want 3", len(events))
	}
	want := []string{"response.output_text.delta", "response.output_text.delta", "response.completed"}
	for i, name := range want {
		if events[i].Event != name {
			t.Fatalf("event %d %s want %s", i, events[i].Event, name)
		}
		if events[i].Ordinal != i {
			t.Fatalf("event %d ordinal %d", i, events[i].Ordinal)
		}
	}
	if text := deriveNormalizedText(events, nil); text != "AB" {
		t.Fatalf("normalized text %q want AB", text)
	}
	if terminal := deriveTerminal(events); terminal == nil || *terminal != "completed" {
		t.Fatalf("terminal %v want completed", terminal)
	}
}

func TestNormalizeSseBytesNamedEvents(t *testing.T) {
	raw := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_fixture\",\"type\":\"message\"}}\n\nevent: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_fixture\",\"type\":\"message\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"output\":[]}}\n\n"
	events, err := NormalizeSseBytes([]byte(raw), "openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events %d want 3", len(events))
	}
	for i, want := range []string{"response.output_item.added", "response.output_item.done", "response.completed"} {
		if events[i].Event != want {
			t.Fatalf("event %d %s want %s", i, events[i].Event, want)
		}
	}
	first := events[0].Data.(map[string]any)
	item := first["item"].(map[string]any)
	if item["id"] != "msg_fixture" {
		t.Fatalf("item id %v", item["id"])
	}
}

func TestNormalizeSseBytesMalformed(t *testing.T) {
	raw := "event: custom\ndata: not-json\n\n"
	events, err := NormalizeSseBytes([]byte(raw), "openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events %d want 1", len(events))
	}
	if events[0].Event != "custom" {
		t.Fatalf("event %s want custom", events[0].Event)
	}
	if events[0].Data != "not-json" {
		t.Fatalf("data %v", events[0].Data)
	}
	raw = "data: not-json\n\n"
	events, err = NormalizeSseBytes([]byte(raw), "openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != "malformed" {
		t.Fatalf("unnamed malformed frame %+v", events)
	}
}

func TestNormalizeSseBytesChatDone(t *testing.T) {
	raw := "data: [DONE]\n\n"
	events, err := NormalizeSseBytes([]byte(raw), "openai-chat")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != "[DONE]" {
		t.Fatalf("chat done %+v", events)
	}
	events, err = NormalizeSseBytes([]byte(raw), "openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != "malformed" {
		t.Fatalf("responses [DONE] must be malformed, got %+v", events)
	}
}

func TestNormalizeSseBytesCrlfAndBom(t *testing.T) {
	raw := "\ufeffdata: {\"type\":\"response.output_text.delta\",\"delta\":\"A\"}\r\n\r\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\r\n\r\n"
	events, err := NormalizeSseBytes([]byte(raw), "openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events %d want 2", len(events))
	}
}

func TestNormalizeSseBytesInvalidUTF8(t *testing.T) {
	if _, err := NormalizeSseBytes([]byte{0xff, 0xfe}, "openai-responses"); err == nil {
		t.Fatal("invalid utf-8 must error")
	}
}

func TestTerminalDerivation(t *testing.T) {
	cases := map[string]string{
		"error":               "failed",
		"response.failed":     "failed",
		"response.completed":  "completed",
		"message_stop":        "message_stop",
		"response.incomplete": "incomplete",
	}
	for event, want := range cases {
		events := []NormalizedEvent{{Event: event}}
		terminal := deriveTerminal(events)
		if terminal == nil || *terminal != want {
			t.Fatalf("%s terminal %v want %s", event, terminal, want)
		}
	}
	if terminal := deriveTerminal([]NormalizedEvent{{Event: "response.output_text.delta"}}); terminal != nil {
		t.Fatalf("delta must not be terminal, got %v", *terminal)
	}
}

func TestToolCallProjection(t *testing.T) {
	output := []any{
		map[string]any{
			"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{"q":"x"}`,
		},
		map[string]any{
			"type": "custom_tool_call", "call_id": "call_2", "name": "mcp__ns__tool", "input": "raw",
		},
	}
	proj := projectToolCallsDetailed(output)
	if proj.sawCallItems != true || proj.duplicateIds != false {
		t.Fatalf("flags %+v", proj)
	}
	if len(proj.calls) != 2 {
		t.Fatalf("calls %d", len(proj.calls))
	}
	args, ok := proj.calls[0].Arguments.(map[string]any)
	if !ok || args["q"] != "x" {
		t.Fatalf("function call arguments %v", proj.calls[0].Arguments)
	}
	if proj.calls[1].Kind != "custom" || proj.calls[1].Arguments != "raw" {
		t.Fatalf("custom call %+v", proj.calls[1])
	}
	mcp := projectMcpCalls(proj.calls)
	if len(mcp) != 1 || mcp[0].Namespace != "mcp__ns" || mcp[0].Name != "tool" {
		t.Fatalf("mcp calls %+v", mcp)
	}
}

func TestToolCallProjectionDuplicates(t *testing.T) {
	output := []any{
		map[string]any{"type": "function_call", "call_id": "call_1", "name": "a", "arguments": "{}"},
		map[string]any{"type": "function_call", "call_id": "call_1", "name": "b", "arguments": "{}"},
	}
	proj := projectToolCallsDetailed(output)
	if !proj.duplicateIds || len(proj.calls) != 0 {
		t.Fatalf("duplicates must empty calls: %+v", proj)
	}
}

func TestToolCallProjectionFromEvents(t *testing.T) {
	events := []NormalizedEvent{
		{Event: "response.output_item.done", Data: map[string]any{
			"item": map[string]any{"type": "function_call", "call_id": "call_1", "name": "a", "arguments": "{}"},
		}},
		{Event: "response.output_item.done", Data: map[string]any{
			"item": map[string]any{"type": "message", "id": "msg_1"},
		}},
	}
	proj := projectToolCallsFromEvents(events)
	if len(proj.calls) != 1 || proj.calls[0].ID != "call_1" {
		t.Fatalf("projection %+v", proj.calls)
	}
}

func TestFinalizeObservationJsonTextWins(t *testing.T) {
	json := map[string]any{
		"status": "completed",
		"output": []any{
			map[string]any{
				"type": "message", "role": "assistant", "content": []any{
					map[string]any{"type": "output_text", "text": "OK"},
				},
			},
		},
	}
	events := []NormalizedEvent{
		{Event: "response.output_text.delta", Data: map[string]any{"delta": "OK"}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{"status": "completed"}}},
	}
	obs := Empty()
	FinalizeObservation(obs, events, json, 200)
	if obs.Client.Response.NormalizedText != "OK" {
		t.Fatalf("text %q", obs.Client.Response.NormalizedText)
	}
	if obs.Client.Response.Terminal == nil || *obs.Client.Response.Terminal != "completed" {
		t.Fatalf("terminal %v", obs.Client.Response.Terminal)
	}
	if len(obs.Client.Response.Events) != 2 {
		t.Fatalf("events %d", len(obs.Client.Response.Events))
	}
	if v, ok := obs.Verifiers["duplicate_tool_call_ids"]; !ok || v != false {
		t.Fatalf("duplicate verifier %v", obs.Verifiers["duplicate_tool_call_ids"])
	}
}

func TestEvaluateJsonSseEquivalence(t *testing.T) {
	c := Case{ID: "responses-core.protocol.json-sse-equivalence", Fixture: Fixture{Bytes: `{"json":{"id":"resp_fixture","status":"completed","output":[{"id":"msg_fixture","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK"}]}]},"sse":"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\"}}\n\n"}`}}
	if got := evaluateJsonSseEquivalence(c); got != "pass" {
		t.Fatalf("equivalence %s want pass", got)
	}
	c.Fixture.Bytes = strings.Replace(c.Fixture.Bytes, `"OK"`, `"NO"`, 1)
	if got := evaluateJsonSseEquivalence(c); got != "fail" {
		t.Fatalf("mismatch must fail, got %s", got)
	}
}

func TestResponsesURL(t *testing.T) {
	cases := map[string]string{
		"https://api.openai.com/v1":           "https://api.openai.com/v1/responses",
		"https://api.openai.com/v1/":          "https://api.openai.com/v1/responses",
		"https://api.openai.com/v1/responses": "https://api.openai.com/v1/responses",
		"http://127.0.0.1:1/v1":               "http://127.0.0.1:1/v1/responses",
	}
	for in, want := range cases {
		if got := ResponsesURL(in); got != want {
			t.Fatalf("ResponsesURL(%q) = %s want %s", in, got, want)
		}
	}
}
