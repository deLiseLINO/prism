package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/customchat"
	"github.com/deLiseLINO/prism/internal/routing"
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

func TestChatRouteAcceptsSlashlessRouteAndComboNames(t *testing.T) {
	events := []canon.Event{
		canon.ItemStarted{Item: messageAssistant("m1", "ok")},
		canon.TextDelta{ItemID: "m1", Text: "ok"},
		canon.ItemFinished{Item: messageAssistant("m1", "ok")},
		canon.TurnFinished{Status: canon.Completed()},
	}
	runner := &fakeRunner{scripts: []fakeScript{{events: events}, {events: events}}}
	plans := map[canon.ModelID]routing.Plan{"fast": singlePlan("p1"), "pair": singlePlan("p1")}
	h := newTestServer(t, nil, plans, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	for _, model := range []string{"fast", "pair"} {
		rec := postJSON(t, h, "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
			t.Fatalf("model %q: status = %d body %s", model, rec.Code, rec.Body.String())
		}
	}
	rec := postJSON(t, h, "/v1/chat/completions", `{"model":"nothing","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown model: status = %d body %s, want 404", rec.Code, rec.Body.String())
	}
}

func TestFullyDisabledComboIsNotFoundOnNonStream(t *testing.T) {
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"gone": {}}, nil)
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"gone","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"gone","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		if rec := postJSON(t, h, tc.path, tc.body); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d body %s, want 404", tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestFullyDisabledComboIsNotFoundOnCountTokens(t *testing.T) {
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"gone": {}}, nil)
	rec := postJSON(t, h, "/v1/messages/count_tokens", `{"model":"gone","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body %s, want 404", rec.Code, rec.Body.String())
	}
}
