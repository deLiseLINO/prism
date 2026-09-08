package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/canon"
	"prism/internal/config"
	"prism/internal/provider"
)

type countSink struct{ events []canon.Event }

func (s *countSink) Emit(ev canon.Event) error {
	s.events = append(s.events, ev)
	return nil
}

func wireSwitchDoc(baseURL string, wire config.Wire) config.Document {
	return config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"router": {Wire: wire, BaseURL: baseURL, Models: []string{"glm-5.3"}},
	}}
}

func runAstraTurn(t *testing.T, env *daemonEnv, wire provider.Wire, baseURL string) error {
	t.Helper()
	runner, ok := env.registry.Lookup("router")
	if !ok {
		t.Fatalf("router runner not registered")
	}
	sink := &countSink{}
	return runner.Run(context.Background(), provider.RunRequest{
		Request: canon.Request{
			Model:  "glm-5.3",
			Stream: true,
			Input: []canon.Item{canon.Message{
				ID:      "m1",
				Role:    canon.RoleUser,
				Content: []canon.Content{canon.TextContent{Text: "hi"}},
			}},
		},
		Target: provider.Target{Provider: "router", Wire: wire, BaseURL: baseURL},
	}, sink)
}

func TestWireSwitchSwapsRunnerWithoutRestart(t *testing.T) {
	var responsesHits, chatHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesHits++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"endpoint dead"}}`)
		case "/v1/chat/completions":
			chatHits++
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
			fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	env, mgr := testEnv(t, wireSwitchDoc(srv.URL+"/v1", config.WireOpenAIResponses))
	ctx := context.Background()
	if err := env.ensureProvider(ctx, "router", wireSwitchDoc(srv.URL+"/v1", config.WireOpenAIResponses).Providers["router"]); err != nil {
		t.Fatalf("ensure responses: %v", err)
	}

	err := runAstraTurn(t, env, provider.WireResponses, srv.URL+"/v1")
	var runErr provider.RunError
	if !errors.As(err, &runErr) || responsesHits != 1 {
		t.Fatalf("responses-wire run: err=%v hits=%d, want upstream 400 contact", err, responsesHits)
	}

	snap := mgr.Get()
	doc := wireSwitchDoc(srv.URL+"/v1", config.WireOpenAIChat)
	if _, err := mgr.Update(doc, snap.Generation); err != nil {
		t.Fatalf("switch wire: %v", err)
	}
	env.reconcileOnce(ctx)

	if err := runAstraTurn(t, env, provider.WireChat, srv.URL+"/v1"); err != nil {
		t.Fatalf("chat-wire run after hot switch: %v (chatHits=%d)", err, chatHits)
	}
	if chatHits != 1 {
		t.Fatalf("chat hits = %d, want 1", chatHits)
	}
	if responsesHits != 1 {
		t.Fatalf("responses hits = %d, want 1 (no retry on dead wire)", responsesHits)
	}
}
