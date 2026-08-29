package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/execution"
)

func parseBody(t *testing.T, body string, header http.Header) (canon.Request, execution.Facts, error) {
	t.Helper()
	hr, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	hr.Header = header
	return Ingress{}.Parse(context.Background(), hr)
}

func mustParse(t *testing.T, body string) canon.Request {
	t.Helper()
	req, _, err := parseBody(t, body, nil)
	if err != nil {
		t.Fatalf("Parse(%s): %v", body, err)
	}
	return req
}

func contentOf(item canon.Item) []canon.Content {
	msg, ok := item.(canon.Message)
	if !ok {
		return nil
	}
	return msg.Content
}

func contentDiff(want, got []canon.Content) string {
	w, err := json.Marshal(want)
	if err != nil {
		return fmt.Sprintf("marshal want: %v", err)
	}
	g, err := json.Marshal(got)
	if err != nil {
		return fmt.Sprintf("marshal got: %v", err)
	}
	if string(w) != string(g) {
		return fmt.Sprintf("want %s, got %s", w, g)
	}
	return ""
}

func TestRoleMapping(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		checkRole canon.Role
		wantItems func(t *testing.T, items []canon.Item)
	}{
		{
			name:      "system message",
			checkRole: canon.RoleSystem,
			body:      `{"model":"prism-gpt-5-6-luna","messages":[{"role":"system","content":"be terse"}]}`,
			wantItems: func(t *testing.T, items []canon.Item) {
				want := []canon.Content{canon.TextContent{Text: "be terse"}}
				if diff := contentDiff(want, contentOf(items[0])); diff != "" {
					t.Errorf("system content: %s", diff)
				}
			},
		},
		{
			name:      "user message",
			checkRole: canon.RoleUser,
			body:      `{"model":"antigravity/gemini-3.7-flash","messages":[{"role":"user","content":"hi"}]}`,
			wantItems: func(t *testing.T, items []canon.Item) {
				want := []canon.Content{canon.TextContent{Text: "hi"}}
				if diff := contentDiff(want, contentOf(items[0])); diff != "" {
					t.Errorf("user content: %s", diff)
				}
			},
		},
		{
			name:      "assistant message",
			checkRole: canon.RoleAssistant,
			body:      `{"model":"a/b","messages":[{"role":"assistant","content":"hello"}]}`,
			wantItems: func(t *testing.T, items []canon.Item) {
				want := []canon.Content{canon.TextContent{Text: "hello"}}
				if diff := contentDiff(want, contentOf(items[0])); diff != "" {
					t.Errorf("assistant content: %s", diff)
				}
			},
		},
		{
			name:      "developer message",
			checkRole: canon.RoleDeveloper,
			body:      `{"model":"a/b","messages":[{"role":"developer","content":"rules"}]}`,
			wantItems: func(t *testing.T, items []canon.Item) {
				msg, ok := items[0].(canon.Message)
				if !ok {
					t.Fatalf("want canon.Message, got %T", items[0])
				}
				if msg.Role != canon.RoleDeveloper {
					t.Errorf("role = %d, want RoleDeveloper", msg.Role)
				}
			},
		},
		{
			name:      "tool role maps to FunctionOutput item not Message",
			checkRole: 0,
			body:      `{"model":"a/b","messages":[{"role":"tool","tool_call_id":"call_7","content":"42"}]}`,
			wantItems: func(t *testing.T, items []canon.Item) {
				fo, ok := items[0].(canon.FunctionOutput)
				if !ok {
					t.Fatalf("want canon.FunctionOutput, got %T", items[0])
				}
				if fo.CallID != "call_7" {
					t.Errorf("CallID = %q, want call_7", fo.CallID)
				}
				want := []canon.Content{canon.TextContent{Text: "42"}}
				if diff := contentDiff(want, fo.Output); diff != "" {
					t.Errorf("output content: %s", diff)
				}
			},
		},
		{
			name:      "assistant tool_calls map to FunctionCall items",
			checkRole: 0,
			body:      `{"model":"a/b","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get","arguments":"{\"q\":\"x\"}"}}]}]}`,
			wantItems: func(t *testing.T, items []canon.Item) {
				if len(items) != 2 {
					t.Fatalf("len(items) = %d, want 2", len(items))
				}
				msg, ok := items[0].(canon.Message)
				if !ok || msg.Role != canon.RoleAssistant {
					t.Fatalf("items[0] = %T, want assistant Message", items[0])
				}
				call, ok := items[1].(canon.FunctionCall)
				if !ok {
					t.Fatalf("items[1] = %T, want canon.FunctionCall", items[1])
				}
				if call.CallID != "call_1" || call.Name != "get" || string(call.Arguments) != `{"q":"x"}` {
					t.Errorf("call = %+v", call)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustParse(t, tt.body)
			if tt.checkRole != 0 {
				msg, ok := req.Input[0].(canon.Message)
				if !ok {
					t.Fatalf("want canon.Message, got %T", req.Input[0])
				}
				if msg.Role != tt.checkRole {
					t.Errorf("role = %d, want %d", msg.Role, tt.checkRole)
				}
			}
			tt.wantItems(t, req.Input)
		})
	}
}

