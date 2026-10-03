package responses

import (
	"reflect"
	"testing"

	"prism/internal/canon"
)

const namespaceBody = `{"model":"gpt-5.3","input":"hi","tools":[
{"type":"namespace","name":"functions","description":"builtin","tools":[
 {"type":"function","name":"shell","description":"run","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]},"strict":true},
 {"type":"custom","name":"apply_patch","description":"patch"}]},
{"type":"namespace","name":"mcp__x","description":"x","tools":[
 {"type":"function","name":"child","description":"c","parameters":{"type":"object","properties":{"a":{"type":"integer"}}},"strict":false},
 {"type":"custom","name":"free","description":"f"},
 {"type":"namespace","name":"inner","tools":[{"type":"function","name":"deep"}]},
 {"type":"function","name":""},
 {"type":"function","name":"child","description":"c","parameters":{"type":"object","properties":{"a":{"type":"integer"}}}}]}
]}`

func TestNamespaceToolsFlatten(t *testing.T) {
	req, _, _ := mustParse(t, namespaceBody, nil)
	want := []canon.Tool{
		canon.FunctionTool{Name: "shell", Description: "run", Parameters: []byte(`{"properties":{"cmd":{"type":"string"}},"required":["cmd"],"type":"object"}`), Strict: true},
		canon.CustomToolDef{Name: "apply_patch", Description: "patch", Format: canon.FormatText},
		canon.FunctionTool{Name: "mcp__x__child", Description: "c", Parameters: []byte(`{"properties":{"a":{"type":"integer"}},"type":"object"}`)},
		canon.CustomToolDef{Name: "mcp__x__free", Description: "f", Format: canon.FormatText},
	}
	if !reflect.DeepEqual(req.Tools, want) {
		t.Fatalf("tools = %#v\nwant %#v", req.Tools, want)
	}
	wantRoutes := map[canon.ToolName]canon.ToolRoute{
		"mcp__x__child": {Namespace: "mcp__x", Name: "child"},
		"mcp__x__free":  {Namespace: "mcp__x", Name: "free"},
	}
	if !reflect.DeepEqual(req.ToolRoutes, wantRoutes) {
		t.Fatalf("routes = %#v", req.ToolRoutes)
	}
}

func TestNamespaceToolsConflictingDuplicateRejected(t *testing.T) {
	body := `{"model":"m","input":"hi","tools":[
{"type":"namespace","name":"a","tools":[{"type":"function","name":"b__c"}]},
{"type":"namespace","name":"a__b","tools":[{"type":"function","name":"c"}]}]}`
	_, _, _, err := parseBody(t, body, nil)
	pe, ok := err.(*ParseError)
	if !ok || pe.Status != 400 || pe.Field != "tools[1].tools[0].name" {
		t.Fatalf("err = %#v", err)
	}
}

func TestNamespaceBareDuplicateAcrossGroupsDeduped(t *testing.T) {
	body := `{"model":"m","input":"hi","tools":[
{"type":"namespace","name":"functions","tools":[{"type":"function","name":"f"}]},
{"type":"namespace","name":"","tools":[{"type":"function","name":"f"}]}]}`
	req, _, _ := mustParse(t, body, nil)
	if !reflect.DeepEqual(req.Tools, []canon.Tool{canon.FunctionTool{Name: "f"}}) || req.ToolRoutes != nil {
		t.Fatalf("tools = %#v routes = %#v", req.Tools, req.ToolRoutes)
	}
}

func TestReplayedNamespacedCallsUseFlatName(t *testing.T) {
	body := `{"model":"m","input":[
{"type":"function_call","call_id":"c1","name":"child","namespace":"mcp__x","arguments":"{}"},
{"type":"custom_tool_call","call_id":"c2","name":"free","namespace":"mcp__x","input":"i"},
{"type":"function_call","call_id":"c3","name":"shell","namespace":"functions","arguments":"{}"},
{"type":"function_call","call_id":"c4","name":"plain","arguments":"{}"}]}`
	req, _, _ := mustParse(t, body, nil)
	var names []canon.ToolName
	for _, it := range req.Input {
		switch c := it.(type) {
		case canon.FunctionCall:
			names = append(names, c.Name)
		case canon.CustomToolCall:
			names = append(names, c.Name)
		}
	}
	want := []canon.ToolName{"mcp__x__child", "mcp__x__free", "shell", "plain"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v", names)
	}
}
