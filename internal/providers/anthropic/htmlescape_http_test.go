package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestUpstreamBodyKeepsHTMLCharactersLiteral(t *testing.T) {
	const text = `if a < b && b > c { return "<div>" }`
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Write([]byte(sse(
			ssePart("message_start", `{"type":"message_start","message":{}}`),
			ssePart("message_stop", `{"type":"message_stop"}`),
		)))
	}))
	defer server.Close()
	req := baseRequest()
	req.Input = []canon.Item{
		canon.Message{ID: "m1", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: text}}},
		canon.CustomToolCall{CallID: "c1", Name: "apply_patch", Input: text},
		canon.CustomToolOutput{CallID: "c1", Output: "ok"},
	}
	req.Tools = []canon.Tool{canon.FunctionTool{
		Name:       "read",
		Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string","description":"a <path> & more"}}}`),
	}}
	runner := New(Options{BaseURL: server.URL})
	if err := runner.Run(context.Background(), provider.RunRequest{
		Request: req,
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, &collectingSink{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, escaped := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if bytes.Contains(body, []byte(escaped)) {
			t.Fatalf("upstream body contains %s:\n%s", escaped, body)
		}
	}
	var wire struct {
		Messages []struct {
			Content []struct {
				Text  string          `json:"text"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"messages"`
		Tools []struct {
			InputSchema struct {
				Properties struct {
					Path struct {
						Description string `json:"description"`
					} `json:"path"`
				} `json:"properties"`
			} `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode upstream body: %v\n%s", err, body)
	}
	if got := wire.Messages[0].Content[0].Text; got != text {
		t.Fatalf("message text = %q, want %q", got, text)
	}
	var input struct{ Input string }
	if err := json.Unmarshal(wire.Messages[1].Content[0].Input, &input); err != nil || input.Input != text {
		t.Fatalf("custom tool input = %s (%v), want %q", wire.Messages[1].Content[0].Input, err, text)
	}
	if got := wire.Tools[0].InputSchema.Properties.Path.Description; got != "a <path> & more" {
		t.Fatalf("tool description = %q", got)
	}
}
