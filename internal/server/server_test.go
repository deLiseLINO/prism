package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/config"
	"prism/internal/provider"
	"prism/internal/routing"
)

func TestManagementProxied(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUnauthorizedNonLoopback(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	req2.Header.Set("Authorization", "Bearer anything")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusUnauthorized {
		t.Fatalf("bearer rejected: %d", rec2.Code)
	}
}

func TestModelsEndpoint(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	doc := config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"p1": {Wire: config.WireOpenAIResponses, BaseURL: "http://example.invalid"},
		},
		Routes:  map[string]string{"prism-auto": "p1/m1"},
		Aliases: map[string]string{"prism-flash": "p1/m2"},
		Combos: map[string]config.Combo{
			"fast-pair": {Targets: []config.Target{{Provider: "p1", Model: "m1"}}, Strategy: config.ComboFailover},
		},
	}
	if _, err := h.cfg.Update(doc, 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"prism-auto", "prism-flash", "fast-pair"} {
		if !strings.Contains(body, want) {
			t.Fatalf("models missing %q: %s", want, body)
		}
	}
}

func TestCountTokensLocalEstimateDeterministic(t *testing.T) {
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"claude-prism-p1--m1": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", &fakeRunner{}); err != nil {
			t.Fatal(err)
		}
	})
	body := `{"model":"claude-prism-p1--m1","max_tokens":64,"messages":[{"role":"user","content":"count my tokens please"}]}`
	rec1 := postJSON(t, h, "/v1/messages/count_tokens", body)
	rec2 := postJSON(t, h, "/v1/messages/count_tokens", body)
	if rec1.Code != http.StatusOK || rec2.Code != http.StatusOK {
		t.Fatalf("status = %d %d", rec1.Code, rec2.Code)
	}
	if rec1.Body.String() != rec2.Body.String() {
		t.Fatalf("estimates differ: %s vs %s", rec1.Body.String(), rec2.Body.String())
	}
	if !strings.Contains(rec1.Body.String(), `"input_tokens"`) {
		t.Fatalf("body = %s", rec1.Body.String())
	}
	var parsed struct {
		InputTokens int64 `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parsed.InputTokens <= 0 {
		t.Fatalf("estimate = %d", parsed.InputTokens)
	}
}

func TestCountTokensAnswersLocallyWithoutDispatch(t *testing.T) {
	counter := &countingRunner{fakeRunner: &fakeRunner{}, tokens: 42}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"claude-prism-p1--m1": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", counter); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/messages/count_tokens", `{"model":"claude-prism-p1--m1","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"input_tokens":`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if counter.callsMade() != 0 {
		t.Fatalf("count_tokens dispatched %d upstream runs, want 0", counter.callsMade())
	}
}

func TestCompactLocalDeterministic(t *testing.T) {
	script := fakeScript{events: []canon.Event{
		canon.ItemStarted{Item: messageAssistant("m1", "Summary of the conversation.")},
		canon.TextDelta{ItemID: "m1", Text: "Summary of the conversation."},
		canon.ItemFinished{Item: messageAssistant("m1", "Summary of the conversation.")},
		canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}},
	}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", &fakeRunner{scripts: []fakeScript{script, script}}); err != nil {
			t.Fatal(err)
		}
	})
	body := `{"model":"test-model","input":"compact this conversation"}`
	rec1 := postJSON(t, h, "/v1/responses/compact", body)
	rec2 := postJSON(t, h, "/v1/responses/compact", body)
	if rec1.Code != http.StatusOK || rec2.Code != http.StatusOK {
		t.Fatalf("status = %d %d bodies %s %s", rec1.Code, rec2.Code, rec1.Body.String(), rec2.Body.String())
	}
	if !strings.Contains(rec1.Body.String(), `"output":[`) {
		t.Fatalf("body = %s", rec1.Body.String())
	}
	if !strings.Contains(rec1.Body.String(), "compact this conversation") {
		t.Fatalf("retained user message missing: %s", rec1.Body.String())
	}
	if !strings.Contains(rec1.Body.String(), "Summary of the conversation.") {
		t.Fatalf("summary message missing: %s", rec1.Body.String())
	}
	if rec1.Body.String() != rec2.Body.String() {
		t.Fatalf("compact not deterministic:\n%s\n%s", rec1.Body.String(), rec2.Body.String())
	}
}

func TestCompactProviderCompactor(t *testing.T) {
	comp := &compactingRunner{result: provider.CompactResult{
		Summary: canon.Message{ID: "s1", Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "provider summary"}}},
	}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", comp); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/responses/compact", `{"model":"test-model","input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "provider summary") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if !comp.called {
		t.Fatal("provider compactor not called")
	}
}

func TestConfigPlanner(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	doc := config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"p1": {Wire: config.WireOpenAIResponses, BaseURL: "http://example.invalid"},
			"p2": {Wire: config.WireAnthropicMessages, BaseURL: "http://example.invalid"},
		},
		Routes:  map[string]string{"routed": "p1/m1"},
		Aliases: map[string]string{"claude-prism-p2--m2": "p2/m2"},
		Combos: map[string]config.Combo{
			"pair": {Targets: []config.Target{{Provider: "p1", Model: "m1"}, {Provider: "p2", Model: "m3"}}, Strategy: config.ComboFailover},
		},
	}
	if _, err := h.cfg.Update(doc, 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	planner := NewConfigPlanner(h.cfg)
	plan, ok := planner.Plan("routed")
	if !ok || len(plan.Targets) != 1 || plan.Targets[0].Provider != "p1" || plan.Targets[0].Model != "m1" {
		t.Fatalf("routed plan = %+v ok=%t", plan, ok)
	}
	plan, ok = planner.Plan("claude-prism-p2--m2")
	if !ok || len(plan.Targets) != 1 || plan.Targets[0].Wire != provider.WireMessages {
		t.Fatalf("alias plan = %+v ok=%t", plan, ok)
	}
	plan, ok = planner.Plan("pair")
	if !ok || len(plan.Targets) != 2 {
		t.Fatalf("combo plan = %+v ok=%t", plan, ok)
	}
	plan, ok = planner.Plan("p1/direct")
	if !ok || len(plan.Targets) != 1 || plan.Targets[0].Model != "direct" {
		t.Fatalf("direct plan = %+v ok=%t", plan, ok)
	}
	plan, ok = planner.Plan("claude-prism-p1--derived")
	if !ok || len(plan.Targets) != 1 || plan.Targets[0].Provider != "p1" || plan.Targets[0].Model != "derived" {
		t.Fatalf("derived alias plan = %+v ok=%t", plan, ok)
	}
	if _, ok := planner.Plan("claude-prism-p2--m2"); !ok {
		t.Fatal("explicit alias should win over derived")
	}
	if _, ok := planner.Plan("missing"); ok {
		t.Fatal("unknown model resolved")
	}
}
