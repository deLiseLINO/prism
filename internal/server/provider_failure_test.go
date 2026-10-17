package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/customchat"
	"github.com/deLiseLINO/prism/internal/routing"
)

func TestProviderAuthRefusalSurvivesBlockedRetry(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid credential","type":"authentication_error","details":{"request":"mock"}}}`))
	}))
	defer upstream.Close()
	runner := customchat.New(func(context.Context, provider.Target, account.Lease) (string, error) { return "mock", nil }, customchat.Options{Client: upstream.Client()})
	plan := routing.Plan{Targets: []provider.Target{{Provider: "p1", Model: "mock", Wire: provider.WireChat, BaseURL: upstream.URL}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"edge/mock": plan}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	for i := range 2 {
		rec := postJSON(t, h, "/v1/chat/completions", `{"model":"edge/mock","messages":[{"role":"user","content":"hi"}]}`)
		var body struct {
			Error struct {
				Message string            `json:"message"`
				Details map[string]string `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusUnauthorized || body.Error.Message != "Invalid credential" || body.Error.Details["request"] != "mock" {
			t.Fatalf("request %d: HTTP %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("blocked retry made %d upstream calls, want 1", calls)
	}
	pool := h.pool.(interface {
		PublishCredential(account.AccountID, account.CredentialGeneration, bool) error
	})
	if err := pool.PublishCredential("a1", 2, true); err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, h, "/v1/chat/completions", `{"model":"edge/mock","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusUnauthorized || calls != 2 {
		t.Fatalf("replacement did not reach upstream: HTTP %d, calls %d", rec.Code, calls)
	}
}
