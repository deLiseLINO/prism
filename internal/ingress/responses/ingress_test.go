package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/execution"
)

type warningSink struct {
	warnings []Warning
}

func (s *warningSink) ingest(w Warning) {
	s.warnings = append(s.warnings, w)
}

func newTestIngress() (*Ingress, *warningSink) {
	sink := &warningSink{}
	return New(sink.ingest), sink
}

func parseBody(t *testing.T, body string, headers map[string]string) (canon.Request, execution.Facts, *warningSink, error) {
	t.Helper()
	g, sink := newTestIngress()
	hr, err := http.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	hr.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		hr.Header.Set(k, v)
	}
	req, facts, err := g.Parse(context.Background(), hr)
	return req, facts, sink, err
}

func mustParse(t *testing.T, body string, headers map[string]string) (canon.Request, execution.Facts, *warningSink) {
	t.Helper()
	req, facts, sink, err := parseBody(t, body, headers)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return req, facts, sink
}

func TestParseStandardItems(t *testing.T) {
	body := `{
		"model": "gpt-5.3",
		"stream": true,
		"instructions": "be brief",
		"max_output_tokens": 512,
		"temperature": 0.4,
		"top_p": 0.9,
		"stop": ["END"],
		"parallel_tool_calls": false,
		"service_tier": "flex",
		"reasoning": {"effort": "high", "summary": "concise"},
		"input": [
			{"type": "message", "id": "m1", "role": "user", "content": "hello"},
			{"type": "message", "id": "m2", "role": "assistant", "content": [
				{"type": "output_text", "text": "hi there"}
			]},
			{"type": "reasoning", "id": "r1", "summary": [{"type": "summary_text", "text": "thinking"}], "signature": "sig1"},
			{"type": "function_call", "id": "fc1", "call_id": "call_1", "name": "get", "arguments": "{\"n\":3}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "42"},
			{"type": "custom_tool_call", "id": "ct1", "call_id": "call_2", "name": "patch", "input": "*** Begin Patch"},
			{"type": "custom_tool_call_output", "call_id": "call_2", "output": "done"}
		]
	}`
	req, _, _ := mustParse(t, body, nil)
	if req.Model != "gpt-5.3" || !req.Stream || req.MaxOutputTokens != 512 {
		t.Fatalf("request head: %+v", req)
	}
	if req.Sampling.Temperature == nil || *req.Sampling.Temperature != 0.4 {
		t.Fatalf("temperature: %+v", req.Sampling)
	}
	if req.Sampling.ParallelToolCalls == nil || *req.Sampling.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls: %+v", req.Sampling)
	}
	if req.Sampling.ServiceTier != canon.TierFlex {
		t.Fatalf("service tier: %+v", req.Sampling)
	}
	if req.Reasoning.Effort != canon.EffortHigh || req.Reasoning.Summary != canon.SummaryConcise {
		t.Fatalf("reasoning config: %+v", req.Reasoning)
	}
	if len(req.Instructions) != 1 || req.Instructions[0] != (canon.TextContent{Text: "be brief"}) {
		t.Fatalf("instructions: %+v", req.Instructions)
	}
	if len(req.Input) != 7 {
		t.Fatalf("input count: %d", len(req.Input))
	}
	msg0, ok := req.Input[0].(canon.Message)
	if !ok || msg0.Role != canon.RoleUser || len(msg0.Content) != 1 || msg0.Content[0] != (canon.TextContent{Text: "hello"}) || msg0.ID != "m1" {
		t.Fatalf("input[0]: %#v", req.Input[0])
	}
	msg1, ok := req.Input[1].(canon.Message)
	if !ok || msg1.Role != canon.RoleAssistant || len(msg1.Content) != 1 || msg1.Content[0] != (canon.TextContent{Text: "hi there"}) {
		t.Fatalf("input[1]: %#v", req.Input[1])
	}
	reasoning, ok := req.Input[2].(canon.ReasoningItem)
	if !ok || reasoning.ID != "r1" || reasoning.Signature != "sig1" || reasoning.Content != "thinking" || len(reasoning.Summary) != 1 || reasoning.Summary[0].Text != "thinking" {
		t.Fatalf("input[2]: %#v", req.Input[2])
	}
	call, ok := req.Input[3].(canon.FunctionCall)
	if !ok || call.CallID != "call_1" || call.Name != "get" || string(call.Arguments) != `{"n":3}` {
		t.Fatalf("input[3]: %#v", req.Input[3])
	}
	out, ok := req.Input[4].(canon.FunctionOutput)
	if !ok || out.CallID != "call_1" || len(out.Output) != 1 || out.Output[0] != (canon.TextContent{Text: "42"}) {
		t.Fatalf("input[4]: %#v", req.Input[4])
	}
	custom, ok := req.Input[5].(canon.CustomToolCall)
	if !ok || custom.CallID != "call_2" || custom.Name != "patch" || custom.Input != "*** Begin Patch" {
		t.Fatalf("input[5]: %#v", req.Input[5])
	}
	customOut, ok := req.Input[6].(canon.CustomToolOutput)
	if !ok || customOut.CallID != "call_2" || customOut.Output != "done" {
		t.Fatalf("input[6]: %#v", req.Input[6])
	}
}

