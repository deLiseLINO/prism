package messages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/execution"
)

func parseBody(t *testing.T, body string, headers map[string]string) (canon.Request, execution.Facts, error) {
	t.Helper()
	hr := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	for k, v := range headers {
		hr.Header.Set(k, v)
	}
	return Ingress{}.Parse(context.Background(), hr)
}

func mustParse(t *testing.T, body string) canon.Request {
	t.Helper()
	req, _, err := parseBody(t, body, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return req
}

func parseErr(t *testing.T, err error) *ParseError {
	t.Helper()
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParseError, got %v", err)
	}
	return pe
}

func intPtr(v int) *int { return &v }

func f64Ptr(v float64) *float64 { return &v }

func TestSystemInstructions(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []canon.Content
	}{
		{
			name: "system string",
			body: `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"system":"be brief"}`,
			want: []canon.Content{canon.TextContent{Text: "be brief"}},
		},
		{
			name: "system content blocks joined",
			body: `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"system":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}`,
			want: []canon.Content{canon.TextContent{Text: "one\n\ntwo"}},
		},
		{
			name: "no system",
			body: `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustParse(t, tt.body)
			if len(req.Instructions) != len(tt.want) {
				t.Fatalf("instructions = %#v, want %#v", req.Instructions, tt.want)
			}
			for i := range tt.want {
				if req.Instructions[i] != tt.want[i] {
					t.Fatalf("instructions[%d] = %#v, want %#v", i, req.Instructions[i], tt.want[i])
				}
			}
		})
	}
}

func TestMessageBlocks(t *testing.T) {
	const body = `{
		"model":"claude-antigravity--gemini-3-pro",
		"max_tokens":100,
		"messages":[
			{"role":"user","content":"plain string"},
			{"role":"user","content":[
				{"type":"text","text":"look"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},
				{"type":"tool_result","tool_use_id":"call1","content":"result text"},
				{"type":"text","text":"after"}
			]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"let me think","signature":"sig1"},
				{"type":"text","text":"doing"},
				{"type":"tool_use","id":"call1","name":"get_weather","input":{"city":"paris"}}
			]}
		]
	}`
	req := mustParse(t, body)
	if len(req.Input) != 7 {
		t.Fatalf("input items = %d, want 7: %#v", len(req.Input), req.Input)
	}
	first, ok := req.Input[0].(canon.Message)
	if !ok || first.Role != canon.RoleUser || len(first.Content) != 1 || first.Content[0] != (canon.TextContent{Text: "plain string"}) {
		t.Fatalf("input[0] = %#v", req.Input[0])
	}
	second, ok := req.Input[1].(canon.Message)
	if !ok || second.Role != canon.RoleUser || len(second.Content) != 2 {
		t.Fatalf("input[1] = %#v", req.Input[1])
	}
	if second.Content[0] != (canon.TextContent{Text: "look"}) {
		t.Fatalf("content[0] = %#v", second.Content[0])
	}
	img, ok := second.Content[1].(canon.ImageContent)
	if !ok || img.MIMEType != "image/png" || string(img.Data) != "hello" {
		t.Fatalf("content[1] = %#v", second.Content[1])
	}
	out, ok := req.Input[2].(canon.FunctionOutput)
	if !ok || out.CallID != "call1" || len(out.Output) != 1 || out.Output[0] != (canon.TextContent{Text: "result text"}) {
		t.Fatalf("input[2] = %#v", req.Input[2])
	}
	third, ok := req.Input[3].(canon.Message)
	if !ok || third.Role != canon.RoleUser || len(third.Content) != 1 || third.Content[0] != (canon.TextContent{Text: "after"}) {
		t.Fatalf("input[3] = %#v", req.Input[3])
	}
	reasoning, ok := req.Input[4].(canon.ReasoningItem)
	if !ok || reasoning.Content != "let me think" || reasoning.Signature != "sig1" {
		t.Fatalf("input[4] = %#v", req.Input[4])
	}
	doing, ok := req.Input[5].(canon.Message)
	if !ok || doing.Role != canon.RoleAssistant || len(doing.Content) != 1 || doing.Content[0] != (canon.TextContent{Text: "doing"}) {
		t.Fatalf("input[5] = %#v", req.Input[5])
	}
	call, ok := req.Input[6].(canon.FunctionCall)
	if !ok || call.CallID != "call1" || call.Name != "get_weather" {
		t.Fatalf("input[6] = %#v", req.Input[6])
	}
}

func TestAssistantToolUse(t *testing.T) {
	const body = `{
		"model":"claude-antigravity--gemini-3-pro",
		"max_tokens":100,
		"messages":[
			{"role":"user","content":"weather?"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call9","name":"lookup","input":{"q":"sf"}}]}
		]
	}`
	req := mustParse(t, body)
	if len(req.Input) != 2 {
		t.Fatalf("input items = %d", len(req.Input))
	}
	call, ok := req.Input[1].(canon.FunctionCall)
	if !ok || call.CallID != "call9" || call.Name != "lookup" || string(call.Arguments) != `{"q":"sf"}` {
		t.Fatalf("input[1] = %#v", req.Input[1])
	}
}

func TestThinkingConfig(t *testing.T) {
	tests := []struct {
		name       string
		thinking   string
		wantEffort canon.ReasoningEffort
		wantSum    canon.ReasoningSummary
	}{
		{name: "low budget", thinking: `{"type":"enabled","budget_tokens":2048}`, wantEffort: canon.EffortLow, wantSum: canon.SummaryAuto},
		{name: "medium budget", thinking: `{"type":"enabled","budget_tokens":8192}`, wantEffort: canon.EffortMedium, wantSum: canon.SummaryAuto},
		{name: "high budget", thinking: `{"type":"enabled","budget_tokens":32768}`, wantEffort: canon.EffortHigh, wantSum: canon.SummaryAuto},
		{name: "enabled without budget", thinking: `{"type":"enabled"}`, wantEffort: 0, wantSum: canon.SummaryAuto},
		{name: "adaptive", thinking: `{"type":"adaptive"}`, wantEffort: 0, wantSum: canon.SummaryAuto},
		{name: "disabled", thinking: `{"type":"disabled"}`, wantEffort: 0, wantSum: 0},
		{name: "absent", thinking: "", wantEffort: 0, wantSum: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}]`
			if tt.thinking != "" {
				body += `,"thinking":` + tt.thinking
			}
			req := mustParse(t, body+"}")
			if req.Reasoning.Effort != tt.wantEffort || req.Reasoning.Summary != tt.wantSum {
				t.Fatalf("reasoning = %#v, want effort=%d summary=%d", req.Reasoning, tt.wantEffort, tt.wantSum)
			}
		})
	}

	t.Run("unknown thinking type is a parse error", func(t *testing.T) {
		body := `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"turbo"}}`
		_, _, err := parseBody(t, body, nil)
		pe := parseErr(t, err)
		if pe.Reason != ReasonInvalidField || pe.Field != "thinking.type" {
			t.Fatalf("parse error = %#v", pe)
		}
	})
}

