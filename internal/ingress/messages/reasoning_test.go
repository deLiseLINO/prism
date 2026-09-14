package messages

import (
	"testing"

	"prism/internal/canon"
	"prism/internal/reasonenv"
)

func TestRedactedThinkingResentBecomesReasoningItem(t *testing.T) {
	const body = `{
		"model":"claude-antigravity--gemini-3-pro",
		"max_tokens":100,
		"messages":[
			{"role":"assistant","content":[
				{"type":"redacted_thinking","data":"opaque-blob"}
			]},
			{"role":"user","content":"hi"}
		]
	}`
	req := mustParse(t, body)
	if len(req.Input) != 2 {
		t.Fatalf("input items = %d, want 2: %#v", len(req.Input), req.Input)
	}
	ri, ok := req.Input[0].(canon.ReasoningItem)
	if !ok {
		t.Fatalf("input[0] = %#v, want ReasoningItem", req.Input[0])
	}
	env, ok := reasonenv.Decode(ri.Signature)
	if !ok || len(env.Red) != 1 || env.Red[0] != "opaque-blob" {
		t.Fatalf("signature %q does not carry redacted payload", ri.Signature)
	}
}

func TestServerToolUseAndSearchHistoryRenderAsText(t *testing.T) {
	const body = `{
		"model":"claude-antigravity--gemini-3-pro",
		"max_tokens":100,
		"messages":[
			{"role":"assistant","content":[
				{"type":"server_tool_use","id":"srvu_1","name":"web_search","input":{"query":"weather paris"}}
			]},
			{"role":"user","content":[
				{"type":"web_search_tool_result","tool_use_id":"srvu_1","content":[
					{"type":"web_search_result","url":"https://example.com/a","title":"A"},
					{"type":"web_search_result","url":"https://example.com/b","title":"B"}
				]},
				{"type":"text","text":"continue"}
			]}
		]
	}`
	req := mustParse(t, body)
	if len(req.Input) != 2 {
		t.Fatalf("input items = %d, want 2: %#v", len(req.Input), req.Input)
	}
	assistant, ok := req.Input[0].(canon.Message)
	if !ok || len(assistant.Content) != 1 {
		t.Fatalf("input[0] = %#v, want one rendered text part", req.Input[0])
	}
	if text, ok := assistant.Content[0].(canon.TextContent); !ok || text.Text != "[server tool web_search: weather paris]" {
		t.Fatalf("assistant history block = %#v", assistant.Content[0])
	}
	user, ok := req.Input[1].(canon.Message)
	if !ok || len(user.Content) != 2 {
		t.Fatalf("input[1] = %#v, want search text + user text", req.Input[1])
	}
	if text, ok := user.Content[0].(canon.TextContent); !ok || text.Text != "[web search results: https://example.com/a https://example.com/b]" {
		t.Fatalf("search history block = %#v", user.Content[0])
	}
	if text, ok := user.Content[1].(canon.TextContent); !ok || text.Text != "continue" {
		t.Fatalf("user text = %#v", user.Content[1])
	}
}

func TestEmptyThinkingBlockDropped(t *testing.T) {
	const body = `{
		"model":"claude-antigravity--gemini-3-pro",
		"max_tokens":100,
		"messages":[
			{"role":"assistant","content":[
			{"type":"thinking","thinking":"","signature":""},
				{"type":"text","text":"done"}
			]},
			{"role":"user","content":"hi"}
		]
	}`
	req, _, err := parseBody(t, body, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	assistant, ok := req.Input[0].(canon.Message)
	if !ok || len(assistant.Content) != 1 {
		t.Fatalf("input[0] = %#v, want only the text part", req.Input[0])
	}
	if text, ok := assistant.Content[0].(canon.TextContent); !ok || text.Text != "done" {
		t.Fatalf("content = %#v", assistant.Content[0])
	}
}

func TestMalformedEnvelopeSignatureRejected(t *testing.T) {
	const body = `{
		"model":"claude-antigravity--gemini-3-pro",
		"max_tokens":100,
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"hmm","signature":"prismr1:not-base64!!"}
			]},
			{"role":"user","content":"hi"}
		]
	}`
	_, _, err := parseBody(t, body, nil)
	if err == nil {
		t.Fatalf("malformed prism signature accepted")
	}
	parseErr(t, err)
}