func TestParseExoticItems(t *testing.T) {
	body := `{
		"model": "gpt-5.3",
		"input": [
			{"type": "local_shell_call", "id": "ls1", "call_id": "call_ls", "action": {"command": ["ls", "-la"]}},
			{"type": "tool_search_call", "id": "ts1", "call_id": "call_ts", "query": "deploy tools"},
			{"type": "tool_search_output", "call_id": "call_ts", "tools": [{"name": "deploy", "description": "deploys"}]},
			{"type": "compaction_trigger"},
			{"type": "context_compaction", "encrypted_content": "enc-blob"},
			{"type": "compaction_summary", "encrypted_content": "enc-blob-2"},
			{"type": "compaction"},
			{"type": "additional_tools", "tools": [{"type": "image_generation"}]},
			{"type": "brand_new_future_item", "x": 1}
		]
	}`
	req, _, sink := mustParse(t, body, nil)
	if len(req.Input) != 5 {
		t.Fatalf("input count: %d", len(req.Input))
	}
	shell, ok := req.Input[0].(canon.LocalShellCall)
	if !ok || shell.ID != "ls1" || shell.CallID != "call_ls" || shell.Command != "ls -la" {
		t.Fatalf("input[0]: %#v", req.Input[0])
	}
	searchCall, ok := req.Input[1].(canon.ToolSearchCall)
	if !ok || searchCall.Query != "deploy tools" || searchCall.CallID != "call_ts" {
		t.Fatalf("input[1]: %#v", req.Input[1])
	}
	searchOut, ok := req.Input[2].(canon.ToolSearchOutput)
	if !ok || len(searchOut.Results) != 1 || searchOut.Results[0].ToolName != "deploy" || searchOut.Results[0].Summary != "deploys" {
		t.Fatalf("input[2]: %#v", req.Input[2])
	}
	for i, item := range req.Input[3:] {
		msg, ok := item.(canon.Message)
		if !ok || msg.Role != canon.RoleUser || len(msg.Content) != 1 {
			t.Fatalf("input[%d]: %#v", i+3, item)
		}
		text, ok := msg.Content[0].(canon.TextContent)
		if !ok || text.Text != opaqueCompactionNote {
			t.Fatalf("input[%d] content: %#v", i+3, msg.Content[0])
		}
		if msg.ID != "" {
			t.Fatalf("input[%d] synthesized id %q", i+3, msg.ID)
		}
	}
	kinds := map[string]int{}
	for _, w := range sink.warnings {
		kinds[w.Kind]++
	}
	if kinds[WarnOpaquePayload] != 2 {
		t.Fatalf("opaque payload warnings: %+v", sink.warnings)
	}
	if kinds[WarnExoticItem] != 1 {
		t.Fatalf("exotic item warning missing: %+v", sink.warnings)
	}
	if kinds[WarnUnknownItem] != 1 {
		t.Fatalf("unknown item warning missing: %+v", sink.warnings)
	}
}

