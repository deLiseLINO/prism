package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/customchat"
	"github.com/deLiseLINO/prism/internal/routing"
)

const chatSSEDone = "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"late answer\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
	"data: [DONE]\n\n"

func waitUpstream(t *testing.T, headerDelay time.Duration, heartbeats bool) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(headerDelay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if !heartbeats {
			fmt.Fprint(w, chatSSEDone)
			return
		}
		for {
			fmt.Fprint(w, ": ping\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-time.After(10 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func postStream(t *testing.T, ts *httptest.Server, store *fakeUsageStore, plan func(baseURL string) routing.Plan) string {
	t.Helper()
	runner := customchat.New(func(context.Context, provider.Target, account.Lease) (string, error) { return "k", nil }, customchat.Options{})
	h := newTestServerWithUsage(t, nil, map[canon.ModelID]routing.Plan{"p1/m": plan(ts.URL)}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	}, store)
	front := httptest.NewServer(h)
	defer front.Close()
	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"p1/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func chatPlan(w provider.WaitPolicy) func(string) routing.Plan {
	return func(baseURL string) routing.Plan {
		return routing.Plan{Targets: []provider.Target{{Provider: "p1", Wire: provider.WireChat, BaseURL: baseURL, APIKeyRef: "k", Model: "m", Wait: w}}}
	}
}

func lastUsage(t *testing.T, store *fakeUsageStore) (status, reason string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(store.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	records := store.snapshot()
	if len(records) != 1 {
		t.Fatalf("usage records = %d, want 1", len(records))
	}
	return records[0].Status, records[0].Reason
}

func TestDelayedHeadersFitLongFirstBudgetWithShortIdle(t *testing.T) {
	store := &fakeUsageStore{}
	body := postStream(t, waitUpstream(t, 300*time.Millisecond, false), store,
		chatPlan(provider.WaitPolicy{FirstProgress: 5 * time.Second, Idle: 50 * time.Millisecond}))
	if !strings.Contains(body, "late answer") || !strings.Contains(body, "[DONE]") || strings.Contains(body, "upstream_stall") {
		t.Fatalf("want completed answer:\n%s", body)
	}
	if status, _ := lastUsage(t, store); status != "completed" {
		t.Fatalf("usage status = %q, want completed", status)
	}
}

func TestDelayedHeadersStallUnderShortFirstBudget(t *testing.T) {
	store := &fakeUsageStore{}
	body := postStream(t, waitUpstream(t, 5*time.Second, false), store,
		chatPlan(provider.WaitPolicy{FirstProgress: 100 * time.Millisecond, Idle: 5 * time.Second}))
	if strings.Contains(body, "late answer") {
		t.Fatalf("want stall, got answer:\n%s", body)
	}
	if status, reason := lastUsage(t, store); status != "incomplete" || reason != "upstream_stall" {
		t.Fatalf("usage = %q/%q, want incomplete/upstream_stall", status, reason)
	}
}

func TestHeartbeatOnlyStreamIsStillBounded(t *testing.T) {
	store := &fakeUsageStore{}
	start := time.Now()
	postStream(t, waitUpstream(t, 0, true), store,
		chatPlan(provider.WaitPolicy{FirstProgress: 150 * time.Millisecond, Idle: 5 * time.Second}))
	if time.Since(start) > 3*time.Second {
		t.Fatalf("heartbeat-only stream ran %s, want bounded by first budget", time.Since(start))
	}
	if status, reason := lastUsage(t, store); status != "incomplete" || reason != "upstream_stall" {
		t.Fatalf("usage = %q/%q, want incomplete/upstream_stall", status, reason)
	}
}