func TestToolsAndToolChoice(t *testing.T) {
	const body = `{
		"model":"claude-antigravity--gemini-3-pro",
		"max_tokens":100,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[
			{"name":"get_weather","description":"weather lookup","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}},
			{"type":"web_search_20250305","name":"web_search"},
			{"name":"no_schema"}
		],
		"tool_choice":{"type":"tool","name":"get_weather","disable_parallel_tool_use":true}
	}`
	req := mustParse(t, body)
	if len(req.Tools) != 1 {
		t.Fatalf("tools = %#v", req.Tools)
	}
	tool, ok := req.Tools[0].(canon.FunctionTool)
	if !ok || tool.Name != "get_weather" || tool.Description != "weather lookup" || len(tool.Parameters) == 0 {
		t.Fatalf("tools[0] = %#v", req.Tools[0])
	}
	named, ok := req.ToolChoice.(canon.ToolNamed)
	if !ok || named.Name != "get_weather" {
		t.Fatalf("tool_choice = %#v", req.ToolChoice)
	}
	if req.Sampling.ParallelToolCalls == nil || *req.Sampling.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls = %#v", req.Sampling.ParallelToolCalls)
	}
}

func TestToolChoiceTypes(t *testing.T) {
	tests := []struct {
		choice string
		want   canon.ToolChoice
	}{
		{choice: `{"type":"auto"}`, want: canon.ToolAuto{}},
		{choice: `{"type":"none"}`, want: canon.ToolNone{}},
		{choice: `{"type":"any"}`, want: canon.ToolRequired{}},
	}
	for _, tt := range tests {
		t.Run(tt.choice, func(t *testing.T) {
			body := `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tool_choice":` + tt.choice + `}`
			req := mustParse(t, body)
			if req.ToolChoice != tt.want {
				t.Fatalf("tool_choice = %#v, want %#v", req.ToolChoice, tt.want)
			}
		})
	}
	t.Run("tool choice without name", func(t *testing.T) {
		body := `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"tool"}}`
		_, _, err := parseBody(t, body, nil)
		pe := parseErr(t, err)
		if pe.Reason != ReasonMissingField || pe.Field != "tool_choice.name" {
			t.Fatalf("parse error = %#v", pe)
		}
	})
}

