package anthropic

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

// The messages runner always asks for a stream; a gateway that ignores that
// and replies with one message document must still yield a finished turn.
func TestJSONMessageBodyForStreamRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"toolu_1","name":"get","input":{"a":1}}],"stop_reason":"tool_use","usage":{"input_tokens":4,"output_tokens":2}}`))
	}))
	defer server.Close()
	sink := &collectingSink{}
	err := New(Options{BaseURL: server.URL}).Run(t.Context(), provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var text string
	var call canon.FunctionCall
	var fin canon.TurnFinished
	for _, ev := range sink.events {
		switch e := ev.(type) {
		case canon.ItemFinished:
			switch it := e.Item.(type) {
			case canon.Message:
				text = it.Content[0].(canon.TextContent).Text
			case canon.FunctionCall:
				call = it
			}
		case canon.TurnFinished:
			fin = e
		}
	}
	if text != "hello" || call.Name != "get" || string(call.Arguments) != `{"a":1}` || fin.Usage.InputTokens != 4 {
		t.Fatalf("text=%q call=%+v fin=%+v", text, call, fin)
	}
}