func TestForwardHeadersBuildFacts(t *testing.T) {
	headers := map[string]string{
		"session_id":               "sess_94a02f",
		"thread-id":                "thread_c091",
		"x-codex-parent-thread-id": "thread_parent_81b",
		"originator":               "codex_cli_rs",
		"user-agent":               "codex_cli_rs/1.0",
		"authorization":            "Bearer sk-secret",
		"x-api-key":                "sk-key",
		"cookie":                   "session=abc",
	}
	req, facts, _ := mustParse(t, `{"model": "gpt-5.3", "input": "hi"}`, headers)
	if facts.Client != execution.ClientCodex {
		t.Fatalf("client: %v", facts.Client)
	}
	if facts.Session != execution.SessionKey("sess_94a02f") {
		t.Fatalf("session: %v", facts.Session)
	}
	if facts.Thread != execution.ThreadKey("thread_c091") {
		t.Fatalf("thread: %v", facts.Thread)
	}
	if v, ok := facts.Forward.Get(execution.ForwardSessionID); !ok || v != "sess_94a02f" {
		t.Fatalf("forward session: %q %v", v, ok)
	}
	if v, ok := facts.Forward.Get(execution.ForwardOriginator); !ok || v != "codex_cli_rs" {
		t.Fatalf("forward originator: %q %v", v, ok)
	}
	if v, ok := facts.Forward.Get(execution.ForwardUserAgent); !ok || v != "codex_cli_rs/1.0" {
		t.Fatalf("forward user-agent: %q %v", v, ok)
	}
	redacted := facts.Forward.Redacted()
	if strings.Contains(redacted, "sk-secret") || strings.Contains(redacted, "sk-key") || strings.Contains(redacted, "session=abc") || strings.Contains(redacted, "Bearer") {
		t.Fatalf("redacted leaks credentials: %q", redacted)
	}
	if !strings.Contains(redacted, "x-session-id=<redacted>") {
		t.Fatalf("redacted: %q", redacted)
	}
	if req.Model != "gpt-5.3" {
		t.Fatalf("model: %v", req.Model)
	}
}

func TestParentThreadFallback(t *testing.T) {
	_, facts, _ := mustParse(t, `{"model": "gpt-5.3", "input": "hi"}`, map[string]string{
		"x-codex-parent-thread-id": "thread_parent_81b",
	})
	if facts.Thread != execution.ThreadKey("thread_parent_81b") {
		t.Fatalf("thread: %v", facts.Thread)
	}
}

func TestClientClassification(t *testing.T) {
	cases := []struct {
		headers map[string]string
		want    execution.Client
	}{
		{map[string]string{"x-prism-grok": "1"}, execution.ClientGrok},
		{map[string]string{"originator": "codex_exec"}, execution.ClientCodex},
		{map[string]string{"originator": "codex_cli_rs"}, execution.ClientCodex},
		{nil, execution.ClientOMP},
	}
	for _, tc := range cases {
		_, facts, _ := mustParse(t, `{"model": "gpt-5.3", "input": "hi"}`, tc.headers)
		if facts.Client != tc.want {
			t.Fatalf("headers %v: client %v, want %v", tc.headers, facts.Client, tc.want)
		}
	}
}

func TestReasoningEffortMaxParses(t *testing.T) {
	req, _, sink := mustParse(t, `{"model": "gpt-5.3", "input": "hi", "reasoning": {"effort": "max"}}`, nil)
	if req.Reasoning.Effort != canon.EffortMax {
		t.Fatalf("max effort = %d, want EffortMax", req.Reasoning.Effort)
	}
	if len(sink.warnings) != 0 {
		t.Fatalf("max effort must not warn: %v", sink.warnings)
	}
	req, _, sink = mustParse(t, `{"model": "gpt-5.3", "input": "hi", "reasoning": {"effort": "ultra"}}`, nil)
	if req.Reasoning.Effort != canon.EffortMax {
		t.Fatalf("ultra effort = %d, want EffortMax", req.Reasoning.Effort)
	}
	if len(sink.warnings) == 0 {
		t.Fatal("ultra effort must warn")
	}
}

func TestEffortCapsFromPolicyHeaders(t *testing.T) {
	g, _ := newTestIngress()
	hr, err := http.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	hr.Header.Set("x-openai-subagent", "refactor-worker")
	hr.Header.Set("x-codex-turn-metadata", "depth=2;role=worker")
	caps, ok := g.EffortCaps(hr)
	if !ok || caps.Subagent != "refactor-worker" || caps.TurnMetadata != "depth=2;role=worker" {
		t.Fatalf("caps: %+v %v", caps, ok)
	}
	plain, err := http.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.EffortCaps(plain); ok {
		t.Fatal("caps present without policy headers")
	}
}

