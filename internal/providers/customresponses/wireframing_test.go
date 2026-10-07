package customresponses

import (
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

const multibyteText = "héllo 日本語 🎉 ok"

func responsesStreamBody() string {
	var b strings.Builder
	b.WriteString("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n")
	for _, r := range multibyteText {
		fmt.Fprintf(&b, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":%q}\n\n", string(r))
	}
	fmt.Fprintf(&b, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", multibyteText)
	b.WriteString("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	return b.String()
}

func streamedText(events []canon.Event) string {
	var out strings.Builder
	for _, ev := range events {
		if d, ok := ev.(canon.TextDelta); ok {
			out.WriteString(d.Text)
		}
	}
	return out.String()
}

func TestStreamSurvivesByteWiseWritesInsideMultibyteCharacters(t *testing.T) {
	body := responsesStreamBody()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < len(body); i++ {
			_, _ = w.Write([]byte{body[i]})
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	events := &collector{}
	if err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := streamedText(events.events); got != multibyteText {
		t.Fatalf("text = %q, want %q", got, multibyteText)
	}
}

func TestStreamAndAggregateDecodeGzipBodies(t *testing.T) {
	aggregate := `{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"` + multibyteText + `"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	for _, tc := range []struct {
		name, contentType, body string
		stream                  bool
	}{
		{"stream", "text/event-stream", responsesStreamBody(), true},
		{"aggregate", "application/json", aggregate, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Content-Encoding", "gzip")
				zw := gzip.NewWriter(w)
				_, _ = zw.Write([]byte(tc.body))
				_ = zw.Close()
			}))
			defer srv.Close()
			events := &collector{}
			if err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(tc.stream), Target: testTarget(srv.URL)}, events); err != nil {
				t.Fatalf("Run: %v", err)
			}
			var text string
			for _, ev := range events.events {
				switch e := ev.(type) {
				case canon.TextDelta:
					text += e.Text
				case canon.ItemFinished:
					if m, ok := e.Item.(canon.Message); ok && !tc.stream {
						for _, c := range m.Content {
							if tc, ok := c.(canon.TextContent); ok {
								text += tc.Text
							}
						}
					}
				}
			}
			if text != multibyteText {
				t.Fatalf("text = %q, want %q", text, multibyteText)
			}
		})
	}
}