func TestModelAlias(t *testing.T) {
	tests := []struct {
		name     string
		alias    string
		provider string
		model    string
		wantErr  bool
	}{
		{name: "antigravity", alias: "claude-antigravity--gemini-3-pro", provider: "antigravity", model: "gemini-3-pro"},
		{name: "codex", alias: "claude-codex--gpt-5.2", provider: "codex", model: "gpt-5.2"},
		{name: "model with dash", alias: "claude-custom--my-model-v2", provider: "custom", model: "my-model-v2"},
		{name: "missing prefix", alias: "claude-sonnet-4", wantErr: true},
		{name: "missing separator", alias: "claude-antigravity", wantErr: true},
		{name: "empty provider", alias: "claude---model", wantErr: true},
		{name: "empty model", alias: "claude-antigravity--", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, model, err := ParseModelAlias(tt.alias)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseModelAlias(%q) = %q, %q, want error", tt.alias, provider, model)
				}
				pe := parseErr(t, err)
				if pe.Reason != ReasonInvalidField || pe.Field != "model" {
					t.Fatalf("parse error = %#v", pe)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseModelAlias(%q): %v", tt.alias, err)
			}
			if provider != tt.provider || model != tt.model {
				t.Fatalf("ParseModelAlias(%q) = %q, %q", tt.alias, provider, model)
			}
		})
	}

	t.Run("Parse keeps the alias in canon model", func(t *testing.T) {
		req := mustParse(t, `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
		if req.Model != "claude-antigravity--gemini-3-pro" {
			t.Fatalf("model = %q", req.Model)
		}
	})

	t.Run("Parse rejects non-alias model", func(t *testing.T) {
		_, _, err := parseBody(t, `{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, nil)
		pe := parseErr(t, err)
		if pe.Reason != ReasonInvalidField || pe.Field != "model" {
			t.Fatalf("parse error = %#v", pe)
		}
	})
}

func TestMaxTokensRequired(t *testing.T) {
	t.Run("missing max_tokens", func(t *testing.T) {
		_, _, err := parseBody(t, `{"model":"claude-antigravity--gemini-3-pro","messages":[{"role":"user","content":"hi"}]}`, nil)
		pe := parseErr(t, err)
		if pe.Reason != ReasonMissingField || pe.Field != "max_tokens" {
			t.Fatalf("parse error = %#v", pe)
		}
		if pe.HTTPStatus() < 400 || pe.HTTPStatus() > 499 {
			t.Fatalf("HTTPStatus = %d, want 4xx", pe.HTTPStatus())
		}
	})
	t.Run("non-positive max_tokens", func(t *testing.T) {
		_, _, err := parseBody(t, `{"model":"claude-antigravity--gemini-3-pro","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`, nil)
		pe := parseErr(t, err)
		if pe.Reason != ReasonInvalidField || pe.Field != "max_tokens" {
			t.Fatalf("parse error = %#v", pe)
		}
	})
}

func TestSamplingPassthrough(t *testing.T) {
	req := mustParse(t, `{
		"model":"claude-antigravity--gemini-3-pro",
		"max_tokens":256,
		"stream":true,
		"temperature":0.7,
		"top_p":0.9,
		"stop_sequences":["END","STOP"],
		"messages":[{"role":"user","content":"hi"}]
	}`)
	if !req.Stream || req.MaxOutputTokens != 256 {
		t.Fatalf("stream/max_tokens = %v/%d", req.Stream, req.MaxOutputTokens)
	}
	if req.Sampling.Temperature == nil || *req.Sampling.Temperature != 0.7 {
		t.Fatalf("temperature = %#v", req.Sampling.Temperature)
	}
	if req.Sampling.TopP == nil || *req.Sampling.TopP != 0.9 {
		t.Fatalf("top_p = %#v", req.Sampling.TopP)
	}
	if len(req.Sampling.Stop) != 2 || req.Sampling.Stop[0] != "END" {
		t.Fatalf("stop = %#v", req.Sampling.Stop)
	}
}

