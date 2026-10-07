package customchat

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

func chatStreamBody() string {
	var b strings.Builder
	for _, r := range multibyteText {
		fmt.Fprintf(&b, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", string(r))
	}
	b.WriteString("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
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

// Upstream gateways flush at arbitrary byte boundaries, including inside a
// multi-byte character.
func TestStreamSurvivesByteWiseWritesInsideMultibyteCharacters(t *testing.T) {
	body := chatStreamBody()
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
	if got := streamedText(events.All()); got != multibyteText {
		t.Fatalf("text = %q, want %q", got, multibyteText)
	}
}

func TestStreamAndAggregateDecodeGzipBodies(t *testing.T) {
	stream := chatStreamBody()
	aggregate := `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"` + multibyteText + `"},"finish_reason":"stop"}]}`
	for _, tc := range []struct {
		name, contentType, body string
		stream                  bool
	}{
		{"stream", "text/event-stream", stream, true},
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
			if got := streamedText(events.All()); got != multibyteText {
				t.Fatalf("text = %q, want %q", got, multibyteText)
			}
		})
	}
}