func TestContentParts(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47})
	body := fmt.Sprintf(`{"model":"a/b","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,%s","detail":"high"}}]}]}`, png)
	req := mustParse(t, body)
	want := []canon.Content{
		canon.TextContent{Text: "look"},
		canon.ImageContent{MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}, Detail: "high"},
	}
	if diff := contentDiff(want, contentOf(req.Input[0])); diff != "" {
		t.Errorf("content: %s", diff)
	}
}

func TestSamplingAndToolsMapping(t *testing.T) {
	body := `{
		"model":"openrouter/minimax/minimax-m3",
		"messages":[{"role":"user","content":"x"}],
		"stream":true,
		"max_tokens":512,
		"temperature":0.5,
		"top_p":0.9,
		"stop":["END","STOP"],
		"presence_penalty":0.1,
		"frequency_penalty":-0.2,
		"parallel_tool_calls":false,
		"tools":[{"type":"function","function":{"name":"search","description":"find things","parameters":{"type":"object"},"strict":true}}],
		"tool_choice":{"type":"function","function":{"name":"search"}}
	}`
	req := mustParse(t, body)
	if !req.Stream {
		t.Error("stream = false, want true")
	}
	if req.MaxOutputTokens != 512 {
		t.Errorf("MaxOutputTokens = %d, want 512", req.MaxOutputTokens)
	}
	s := req.Sampling
	if s.Temperature == nil || *s.Temperature != 0.5 {
		t.Errorf("Temperature = %v, want 0.5", s.Temperature)
	}
	if s.TopP == nil || *s.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", s.TopP)
	}
	if s.PresencePenalty == nil || *s.PresencePenalty != 0.1 {
		t.Errorf("PresencePenalty = %v, want 0.1", s.PresencePenalty)
	}
	if s.FrequencyPenalty == nil || *s.FrequencyPenalty != -0.2 {
		t.Errorf("FrequencyPenalty = %v, want -0.2", s.FrequencyPenalty)
	}
	if s.ParallelToolCalls == nil || *s.ParallelToolCalls != false {
		t.Errorf("ParallelToolCalls = %v, want false", s.ParallelToolCalls)
	}
	if len(s.Stop) != 2 || s.Stop[0] != "END" || s.Stop[1] != "STOP" {
		t.Errorf("Stop = %v, want [END STOP]", s.Stop)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(req.Tools))
	}
	ft, ok := req.Tools[0].(canon.FunctionTool)
	if !ok {
		t.Fatalf("Tools[0] = %T, want canon.FunctionTool", req.Tools[0])
	}
	if ft.Name != "search" || ft.Description != "find things" || !ft.Strict || string(ft.Parameters) != `{"type":"object"}` {
		t.Errorf("FunctionTool = %+v", ft)
	}
	named, ok := req.ToolChoice.(canon.ToolNamed)
	if !ok {
		t.Fatalf("ToolChoice = %T, want canon.ToolNamed", req.ToolChoice)
	}
	if named.Name != "search" {
		t.Errorf("ToolNamed.Name = %q, want search", named.Name)
	}
}

