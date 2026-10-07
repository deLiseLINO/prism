package customresponses

import (
	"encoding/json"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
)

func bodyMap(t *testing.T, req canon.Request) map[string]any {
	t.Helper()
	raw, err := buildBody(req)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBodyCarriesStructuredOutputVerbosityAndParallelCalls(t *testing.T) {
	off := false
	strict := true
	req := testRequest(true)
	req.Sampling.ParallelToolCalls = &off
	req.Text = canon.TextOutput{
		Verbosity: canon.VerbosityLow,
		Format:    &canon.TextFormat{Type: "json_schema", Name: "out", Schema: []byte(`{"type":"object"}`), Strict: &strict},
	}
	got := bodyMap(t, req)
	if got["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls = %v", got["parallel_tool_calls"])
	}
	text, _ := got["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if text["verbosity"] != "low" || format["type"] != "json_schema" || format["name"] != "out" || format["strict"] != true {
		t.Fatalf("text = %v", text)
	}
	if schema, _ := format["schema"].(map[string]any); schema["type"] != "object" {
		t.Fatalf("schema = %v", format["schema"])
	}
}

func TestDeveloperRoleIsKept(t *testing.T) {
	req := testRequest(true)
	req.Input = []canon.Item{canon.Message{Role: canon.RoleDeveloper, Content: []canon.Content{canon.TextContent{Text: "note"}}}}
	input := bodyMap(t, req)["input"].([]any)
	if role := input[0].(map[string]any)["role"]; role != "developer" {
		t.Fatalf("role = %v, want developer", role)
	}
}

func TestPlainTextCustomToolIsSent(t *testing.T) {
	req := testRequest(true)
	req.Tools = []canon.Tool{canon.CustomToolDef{Name: "notes", Description: "freeform", Format: canon.FormatText}}
	tools := bodyMap(t, req)["tools"].([]any)
	tl := tools[0].(map[string]any)
	format, _ := tl["format"].(map[string]any)
	if tl["type"] != "custom" || format["type"] != "text" {
		t.Fatalf("tool = %v", tl)
	}
}