func TestPolicyHeadersStayOutOfCanonRequest(t *testing.T) {
	withHeaders := `{"model": "gpt-5.3", "input": "hi", "reasoning": {"effort": "medium"}}`
	reqWith, _, _, err := parseBody(t, withHeaders, map[string]string{
		"x-openai-subagent":     "refactor-worker",
		"x-codex-turn-metadata": "depth=2",
	})
	if err != nil {
		t.Fatal(err)
	}
	reqWithout, _, _, err := parseBody(t, withHeaders, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reqWith, reqWithout) {
		t.Fatalf("policy headers changed canon request:\n%+v\n%+v", reqWith, reqWithout)
	}
}

func TestToolSchemaValidation(t *testing.T) {
	cases := []struct {
		name   string
		params string
	}{
		{"required not in properties", `{"type": "object", "properties": {"a": {"type": "string"}}, "required": ["b"]}`},
		{"unknown type", `{"type": "integerx"}`},
		{"non-object parameters", `[]`},
		{"bad properties entry", `{"type": "object", "properties": {"a": "nope"}}`},
		{"bad items", `{"type": "array", "items": 3}`},
		{"required not strings", `{"type": "object", "properties": {"a": {"type": "string"}}, "required": [1]}`},
	}
	for _, tc := range cases {
		body := `{"model": "gpt-5.3", "input": "hi", "tools": [{"type": "function", "name": "f", "parameters": ` + tc.params + `}]}`
		_, _, _, err := parseBody(t, body, nil)
		parseErr, ok := err.(*ParseError)
		if !ok {
			t.Fatalf("%s: want ParseError, got %v", tc.name, err)
		}
		if parseErr.Status < 400 || parseErr.Status >= 500 {
			t.Fatalf("%s: status %d", tc.name, parseErr.Status)
		}
		if parseErr.Reason != ReasonInvalidField {
			t.Fatalf("%s: reason %v", tc.name, parseErr.Reason)
		}
		if !strings.HasPrefix(parseErr.Field, "tools[0].parameters") {
			t.Fatalf("%s: field %q", tc.name, parseErr.Field)
		}
	}
	validBody := `{"model": "gpt-5.3", "input": "hi", "tools": [
		{"type": "function", "name": "f", "strict": true, "parameters": {"type": "object", "properties": {"a": {"type": ["integer", "null"]}}, "required": ["a"]}},
		{"type": "custom", "name": "c", "format": {"type": "json"}},
		{"type": "local_shell"},
		{"type": "tool_search", "max_results": 5}
	]}`
	req, _, _ := mustParse(t, validBody, nil)
	if len(req.Tools) != 4 {
		t.Fatalf("tools: %d", len(req.Tools))
	}
	fn, ok := req.Tools[0].(canon.FunctionTool)
	if !ok || !fn.Strict || fn.Name != "f" {
		t.Fatalf("tool[0]: %#v", req.Tools[0])
	}
	var schema map[string]any
	if err := json.Unmarshal(fn.Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	custom, ok := req.Tools[1].(canon.CustomToolDef)
	if !ok || custom.Format != canon.FormatJSON {
		t.Fatalf("tool[1]: %#v", req.Tools[1])
	}
	if _, ok := req.Tools[2].(canon.LocalShellToolDef); !ok {
		t.Fatalf("tool[2]: %#v", req.Tools[2])
	}
	search, ok := req.Tools[3].(canon.ToolSearchToolDef)
	if !ok || search.Limit != 5 {
		t.Fatalf("tool[3]: %#v", req.Tools[3])
	}
}

func TestCustomToolGrammarFormatParses(t *testing.T) {
	body := `{"model": "gpt-5.3", "input": "hi", "tools": [
		{"type": "custom", "name": "apply_patch", "format": {"type": "grammar", "syntax": "lark", "definition": "start: A"}}
	]}`
	req, _, _ := mustParse(t, body, nil)
	custom, ok := req.Tools[0].(canon.CustomToolDef)
	if !ok {
		t.Fatalf("tool[0]: %#v", req.Tools[0])
	}
	if custom.Grammar == nil || custom.Grammar.Syntax != "lark" || custom.Grammar.Definition != "start: A" {
		t.Fatalf("grammar not parsed from nested format: %#v", custom)
	}
}

func TestGrokIntegerArgumentNormalization(t *testing.T) {
	body := `{
		"model": "gpt-5.3",
		"input": [
			{"type": "function_call", "id": "fc1", "call_id": "call_1", "name": "f", "arguments": "{\"count\": 3.0, \"ratio\": 0.5, \"nested\": {\"depth\": 2.0}, \"list\": [7.0, 8.5]}"}
		],
		"tools": [
			{"type": "function", "name": "f", "parameters": {
				"type": "object",
				"properties": {
					"count": {"type": "integer"},
					"ratio": {"type": "number"},
					"nested": {"type": "object", "properties": {"depth": {"type": "integer"}}},
					"list": {"type": "array", "items": {"type": "integer"}}
				}
			}}
		]
	}`
	req, _, _ := mustParse(t, body, map[string]string{"x-prism-grok": "1"})
	call, ok := req.Input[0].(canon.FunctionCall)
	if !ok {
		t.Fatalf("input[0]: %#v", req.Input[0])
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if args["count"] != float64(3) {
		t.Fatalf("count: %#v", args["count"])
	}
	if args["ratio"] != 0.5 {
		t.Fatalf("ratio: %#v", args["ratio"])
	}
	nested, _ := args["nested"].(map[string]any)
	if nested["depth"] != float64(2) {
		t.Fatalf("nested.depth: %#v", nested["depth"])
	}
	list, _ := args["list"].([]any)
	if len(list) != 2 || list[0] != float64(7) || list[1] != 8.5 {
		t.Fatalf("list: %#v", list)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		reason Reason
		field  string
	}{
		{"missing model", `{"input": "hi"}`, ReasonMissingField, "model"},
		{"missing input", `{"model": "gpt-5.3"}`, ReasonMissingField, "input"},
		{"bad json", `{nope`, ReasonInvalidJSON, "body"},
		{"bad role", `{"model": "gpt-5.3", "input": [{"type": "message", "role": "wizard", "content": "x"}]}`, ReasonInvalidField, "input[0].role"},
		{"bad input item", `{"model": "gpt-5.3", "input": [42]}`, ReasonInvalidField, "input[0]"},
		{"bad input kind", `{"model": "gpt-5.3", "input": 5}`, ReasonInvalidField, "input"},
		{"bad arguments", `{"model": "gpt-5.3", "input": [{"type": "function_call", "call_id": "c", "name": "f", "arguments": "{oops"}]}`, ReasonInvalidJSON, "input[0].arguments"},
		{"non-object arguments", `{"model": "gpt-5.3", "input": [{"type": "function_call", "call_id": "c", "name": "f", "arguments": "[1]"}]}`, ReasonInvalidField, "input[0].arguments"},
		{"missing output call id", `{"model": "gpt-5.3", "input": [{"type": "function_call_output", "output": "x"}]}`, ReasonMissingField, "input[0].call_id"},
		{"bad reasoning effort", `{"model": "gpt-5.3", "input": "hi", "reasoning": {"effort": "ludicrous"}}`, ReasonInvalidField, "reasoning.effort"},
		{"bad reasoning summary", `{"model": "gpt-5.3", "input": "hi", "reasoning": {"summary": "verbose"}}`, ReasonInvalidField, "reasoning.summary"},
		{"bad content part", `{"model": "gpt-5.3", "input": [{"type": "message", "role": "user", "content": [{"type": "smell"}]}]}`, ReasonInvalidField, "input[0].content[0].type"},
		{"bad tool choice", `{"model": "gpt-5.3", "input": "hi", "tool_choice": "sometimes"}`, ReasonInvalidField, "tool_choice"},
	}
	for _, tc := range cases {
		_, _, _, err := parseBody(t, tc.body, nil)
		parseErr, ok := err.(*ParseError)
		if !ok {
			t.Fatalf("%s: want ParseError, got %v", tc.name, err)
		}
		if parseErr.Reason != tc.reason || parseErr.Field != tc.field {
			t.Fatalf("%s: got %v/%q, want %v/%q", tc.name, parseErr.Reason, parseErr.Field, tc.reason, tc.field)
		}
		if parseErr.Status < 400 || parseErr.Status >= 500 {
			t.Fatalf("%s: status %d", tc.name, parseErr.Status)
		}
	}
}

func TestParseRejectsNonPost(t *testing.T) {
	g, _ := newTestIngress()
	hr, err := http.NewRequest(http.MethodGet, "/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = g.Parse(context.Background(), hr)
	parseErr, ok := err.(*ParseError)
	if !ok || parseErr.Status != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 ParseError, got %v", err)
	}
}

func TestParseStringInputAndToolChoice(t *testing.T) {
	req, _, _ := mustParse(t, `{"model": "gpt-5.3", "input": "hello", "tool_choice": "required"}`, nil)
	msg, ok := req.Input[0].(canon.Message)
	if !ok || msg.Role != canon.RoleUser || msg.Content[0] != (canon.TextContent{Text: "hello"}) {
		t.Fatalf("input: %#v", req.Input[0])
	}
	if _, ok := req.ToolChoice.(canon.ToolRequired); !ok {
		t.Fatalf("tool choice: %#v", req.ToolChoice)
	}
}

func TestAllowedToolsFilter(t *testing.T) {
	body := `{
		"model": "gpt-5.3",
		"input": "hi",
		"tools": [
			{"type": "function", "name": "a"},
			{"type": "function", "name": "b"},
			{"type": "custom", "name": "c"}
		],
		"tool_choice": {"type": "allowed_tools", "mode": "required", "tools": [{"name": "a"}, {"name": "c"}]}
	}`
	req, _, _ := mustParse(t, body, nil)
	if len(req.Tools) != 2 {
		t.Fatalf("tools: %d", len(req.Tools))
	}
	_, ok := req.ToolChoice.(canon.ToolRequired)
	if !ok {
		t.Fatalf("tool choice: %#v", req.ToolChoice)
	}
}

func TestWarningsFlowToSink(t *testing.T) {
	sink := &warningSink{}
	g := New(sink.ingest)
	hr, err := http.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model": "gpt-5.3", "input": [{"type": "mystery"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Parse(context.Background(), hr); err != nil {
		t.Fatal(err)
	}
	if len(sink.warnings) != 1 || sink.warnings[0].Kind != WarnUnknownItem || !strings.Contains(sink.warnings[0].Detail, "mystery") {
		t.Fatalf("warnings: %+v", sink.warnings)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil sink")
		}
	}()
	New(nil)
}

