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

func TestRequestBodyStripsPrismR1EnvelopeFromReplay(t *testing.T) {
	envelope := prismReasoningPrefix + "eyJzaWciOiJzaWctYnl0ZXMifQ"
	req := canon.Request{
		Model:  "gpt-5.2-codex",
		Stream: true,
		Input: []canon.Item{
			canon.ReasoningItem{ID: "rs-1", State: canon.OpaqueRef{Store: reasoningStorePRISMR1, Key: envelope}},
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
	input, ok := raw["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input = %v", raw["input"])
	}
	item, _ := input[0].(map[string]any)
	if _, present := item["encrypted_content"]; present {
		t.Fatalf("prismr1 envelope must be stripped for the native backend, got %v", item["encrypted_content"])
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

func ptrFloat(v float64) *float64 { return &v }
func ptrBool(v bool) *bool        { return &v }
