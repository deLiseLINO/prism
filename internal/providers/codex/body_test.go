package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"prism/internal/canon"
)

func TestRequestBodyMatchesGolden(t *testing.T) {
	raw, err := os.ReadFile("fixtures/request-body-v1.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var golden struct {
		Request map[string]any `json:"request"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	req := canon.Request{
		Model:        "gpt-5.2-codex",
		Stream:       true,
		Instructions: []canon.Content{canon.TextContent{Text: "Be terse."}},
		Input: []canon.Item{
			canon.Message{
				ID:      "msg-1",
				Role:    canon.RoleUser,
				Content: []canon.Content{canon.TextContent{Text: "hi"}},
			},
			canon.ReasoningItem{
				ID:      "rs-1",
				Summary: []canon.TextContent{{Text: "thinking"}},
				State:   canon.OpaqueRef{Store: reasoningStoreNative, Key: "native-blob"},
			},
			canon.FunctionCall{ID: "fc-1", CallID: "call-1", Name: "lookup"},
			canon.FunctionOutput{ID: "out-1", CallID: "call-1", Output: []canon.Content{canon.TextContent{Text: "42"}}},
		},
		Tools: []canon.Tool{
			canon.FunctionTool{
				Name:        "lookup",
				Description: "finds",
				Parameters:  json.RawMessage(`{"type":"object"}`),
				Strict:      true,
			},
		},
		ToolChoice: canon.ToolAuto{},
		Reasoning:  canon.ReasoningConfig{Effort: canon.EffortHigh, Summary: canon.SummaryAuto},
		Sampling: canon.Sampling{
			Temperature:       ptrFloat(0.5),
			TopP:              ptrFloat(0.9),
			ParallelToolCalls: ptrBool(false),
		},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(result.Body, &got); err != nil {
		t.Fatalf("parse built body: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal built: %v", err)
	}
	wantJSON, err := json.Marshal(golden.Request)
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	if !jsonEqual(gotJSON, wantJSON) {
		t.Fatalf("body mismatch\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", result.Warnings)
	}
}

func jsonEqual(a, b []byte) bool { return bytes.Equal(a, b) }

func TestEffortWireSendsExtendedRungs(t *testing.T) {
	for effort, want := range map[canon.ReasoningEffort]string{
		canon.EffortXHigh: "xhigh",
		canon.EffortMax:   "max",
		canon.EffortOff:   "off",
	} {
		req := canon.Request{
			Model:     "gpt-5.2-codex",
			Stream:    false,
			Input:     []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}}},
			Reasoning: canon.ReasoningConfig{Effort: effort},
		}
		result, err := BuildRequestBody(req)
		if err != nil {
			t.Fatalf("BuildRequestBody(effort %d): %v", effort, err)
		}
		var got map[string]any
		if err := json.Unmarshal(result.Body, &got); err != nil {
			t.Fatalf("parse built body: %v", err)
		}
		reasoning, ok := got["reasoning"].(map[string]any)
		if !ok || reasoning["effort"] != want {
			t.Fatalf("effort %d wire = %#v, want %q", effort, got["reasoning"], want)
		}
	}
}

func TestRequestBodyKeyOrder(t *testing.T) {
	req := canon.Request{
		Model:  "gpt-5.2-codex",
		Stream: true,
		Input: []canon.Item{
			canon.Message{ID: "m", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
		},
		Reasoning: canon.ReasoningConfig{Effort: canon.EffortLow},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	checkKeyOrder(t, result.Body, "model", "input")
	checkKeyOrder(t, result.Body, "input", "reasoning")
	checkKeyOrder(t, result.Body, "reasoning", "stream")
	checkKeyOrder(t, result.Body, "stream", "store")
}

func TestRequestBodyDropsUnsupportedParamsWithWarnings(t *testing.T) {
	req := canon.Request{
		Model:           "gpt-5.2-codex",
		MaxOutputTokens: 100,
		Sampling: canon.Sampling{
			Stop:             []string{"."},
			PresencePenalty:  ptrFloat(0.1),
			FrequencyPenalty: ptrFloat(0.2),
		},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	want := []string{"drop:max_output_tokens", "drop:stop", "drop:presence_penalty", "drop:frequency_penalty"}
	if len(result.Warnings) != len(want) {
		t.Fatalf("warnings = %v want %v", result.Warnings, want)
	}
	for i := range want {
		if result.Warnings[i] != want[i] {
			t.Fatalf("warning[%d] = %q want %q", i, result.Warnings[i], want[i])
		}
	}
}

func TestRequestBodyIncludesReasoningEncryptedContent(t *testing.T) {
	req := canon.Request{Model: "gpt-5.2-codex", Stream: true, Reasoning: canon.ReasoningConfig{Effort: canon.EffortMedium}}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(result.Body, &raw); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	include, ok := raw["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %v, want [reasoning.encrypted_content]", raw["include"])
	}
	if _, ok := raw["reasoning"]; !ok {
		t.Fatalf("reasoning object missing")
	}
}

func TestRequestBodyKeepsNativeEncryptedContent(t *testing.T) {
	req := canon.Request{
		Model:  "gpt-5.2-codex",
		Stream: true,
		Input: []canon.Item{
			canon.ReasoningItem{ID: "rs-1", State: canon.OpaqueRef{Store: reasoningStoreNative, Key: "blob"}},
		},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(result.Body, &raw); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	input := raw["input"].([]any)
	item := input[0].(map[string]any)
	if item["encrypted_content"] != "blob" {
		t.Fatalf("encrypted_content = %v, want blob", item["encrypted_content"])
	}
}

func checkKeyOrder(t *testing.T, body []byte, before, after string) {
	t.Helper()
	bi := indexOfKey(body, before)
	ai := indexOfKey(body, after)
	if bi < 0 || ai < 0 || bi >= ai {
		t.Fatalf("key %q must precede %q in %s", before, after, body)
	}
}

func indexOfKey(body []byte, key string) int {
	return bytes.Index(body, []byte(`"`+key+`":`))
}

func TestInstructionsJoined(t *testing.T) {
	req := canon.Request{
		Model:        "gpt-5.2-codex",
		Stream:       true,
		Instructions: []canon.Content{canon.TextContent{Text: "a"}, canon.TextContent{Text: "b"}},
		Input:        []canon.Item{},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(result.Body, &raw); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if raw["instructions"] != "a\n\nb" {
		t.Fatalf("instructions = %v", raw["instructions"])
	}
}

func TestSystemMessageMergedIntoInstructions(t *testing.T) {
	req := canon.Request{
		Model:  "gpt-5.6-luna",
		Stream: true,
		Input: []canon.Item{
			canon.Message{Role: canon.RoleSystem, Content: []canon.Content{canon.TextContent{Text: "You are terse."}}},
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
		},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	var raw struct {
		Instructions string `json:"instructions"`
		Input        []struct {
			Role string `json:"role"`
		} `json:"input"`
	}
	if err := json.Unmarshal(result.Body, &raw); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if raw.Instructions != "You are terse." {
		t.Fatalf("instructions = %q, want system text merged", raw.Instructions)
	}
	for _, item := range raw.Input {
		if item.Role == "system" || item.Role == "developer" {
			t.Fatalf("system role leaked into input: %+v", raw.Input)
		}
	}
	if len(raw.Input) != 1 || raw.Input[0].Role != "user" {
		t.Fatalf("input = %+v, want only the user message", raw.Input)
	}
}

func TestAssistantMessageUsesOutputText(t *testing.T) {
	req := canon.Request{
		Model:  "gpt-5.6-luna",
		Stream: true,
		Input: []canon.Item{
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
			canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "on it"}}},
			canon.FunctionCall{CallID: "call-1", Name: "read", Arguments: []byte("{}")},
			canon.FunctionOutput{CallID: "call-1", Output: []canon.Content{canon.TextContent{Text: "data"}}},
		},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	var raw struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(result.Body, &raw); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if len(raw.Input) != 4 {
		t.Fatalf("input has %d items, want 4", len(raw.Input))
	}
	for _, part := range raw.Input[0].Content {
		if part.Type != "input_text" {
			t.Fatalf("user content type = %q, want input_text", part.Type)
		}
	}
	if len(raw.Input[1].Content) != 1 || raw.Input[1].Content[0].Type != "output_text" {
		t.Fatalf("assistant content = %+v, want single output_text", raw.Input[1].Content)
	}
}

func TestEmptyAssistantMessageDropped(t *testing.T) {
	req := canon.Request{
		Model:  "gpt-5.6-luna",
		Stream: true,
		Input: []canon.Item{
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
			canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: ""}}},
			canon.FunctionCall{CallID: "call-1", Name: "read", Arguments: []byte("{}")},
			canon.FunctionOutput{CallID: "call-1", Output: []canon.Content{canon.TextContent{Text: "data"}}},
		},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	var raw struct {
		Input []struct {
			Type string `json:"type"`
			Role string `json:"role"`
		} `json:"input"`
	}
	if err := json.Unmarshal(result.Body, &raw); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	for _, item := range raw.Input {
		if item.Type == "message" && item.Role == "assistant" {
			t.Fatalf("empty assistant message leaked into input: %+v", raw.Input)
		}
	}
	if len(raw.Input) != 3 {
		t.Fatalf("input has %d items, want 3", len(raw.Input))
	}
}

func TestLocalShellToolOmitsName(t *testing.T) {
	req := canon.Request{
		Model:  "gpt-5.6-luna",
		Stream: true,
		Input: []canon.Item{
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
		},
		Tools: []canon.Tool{canon.LocalShellToolDef{}},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	var raw struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(result.Body, &raw); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if len(raw.Tools) != 1 {
		t.Fatalf("tools = %+v, want 1", raw.Tools)
	}
	if raw.Tools[0]["type"] != "local_shell" {
		t.Fatalf("tool = %+v, want local_shell", raw.Tools[0])
	}
	if _, ok := raw.Tools[0]["name"]; ok {
		t.Fatalf("local_shell tool carries name: %+v", raw.Tools[0])
	}
}

func TestCustomToolFormatsAndToolSearchRender(t *testing.T) {
	req := canon.Request{
		Model:  "gpt-5.6-luna",
		Stream: true,
		Input: []canon.Item{
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
		},
		Tools: []canon.Tool{
			canon.CustomToolDef{Name: "apply_patch", Grammar: &canon.ToolGrammar{Syntax: "lark", Definition: "start: A"}},
			canon.CustomToolDef{Name: "freeform", Format: canon.FormatText},
			canon.ToolSearchToolDef{Limit: 5},
		},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	var raw struct {
		Tools []struct {
			Type   string         `json:"type"`
			Name   string         `json:"name"`
			Format map[string]any `json:"format"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result.Body, &raw); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if len(raw.Tools) != 2 {
		t.Fatalf("tools = %d, want 2 (tool_search dropped: upstream rejects it without deferred tools)", len(raw.Tools))
	}
	if raw.Tools[0].Format["type"] != "grammar" || raw.Tools[0].Format["syntax"] != "lark" {
		t.Fatalf("grammar tool = %+v", raw.Tools[0])
	}
	if raw.Tools[1].Format["type"] != "text" {
		t.Fatalf("text tool = %+v", raw.Tools[1])
	}
}

func ptrFloat(v float64) *float64 { return &v }
func ptrBool(v bool) *bool        { return &v }
