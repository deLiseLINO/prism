package anthropic

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/provider"
	"prism/internal/reasonenv"
)

func unsignedThinkingPayload() string {
	return sse(
		ssePart("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":10}}}`),
		ssePart("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`),
		ssePart("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`),
		ssePart("content_block_stop", `{"type":"content_block_stop","index":0}`),
		ssePart("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`),
		ssePart("message_stop", `{"type":"message_stop"}`),
	)
}

func TestThinkingWithoutUpstreamSignatureMintsEnvelope(t *testing.T) {
	sink, runner := runAgainst(t, unsignedThinkingPayload())
	var got canon.ReasoningItem
	for _, ev := range sink.events {
		if fin, ok := ev.(canon.ItemFinished); ok {
			if item, ok := fin.Item.(canon.ReasoningItem); ok {
				got = item
			}
		}
	}
	if got.ID == "" {
		t.Fatalf("no reasoning item finished in stream: %#v", sink.events)
	}
	env, ok := reasonenv.Decode(got.Signature)
	if !ok || env.Txt != "hmm" {
		t.Fatalf("signature %q does not decode to thinking text", got.Signature)
	}
	blob, ok := runner.state.get(string(got.ID))
	if !ok || string(blob) != got.Signature {
		t.Fatalf("store blob = %q, want %q", blob, got.Signature)
	}
}

func TestRedactedThinkingRoundTripOnWire(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(fullStreamPayload()))
	}))
	t.Cleanup(server.Close)
	runner := New(Options{BaseURL: server.URL})
	request := baseRequest()
	request.Model = "claude-sonnet-4-5"
	request.Input = []canon.Item{
		canon.ReasoningItem{ID: "r0", Signature: reasonenv.EncodeRedacted([]string{"opaque-blob"})},
		canon.ReasoningItem{ID: "r1", Content: "introspect", Signature: reasonenv.Encode("introspect")},
		canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
	}
	sink := &collectingSink{}
	err := runner.Run(t.Context(), provider.RunRequest{
		Request: request,
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, sink)
	if err != nil {
		t.Fatalf("run with envelope signatures: %v", err)
	}
	if !strings.Contains(body, `"type":"redacted_thinking"`) || !strings.Contains(body, "opaque-blob") {
		t.Fatalf("replayed wire missing redacted_thinking block: %s", body)
	}
	if !strings.Contains(body, `"type":"thinking"`) || !strings.Contains(body, "introspect") {
		t.Fatalf("replayed wire missing plain thinking block: %s", body)
	}
}
