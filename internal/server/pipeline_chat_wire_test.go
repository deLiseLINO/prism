package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/provider"
	"prism/internal/providers/customchat"
	"prism/internal/routing"
)

func TestChatWireInboundChatAndResponsesRoutes(t *testing.T) {
	mockChatUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path = %q, want /v1/chat/completions", req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer mock-key" {
			t.Errorf("upstream auth = %q", req.Header.Get("Authorization"))
		}

		bodyBytes, _ := io.ReadAll(req.Body)
		_ = bodyBytes

		if strings.Contains(string(bodyBytes), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello from chat upstream!\"},\"finish_reason\":null}]}\n\n")
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":5,\"total_tokens\":13}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id": "chatcmpl-nonstream",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "mock-model",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "Hello from aggregate chat upstream!"
				},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 6,
				"completion_tokens": 4,
				"total_tokens": 10
			}
		}`)
	}))
	defer mockChatUpstream.Close()

	keyResolver := func(ctx context.Context, target provider.Target, lease account.Lease) (string, error) {
		return "mock-key", nil
	}
	runner := customchat.New(keyResolver, customchat.Options{})

	target := provider.Target{
		Provider:  "p1",
		Wire:      provider.WireChat,
		BaseURL:   mockChatUpstream.URL,
		APIKeyRef: "key-ref",
		Model:     "mock-model",
	}

	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{
		"p1/mock-model": {Targets: []provider.Target{target}},
	}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})

	// 1. Inbound /v1/chat/completions streaming
	t.Run("inbound chat streaming", func(t *testing.T) {
		rec := postJSON(t, h, "/v1/chat/completions", `{"model":"p1/mock-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Hello from chat upstream!") {
			t.Errorf("chat stream response body missing text:\n%s", body)
		}
	})

	// 2. Inbound /v1/chat/completions aggregate (non-streaming)
	t.Run("inbound chat aggregate", func(t *testing.T) {
		rec := postJSON(t, h, "/v1/chat/completions", `{"model":"p1/mock-model","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Hello from aggregate chat upstream!") {
			t.Errorf("chat aggregate response body missing text:\n%s", body)
		}
	})

	// 3. Inbound /v1/responses streaming
	t.Run("inbound responses streaming", func(t *testing.T) {
		rec := postJSON(t, h, "/v1/responses", `{"model":"p1/mock-model","stream":true,"input":"hi"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "event: response.output_text.delta") || !strings.Contains(body, "Hello from chat upstream!") {
			t.Errorf("responses stream body missing text:\n%s", body)
		}
	})

	// 4. Inbound /v1/responses aggregate
	t.Run("inbound responses aggregate", func(t *testing.T) {
		rec := postJSON(t, h, "/v1/responses", `{"model":"p1/mock-model","stream":false,"input":"hi"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Hello from aggregate chat upstream!") {
			t.Errorf("responses aggregate body missing text:\n%s", body)
		}
	})
}
