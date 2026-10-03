package customchat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestConfiguredHeadersWinOverForwarded(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()
	r := New(staticKey, Options{ExtraHeaders: []Header{{Name: "User-Agent", Value: "Cline/3.0.49"}}})
	h := http.Header{}
	h.Set("User-Agent", "client-cli/1.0")
	fs, err := execution.NewForwardSet(h)
	if err != nil {
		t.Fatal(err)
	}
	facts := execution.Facts{}
	facts.Forward = fs
	target := provider.Target{Provider: "p", Wire: provider.WireChat, BaseURL: srv.URL, APIKeyRef: "k"}
	err = r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: target, Facts: facts}, &collector{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("User-Agent") != "Cline/3.0.49" {
		t.Fatalf("User-Agent = %q, want configured value to win", got.Get("User-Agent"))
	}
}

func TestForwardedHeadersStillAppliedWithoutExtras(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()
	r := New(staticKey, Options{})
	h := http.Header{}
	h.Set("User-Agent", "client-cli/1.0")
	h.Set("X-Session-Id", "sess-1")
	fs, err := execution.NewForwardSet(h)
	if err != nil {
		t.Fatal(err)
	}
	facts := execution.Facts{}
	facts.Forward = fs
	target := provider.Target{Provider: "p", Wire: provider.WireChat, BaseURL: srv.URL, APIKeyRef: "k"}
	err = r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: target, Facts: facts}, &collector{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("User-Agent") != "client-cli/1.0" || got.Get("X-Session-Id") != "sess-1" {
		t.Fatalf("forwarded headers lost: %v", got)
	}
}
