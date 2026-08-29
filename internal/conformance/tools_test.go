package conformance_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"prism/internal/canon"
	"prism/internal/conformance"
	"prism/internal/conformance/codec/chat"
	"prism/internal/conformance/codec/responses"
)

func fixtureFile() string {
	return filepath.Join("..", "..", "cmd", "prism-wirecheck", "fixtures", "protocol-v1-cases.json")
}

func TestToolsCoreSuitePasses(t *testing.T) {
	a, err := conformance.Load(fixtureFile())
	if err != nil {
		t.Fatal(err)
	}
	runner := conformance.NewRoutedRunner(chat.Builder{}, responses.Builder{})
	opts := conformance.Options{BaseURL: "https://api.openai.com/v1", APIKey: "fixture-key"}
	toolsCases := 0
	for _, c := range a.Cases {
		if c.Suite != "tools-core" {
			continue
		}
		toolsCases++
		res := runner.Run(context.Background(), c, opts)
		if !res.Passed {
			t.Fatalf("%s failed: %+v diag=%v", c.ID, res, res.Diagnostics)
		}
		if res.Classification != conformance.ClassInconclusive || res.SecondaryCode != "unclassified" {
			t.Fatalf("%s classification %s/%s", c.ID, res.Classification, res.SecondaryCode)
		}
		for _, r := range res.AssertionResults {
			if !r.Passed {
				t.Fatalf("%s assertion %s failed: %s", c.ID, r.ID, r.Reason)
			}
		}
	}
	if toolsCases != 5 {
		t.Fatalf("tools-core cases %d want 5", toolsCases)
	}
}
func TestChatToolRoundTripWire(t *testing.T) {
	got, err := (chat.Builder{}).Build(context.Background(), canon.Request{
		Model: "fixture-model",
		Input: []canon.Item{
			canon.FunctionCall{CallID: "call_fixture", Name: "lookup", Arguments: []byte(`{"q":"x"}`)},
			canon.FunctionOutput{CallID: "call_fixture", Output: []canon.Content{canon.TextContent{Text: "RESULT"}}},
		},
		Stream: false,
	}, conformance.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"fixture-model","messages":[{"role":"assistant","content":"","tool_calls":[{"id":"call_fixture","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},{"role":"tool","tool_call_id":"call_fixture","content":"RESULT"}],"stream":false}`
	if string(got.Body) != want {
		t.Fatalf("body %s", got.Body)
	}
}

func TestChatAllowedToolsWire(t *testing.T) {
	parameters := json.RawMessage(`{"type":"object"}`)
	got, err := (chat.Builder{}).Build(context.Background(), canon.Request{
		Model: "fixture-model",
		Input: []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "PING"}}}},
		Tools: []canon.Tool{
			canon.FunctionTool{Name: "alpha", Parameters: parameters},
			canon.FunctionTool{Name: "beta", Parameters: parameters},
		},
		ToolChoice: canon.ToolAllowed{Mode: canon.AllowedRequired, Tools: []canon.ToolName{"beta"}},
		Stream:     false,
	}, conformance.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"fixture-model","messages":[{"role":"user","content":"PING"}],"stream":false,"tools":[{"type":"function","function":{"name":"beta","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"beta"}}}`
	if string(got.Body) != want {
		t.Fatalf("body %s", got.Body)
	}
}

func TestResponsesCustomToolWire(t *testing.T) {
	got, err := (responses.Builder{}).Build(context.Background(), canon.Request{
		Model: "fixture-model",
		Input: []canon.Item{
			canon.CustomToolCall{CallID: "call_patch", Name: "apply_patch", Input: "*** Begin Patch\n*** End Patch\n"},
			canon.CustomToolOutput{CallID: "call_patch", Output: "Done"},
		},
		Tools: []canon.Tool{
			canon.CustomToolDef{
				Name:    "apply_patch",
				Format:  canon.FormatGrammar,
				Grammar: &canon.ToolGrammar{Syntax: "lark", Definition: "start: /[\\s\\S]+/"},
			},
		},
		Stream: false,
	}, conformance.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"fixture-model","input":[{"type":"custom_tool_call","call_id":"call_patch","name":"apply_patch","input":"*** Begin Patch\n*** End Patch\n"},{"type":"custom_tool_call_output","call_id":"call_patch","output":"Done"}],"stream":false,"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: /[\\s\\S]+/"}}]}`
	if string(got.Body) != want {
		t.Fatalf("body %s", got.Body)
	}
}
