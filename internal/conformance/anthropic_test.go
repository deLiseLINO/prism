package conformance

import (
	"strings"
	"testing"

	"prism/internal/canon"
)

func mustLoadCases(t *testing.T) []Case {
	t.Helper()
	a, err := Load("../../cmd/prism-wirecheck/fixtures/protocol-v1-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	return a.Cases
}

func TestDecodeMessagesRequest(t *testing.T) {
	req, err := DecodeMessagesRequest([]byte(`{"model":"fixture-model","system":"SYS","messages":[{"role":"user","content":"PING"}],"max_tokens":32,"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "fixture-model" {
		t.Fatalf("model %s", req.Model)
	}
	if req.Stream {
		t.Fatal("stream must be false")
	}
	if req.MaxOutputTokens != 32 {
		t.Fatalf("max output tokens %d", req.MaxOutputTokens)
	}
	if len(req.Instructions) != 1 {
		t.Fatalf("instructions %d", len(req.Instructions))
	}
	ins, ok := req.Instructions[0].(canon.TextContent)
	if !ok || ins.Text != "SYS" {
		t.Fatalf("instructions %+v", req.Instructions)
	}
	if len(req.Input) != 1 {
		t.Fatalf("input %d", len(req.Input))
	}
	m, ok := req.Input[0].(canon.Message)
	if !ok || m.Role != canon.RoleUser || len(m.Content) != 1 {
		t.Fatalf("input message %+v", req.Input[0])
	}
	text, ok := m.Content[0].(canon.TextContent)
	if !ok || text.Text != "PING" {
		t.Fatalf("content %+v", m.Content)
	}
}

func TestDecodeMessagesToolRoundTrip(t *testing.T) {
	raw := `{"model":"fixture-model","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_fixture","name":"lookup","input":{"q":"x"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_fixture","content":"RESULT"}]}],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}],"max_tokens":32}`
	req, err := DecodeMessagesRequest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Input) != 2 {
		t.Fatalf("input %d", len(req.Input))
	}
	call, ok := req.Input[0].(canon.FunctionCall)
	if !ok || call.CallID != "call_fixture" || call.Name != "lookup" || string(call.Arguments) != `{"q":"x"}` {
		t.Fatalf("function call %+v", req.Input[0])
	}
	out, ok := req.Input[1].(canon.FunctionOutput)
	if !ok || out.CallID != "call_fixture" || len(out.Output) != 1 {
		t.Fatalf("function output %+v", req.Input[1])
	}
	text, ok := out.Output[0].(canon.TextContent)
	if !ok || text.Text != "RESULT" {
		t.Fatalf("output content %+v", out.Output)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("tools %d", len(req.Tools))
	}
	fn, ok := req.Tools[0].(canon.FunctionTool)
	if !ok || fn.Name != "lookup" || !strings.Contains(string(fn.Parameters), `"required"`) {
		t.Fatalf("tool %+v", req.Tools[0])
	}
}

func TestDecodeMessagesSystemArrayJoinsBlocks(t *testing.T) {
	req, err := DecodeMessagesRequest([]byte(`{"model":"m","system":[{"type":"text","text":"A"},{"type":"text","text":"B"}],"messages":[{"role":"user","content":"PING"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Instructions) != 1 {
		t.Fatalf("instructions %d", len(req.Instructions))
	}
	ins, ok := req.Instructions[0].(canon.TextContent)
	if !ok || ins.Text != "A\n\nB" {
		t.Fatalf("instructions %+v", req.Instructions)
	}
}

func TestDecodeMessagesRejectsMissingModel(t *testing.T) {
	if _, err := DecodeMessagesRequest([]byte(`{"messages":[{"role":"user","content":"PING"}]}`)); err == nil {
		t.Fatal("missing model must fail")
	}
}

func TestDecodeMessagesRejectsEmptyMessages(t *testing.T) {
	if _, err := DecodeMessagesRequest([]byte(`{"model":"m","messages":[]}`)); err == nil {
		t.Fatal("empty messages must fail")
	}
}

func TestDecodeMessagesRejectsUnknownRole(t *testing.T) {
	if _, err := DecodeMessagesRequest([]byte(`{"model":"m","messages":[{"role":"robot","content":"PING"}]}`)); err == nil {
		t.Fatal("unknown role must fail")
	}
}

func TestDecodeMessagesToolResultError(t *testing.T) {
	req, err := DecodeMessagesRequest([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"boom","is_error":true}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Input) != 1 {
		t.Fatalf("input %d", len(req.Input))
	}
	out, ok := req.Input[0].(canon.FunctionOutput)
	if !ok || len(out.Output) != 1 {
		t.Fatalf("function output %+v", req.Input[0])
	}
	text, ok := out.Output[0].(canon.TextContent)
	if !ok || text.Text != "[tool error] boom" {
		t.Fatalf("output %+v", out.Output)
	}
}

func contentSequenceFixture(t *testing.T) []byte {
	for _, c := range mustLoadCases(t) {
		if c.ID == "anthropic-core.protocol.content-sequence" {
			return []byte(c.Fixture.Bytes)
		}
	}
	t.Fatal("content-sequence case not found")
	return nil
}

func terminalErrorFixture(t *testing.T) []byte {
	for _, c := range mustLoadCases(t) {
		if c.ID == "anthropic-core.protocol.terminal-errors" {
			return []byte(c.Fixture.Bytes)
		}
	}
	t.Fatal("terminal-errors case not found")
	return nil
}

func TestTranslateResponsesEventsContentSequence(t *testing.T) {
	events, err := NormalizeSseBytes(contentSequenceFixture(t), "openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	translated := TranslateResponsesEvents(events)
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	got := make([]string, 0, len(translated))
	for _, ev := range translated {
		got = append(got, ev.Event)
	}
	if len(got) != len(want) {
		t.Fatalf("events %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v want %v", got, want)
		}
	}
	terminal := deriveTerminal(translated)
	if terminal == nil || *terminal != "message_stop" {
		t.Fatalf("terminal %v want message_stop", terminal)
	}
	delta := translated[2].Data.(map[string]any)
	if delta["type"] != "content_block_delta" {
		t.Fatalf("delta event %v", translated[2].Data)
	}
}

func TestTranslateResponsesEventsTerminalError(t *testing.T) {
	events, err := NormalizeSseBytes(terminalErrorFixture(t), "openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	translated := TranslateResponsesEvents(events)
	if len(translated) != 1 || translated[0].Event != "error" {
		t.Fatalf("events %+v want single error", translated)
	}
	terminal := deriveTerminal(translated)
	if terminal == nil || *terminal != "failed" {
		t.Fatalf("terminal %v want failed", terminal)
	}
	body := translated[0].Data.(map[string]any)
	errObj, _ := body["error"].(map[string]any)
	if errObj["type"] != "overloaded_error" || errObj["message"] != "fixture" {
		t.Fatalf("error body %v", translated[0].Data)
	}
}
