package anthropic

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeStructuredSchemaAndForcedThinkingHTTP(t *testing.T) {
	const schema = `{"type":"object","properties":{"count":{"minimum":9007199254740993,"multipleOf":1e-12}},"required":["count"],"additionalProperties":false}`
	captured := make(chan []byte, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured <- raw
		io.WriteString(w, fullStreamPayload())
	}))
	defer up.Close()
	req := baseRequest()
	req.Tools = []canon.Tool{canon.FunctionTool{Name: "read", Parameters: []byte(schema)}}
	req.ToolChoice = canon.ToolNamed{Name: "read"}
	req.Reasoning.Effort = canon.EffortHigh
	req.Text.Format = &canon.TextFormat{Type: "json_schema", Schema: []byte(schema)}
	err := New(Options{HTTP: up.Client()}).Run(t.Context(), provider.RunRequest{Request: req, Target: provider.Target{BaseURL: up.URL}}, &collectingSink{})
	if err != nil {
		t.Fatal(err)
	}
	body := string(<-captured)
	if strings.Contains(body, `"thinking"`) || !strings.Contains(body, `"output_config":{"format":{"type":"json_schema","schema":`+schema) || !strings.Contains(body, `9007199254740993`) || !strings.Contains(body, `1e-12`) || !strings.Contains(body, `"tool_choice":{"type":"tool","name":"read"}`) {
		t.Fatalf("request = %s", body)
	}
}