func TestSystemRoleMessageGoesToInstructions(t *testing.T) {
	req := mustParse(t, `{
		"model":"claude-antigravity--gemini-3-pro",
		"max_tokens":100,
		"messages":[
			{"role":"system","content":"inline system"},
			{"role":"user","content":"hi"}
		]
	}`)
	if len(req.Instructions) != 1 || req.Instructions[0] != (canon.TextContent{Text: "inline system"}) {
		t.Fatalf("instructions = %#v", req.Instructions)
	}
	if len(req.Input) != 1 {
		t.Fatalf("input = %#v", req.Input)
	}
}

func TestCredentialNeverLeaks(t *testing.T) {
	const key = "sk-ant-secret-123"
	body := `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"system":"be brief"}`
	req, facts, err := parseBody(t, body, map[string]string{
		"x-api-key":    key,
		"x-session-id": "sess-1",
		"originator":   "claude-code",
		"user-agent":   "claude-cli/2.0",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if facts.Client != execution.ClientAnthropic {
		t.Fatalf("client = %d", facts.Client)
	}
	if facts.Session != "sess-1" {
		t.Fatalf("session = %q", facts.Session)
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal canon request: %v", err)
	}
	if strings.Contains(string(encoded), key) {
		t.Fatalf("canon request contains the credential: %s", encoded)
	}
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		t.Fatalf("marshal facts: %v", err)
	}
	if strings.Contains(string(factsJSON), key) {
		t.Fatalf("facts contain the credential: %s", factsJSON)
	}
	if _, ok := facts.Forward.Get(execution.ForwardSessionID); !ok {
		t.Fatalf("forward set lost x-session-id")
	}
	if _, ok := facts.Forward.Get(execution.ForwardUserAgent); !ok {
		t.Fatalf("forward set lost user-agent")
	}
	if _, ok := facts.Forward.Get(execution.ForwardOriginator); !ok {
		t.Fatalf("forward set lost originator")
	}

	t.Run("execution credential handling rejects x-api-key", func(t *testing.T) {
		h := http.Header{}
		h.Set("x-api-key", key)
		if _, err := execution.NewForwardSet(h); err == nil {
			t.Fatalf("NewForwardSet accepted x-api-key")
		}
	})
}

func TestInvalidJSON(t *testing.T) {
	_, _, err := parseBody(t, `{"model":`, nil)
	pe := parseErr(t, err)
	if pe.Reason != ReasonInvalidJSON {
		t.Fatalf("parse error = %#v", pe)
	}
}

func TestInvalidMessageRole(t *testing.T) {
	_, _, err := parseBody(t, `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100,"messages":[{"role":"tool","content":"hi"}]}`, nil)
	pe := parseErr(t, err)
	if pe.Reason != ReasonInvalidField || pe.Field != "messages.role" {
		t.Fatalf("parse error = %#v", pe)
	}
}

func TestMissingModel(t *testing.T) {
	_, _, err := parseBody(t, `{"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, nil)
	pe := parseErr(t, err)
	if pe.Reason != ReasonMissingField || pe.Field != "model" {
		t.Fatalf("parse error = %#v", pe)
	}
}

func TestMissingMessages(t *testing.T) {
	_, _, err := parseBody(t, `{"model":"claude-antigravity--gemini-3-pro","max_tokens":100}`, nil)
	pe := parseErr(t, err)
	if pe.Reason != ReasonMissingField || pe.Field != "messages" {
		t.Fatalf("parse error = %#v", pe)
	}
}

func TestParseReasonString(t *testing.T) {
	if ReasonInvalidJSON.String() != "invalid_json" {
		t.Fatalf("invalid_json = %q", ReasonInvalidJSON)
	}
	if ReasonMissingField.String() != "missing_field" {
		t.Fatalf("missing_field = %q", ReasonMissingField)
	}
	if ReasonInvalidField.String() != "invalid_field" {
		t.Fatalf("invalid_field = %q", ReasonInvalidField)
	}
	if ParseReason(99).String() != "ParseReason(99)" {
		t.Fatalf("unknown = %q", ParseReason(99))
	}
}

func TestParseErrorUnwrap(t *testing.T) {
	sentinel := errors.New("boom")
	pe := &ParseError{Reason: ReasonInvalidJSON, Field: "body", Err: sentinel}
	if !errors.Is(pe, sentinel) {
		t.Fatalf("Unwrap lost the cause")
	}
	if !strings.Contains(pe.Error(), "boom") {
		t.Fatalf("Error() = %q", pe.Error())
	}
}