func TestTextFormatParsing(t *testing.T) {
	body := `{
		"model": "gpt-5.3",
		"input": "hi",
		"text": {"format": {"type": "json_schema", "name": "out", "description": "structured", "strict": true, "schema": {"type": "object", "properties": {"a": {"type": "string"}}}}}
	}`
	req, _, _ := mustParse(t, body, nil)
	tf := req.Text.Format
	if tf == nil || tf.Type != "json_schema" || tf.Name != "out" || tf.Description != "structured" || tf.Strict == nil || !*tf.Strict {
		t.Fatalf("text format: %+v", tf)
	}
	var schema map[string]any
	if err := json.Unmarshal(tf.Schema, &schema); err != nil || schema["type"] != "object" {
		t.Fatalf("schema: %s %v", tf.Schema, err)
	}
}

func TestParseFunctionCallOutputImage(t *testing.T) {
	img := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg=="
	body := fmt.Sprintf(`{"model":"gpt-5.3","input":[{"type":"function_call_output","call_id":"call_img","output":[{"type":"input_image","image_url":"data:image/png;base64,%s","detail":"high"}]}]}`, img)
	req, _, _ := mustParse(t, body, nil)
	out, ok := req.Input[0].(canon.FunctionOutput)
	if !ok || out.CallID != canon.CallID("call_img") {
		t.Fatalf("item: %#v", req.Input[0])
	}
	if len(out.Output) != 1 {
		t.Fatalf("output parts: %d", len(out.Output))
	}
	imgContent, ok := out.Output[0].(canon.ImageContent)
	if !ok || imgContent.MIMEType != "image/png" || len(imgContent.Data) == 0 {
		t.Fatalf("output content: %#v", out.Output[0])
	}
}