func TestToolChoiceMapping(t *testing.T) {
	tests := []struct {
		name       string
		toolChoice string
		wantKind   string
	}{
		{"auto", `"auto"`, "canon.ToolAuto"},
		{"none", `"none"`, "canon.ToolNone"},
		{"required", `"required"`, "canon.ToolRequired"},
		{"absent", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"model":"a/b","messages":[{"role":"user","content":"x"}]`
			if tt.toolChoice != "" {
				body += `,"tool_choice":` + tt.toolChoice
			}
			body += `}`
			req := mustParse(t, body)
			if tt.wantKind == "" {
				if req.ToolChoice != nil {
					t.Errorf("ToolChoice = %T, want nil", req.ToolChoice)
				}
				return
			}
			if got := fmt.Sprintf("%T", req.ToolChoice); got != tt.wantKind {
				t.Errorf("ToolChoice = %s, want %s", got, tt.wantKind)
			}
		})
	}
}

func TestAuthorizationNeverEntersFactsOrCanon(t *testing.T) {
	header := http.Header{}
	header.Set("Authorization", "Bearer super-secret-token")
	header.Set("User-Agent", "codex-cli/1.0")
	header.Set("Originator", "codex_cli_rs")
	header.Set("X-Session-Id", "sess-123")
	req, facts, err := parseBody(t, `{"model":"a/b","messages":[{"role":"user","content":"hi"}]}`, header)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if facts.Client != execution.ClientOMP {
		t.Errorf("Client = %d, want ClientOMP", facts.Client)
	}
	if v, ok := facts.Forward.Get(execution.ForwardUserAgent); !ok || v != "codex-cli/1.0" {
		t.Errorf("ForwardUserAgent = %q, %v", v, ok)
	}
	if v, ok := facts.Forward.Get(execution.ForwardOriginator); !ok || v != "codex_cli_rs" {
		t.Errorf("ForwardOriginator = %q, %v", v, ok)
	}
	if v, ok := facts.Forward.Get(execution.ForwardSessionID); !ok || v != "sess-123" {
		t.Errorf("ForwardSessionID = %q, %v", v, ok)
	}
	redacted := facts.Forward.Redacted()
	if strings.Contains(redacted, "authorization") || strings.Contains(redacted, "secret") {
		t.Errorf("Redacted = %q, must not carry authorization", redacted)
	}
	for _, name := range []string{"x-session-id", "originator", "user-agent"} {
		if !strings.Contains(redacted, name+"=<redacted>") {
			t.Errorf("Redacted = %q, missing %s", redacted, name)
		}
	}
	if req.Model != "a/b" {
		t.Errorf("Model = %q", req.Model)
	}
	if _, err := execution.NewForwardSet(header); err == nil {
		t.Error("NewForwardSet on raw headers must reject authorization; got nil error")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		want  Reason
		field string
	}{
		{"invalid json", `{not json`, ReasonInvalidJSON, "body"},
		{"missing model", `{"messages":[{"role":"user","content":"x"}]}`, ReasonMissingField, "model"},
		{"missing messages", `{"model":"a/b"}`, ReasonMissingField, "messages"},
		{"tool without call id", `{"model":"a/b","messages":[{"role":"tool","content":"x"}]}`, ReasonMissingField, "messages[0].tool_call_id"},
		{"tool call without id", `{"model":"a/b","messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"f","arguments":"{}"}}]}]}`, ReasonMissingField, "messages[0].tool_calls[0].id"},
		{"tool call without name", `{"model":"a/b","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"arguments":"{}"}}]}]}`, ReasonMissingField, "messages[0].tool_calls[0].function.name"},
		{"tool without function name", `{"model":"a/b","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{}}]}`, ReasonMissingField, "tools[0].function.name"},
		{"unknown role", `{"model":"a/b","messages":[{"role":"root","content":"x"}]}`, ReasonInvalidField, "messages[0].role"},
		{"invalid model slug", `{"model":"just-a-name","messages":[{"role":"user","content":"x"}]}`, ReasonInvalidField, "model"},
		{"unsupported tool type", `{"model":"a/b","messages":[{"role":"user","content":"x"}],"tools":[{"type":"custom"}]}`, ReasonInvalidField, "tools[0].type"},
		{"bad tool choice", `{"model":"a/b","messages":[{"role":"user","content":"x"}],"tool_choice":{"type":"allowed_tools"}}`, ReasonInvalidField, "tool_choice"},
		{"bad image url", `{"model":"a/b","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`, ReasonInvalidField, "messages[0].content[0].image_url.url"},
		{"unknown content part", `{"model":"a/b","messages":[{"role":"user","content":[{"type":"audio"}]}]}`, ReasonInvalidField, "messages[0].content[0].type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseBody(t, tt.body, nil)
			var perr *ParseError
			if !errors.As(err, &perr) {
				t.Fatalf("err = %v (%T), want *ParseError", err, err)
			}
			if perr.Reason != tt.want {
				t.Errorf("Reason = %s, want %s", perr.Reason, tt.want)
			}
			if perr.Field != tt.field {
				t.Errorf("Field = %q, want %q", perr.Field, tt.field)
			}
			if perr.Error() == "" {
				t.Error("Error() is empty")
			}
		})
	}
}

func TestParseErrorReasonsAreDistinct(t *testing.T) {
	if ReasonInvalidJSON == ReasonMissingField || ReasonMissingField == ReasonInvalidField || ReasonInvalidJSON == ReasonInvalidField {
		t.Fatal("parse reasons must be distinct")
	}
	if ReasonInvalidJSON.String() != "invalid_json" || ReasonMissingField.String() != "missing_field" || ReasonInvalidField.String() != "invalid_field" {
		t.Fatal("reason strings must be distinct")
	}
}

func TestModelSlugGrammar(t *testing.T) {
	valid := []string{
		"antigravity/gemini-3.7-flash",
		"openrouter/minimax/minimax-m3",
		"prism/auto",
		"prism-antigravity-gemini-3-7-flash",
		"prism-gpt-5-6-luna",
		"prism-123",
	}
	for _, model := range valid {
		t.Run("valid "+model, func(t *testing.T) {
			req := mustParse(t, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"x"}]}`, model))
			if req.Model != canon.ModelID(model) {
				t.Errorf("Model = %q, want %q without loss", req.Model, model)
			}
		})
	}
	invalid := []string{
		"just-a-name",
		"prism-",
		"prism-GPT",
		"prism-underscore_1",
		"/model",
		"provider/",
	}
	for _, model := range invalid {
		t.Run("invalid "+model, func(t *testing.T) {
			_, _, err := parseBody(t, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"x"}]}`, model), nil)
			var perr *ParseError
			if !errors.As(err, &perr) {
				t.Fatalf("model %q: err = %v, want *ParseError", model, err)
			}
			if perr.Reason != ReasonInvalidField {
				t.Errorf("model %q: Reason = %s, want invalid_field", model, perr.Reason)
			}
		})
	}
}
