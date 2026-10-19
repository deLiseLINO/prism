package anthropic

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
)

func tool(name string) canon.Tool {
	return canon.FunctionTool{Name: canon.ToolName(name), Parameters: []byte(`{"type":"object","properties":{}}`)}
}

// Thinking and a forced tool choice cannot share a request upstream.
func TestForcedToolChoiceDropsThinking(t *testing.T) {
	for name, choice := range map[string]canon.ToolChoice{
		"required": canon.ToolRequired{},
		"named":    canon.ToolNamed{Name: "get"},
	} {
		t.Run(name, func(t *testing.T) {
			req := baseRequest()
			req.Tools = []canon.Tool{tool("get")}
			req.ToolChoice = choice
			req.Reasoning = canon.ReasoningConfig{Effort: canon.EffortHigh}
			wr, err := New(Options{}).buildWireRequest(req, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			if wr.ToolChoice == nil {
				t.Fatal("tool choice was dropped; the client's forced call must be honoured")
			}
			if wr.Thinking != nil {
				t.Fatalf("thinking %+v sent with forced tool_choice %q; upstream rejects the pair", wr.Thinking, wr.ToolChoice.Type)
			}
		})
	}
}

func TestAutoToolChoiceKeepsThinking(t *testing.T) {
	req := baseRequest()
	req.Tools = []canon.Tool{tool("get")}
	req.ToolChoice = canon.ToolAuto{}
	req.Reasoning = canon.ReasoningConfig{Effort: canon.EffortHigh}
	req.Sampling.Temperature, req.Sampling.TopP = nil, nil
	wr, err := New(Options{}).buildWireRequest(req, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Thinking == nil {
		t.Fatal("thinking dropped for tool_choice auto")
	}
}

func TestParallelToolCallsFalseReachesMessagesWire(t *testing.T) {
	req := baseRequest()
	req.Tools = []canon.Tool{tool("get")}
	off := false
	req.Sampling.ParallelToolCalls = &off
	wr, err := New(Options{}).buildWireRequest(req, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if wr.ToolChoice == nil || wr.ToolChoice.DisableParallelToolUse == nil || !*wr.ToolChoice.DisableParallelToolUse {
		t.Fatalf("tool_choice = %+v, want disable_parallel_tool_use=true", wr.ToolChoice)
	}
}
