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

func TestStreamReasoningFieldVariants(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
		t.Run(field, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"id\":\"x\",\"choices\":[{\"delta\":{%q:\"think\"}}]}\n\n", field)
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()
			c := &collector{}
			if err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, c); err != nil {
				t.Fatal(err)
			}
			var got string
			for _, ev := range c.events {
				if d, ok := ev.(canon.ReasoningDelta); ok {
					got += d.Text
				}
			}
			if got != "think" {
				t.Fatalf("reasoning = %q, want think", got)
			}
		})
	}
}

func TestStreamReasoningAliasesInOneChunkNotDuplicated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"reasoning_content\":\"think\",\"reasoning\":\"think\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	c := &collector{}
	if err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, c); err != nil {
		t.Fatal(err)
	}
	var got string
	for _, ev := range c.events {
		if d, ok := ev.(canon.ReasoningDelta); ok {
			got += d.Text
		}
	}
	if got != "think" {
		t.Fatalf("reasoning = %q, want a single copy", got)
	}
}
