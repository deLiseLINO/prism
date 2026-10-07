package anthropic

import (
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

const multibyteText = "héllo 日本語 🎉 ok"

func messagesStreamBody() string {
	var b strings.Builder
	b.WriteString(ssePart("message_start", `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1}}}`))
	b.WriteString(ssePart("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	for _, r := range multibyteText {
		b.WriteString(ssePart("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, string(r))))
	}
	b.WriteString(ssePart("content_block_stop", `{"type":"content_block_stop","index":0}`))
	b.WriteString(ssePart("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`))
	b.WriteString(ssePart("message_stop", `{"type":"message_stop"}`))
	return b.String()
}

func deltaText(events []canon.Event) string {
	var out strings.Builder
	for _, ev := range events {
		if d, ok := ev.(canon.TextDelta); ok {
			out.WriteString(d.Text)
		}
	}
	return out.String()
}

func TestStreamSurvivesByteWiseWritesInsideMultibyteCharacters(t *testing.T) {
	body := messagesStreamBody()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < len(body); i++ {
			_, _ = w.Write([]byte{body[i]})
			w.(http.Flusher).Flush()
		}
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
	if got := deltaText(sink.events); got != multibyteText {
		t.Fatalf("text = %q, want %q", got, multibyteText)
	}
}

func TestStreamDecodesGzipBody(t *testing.T) {
	body := messagesStreamBody()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write([]byte(body))
		_ = zw.Close()
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
	if got := deltaText(sink.events); got != multibyteText {
		t.Fatalf("text = %q, want %q", got, multibyteText)
	}
}
