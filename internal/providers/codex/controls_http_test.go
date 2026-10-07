package codex

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeControlsAndExactHistoryHTTP(t *testing.T) {
	const schema = `{"type":"object","properties":{"n":{"minimum":9007199254740993,"multipleOf":1e-12}}}`
	const arguments = `{"n":9007199254740993,"x":1e-12}`
	captured := make(chan []byte, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured <- raw
		io.WriteString(w, joinFrames(`{"type":"response.completed","response":{}}`))
	}))
	defer up.Close()
	temperature, topP, parallel := 0.2, 0.8, false
	req := canon.Request{Tools: []canon.Tool{canon.FunctionTool{Name: "read", Parameters: []byte(schema)}, canon.CustomToolDef{Name: "write"}}, ToolChoice: canon.ToolAllowed{Mode: canon.AllowedRequired, Tools: []canon.ToolName{"write"}}, Reasoning: canon.ReasoningConfig{Effort: canon.EffortOff}, Text: canon.TextOutput{Verbosity: canon.VerbosityLow, Format: &canon.TextFormat{Type: "json_schema", Schema: []byte(schema)}}, Sampling: canon.Sampling{Temperature: &temperature, TopP: &topP, ParallelToolCalls: &parallel}, Input: []canon.Item{canon.FunctionCall{ID: "fc", CallID: "c1", Name: "read", Arguments: []byte(arguments)}, canon.CustomToolCall{ID: "custom", CallID: "c2", Name: "write", Input: "raw\n原文"}}}
	runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "fixture"}}, Client: up.Client()}
	if err := runner.Run(t.Context(), provider.RunRequest{Request: req, Target: provider.Target{BaseURL: up.URL}}, &captureSink{}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Tools []struct {
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
		ToolChoice struct {
			Type  string `json:"type"`
			Mode  string `json:"mode"`
			Tools []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"tool_choice"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Text struct {
			Verbosity string `json:"verbosity"`
			Format    struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"format"`
		} `json:"text"`
		Input []struct {
			Arguments string `json:"arguments"`
			Input     string `json:"input"`
		} `json:"input"`
		Temperature float64 `json:"temperature"`
		TopP        float64 `json:"top_p"`
		Parallel    bool    `json:"parallel_tool_calls"`
	}
	if err := json.Unmarshal(<-captured, &body); err != nil {
		t.Fatal(err)
	}
	if string(body.Tools[0].Parameters) != schema || string(body.Text.Format.Schema) != schema || body.Input[0].Arguments != arguments || body.Input[1].Input != "raw\n原文" || body.Reasoning.Effort != "none" || body.Text.Verbosity != "low" || body.Temperature != temperature || body.TopP != topP || body.Parallel || body.ToolChoice.Type != "allowed_tools" || body.ToolChoice.Mode != "required" || body.ToolChoice.Tools[0].Type != "custom" {
		t.Fatalf("body=%+v", body)
	}
}
