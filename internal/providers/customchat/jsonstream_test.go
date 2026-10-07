package customchat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

// A gateway that ignores stream:true and answers with one JSON document must
// still produce a completed turn.
func TestStreamRequestAnsweredWithJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	defer srv.Close()
	c := &collector{}
	err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, c)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var text string
	for _, ev := range c.events {
		if d, ok := ev.(canon.TextDelta); ok {
			text += d.Text
		}
	}
	fin, ok := c.events[len(c.events)-1].(canon.TurnFinished)
	if text != "hi" || !ok || fin.Usage.TotalTokens != 4 {
		t.Fatalf("text=%q last=%T", text, c.events[len(c.events)-1])
	}
}
