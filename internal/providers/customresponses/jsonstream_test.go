package customresponses

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestStreamRequestAnsweredWithJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"r1","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)
	}))
	defer srv.Close()
	c := &collector{}
	err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, c)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	fin, ok := c.events[len(c.events)-1].(canon.TurnFinished)
	if !ok || fin.Usage.TotalTokens != 4 {
		t.Fatalf("last event = %#v", c.events[len(c.events)-1])
	}
	var text string
	for _, ev := range c.events {
		if f, ok := ev.(canon.ItemFinished); ok {
			if m, ok := f.Item.(canon.Message); ok && len(m.Content) > 0 {
				text = m.Content[0].(canon.TextContent).Text
			}
		}
	}
	if text != "hi" {
		t.Fatalf("text = %q", text)
	}
}
