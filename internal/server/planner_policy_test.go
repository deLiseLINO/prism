package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/config"
	"prism/internal/provider"
)

func policyDoc() config.Document {
	autoOff := false
	return config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"p1": {
				Wire:           config.WireOpenAIResponses,
				BaseURL:        "http://example.invalid",
				Models:         []string{"m1", "m2"},
				DisabledModels: []string{"m2"},
				Pool: &config.PoolSettings{
					Strategy:            config.PoolRoundRobin,
					AutoSwitch:          &autoOff,
					AutoSwitchThreshold: 0.9,
					Affinity:            config.AffinityOff,
					PinnedAccount:       "acct-1",
					MaxFailovers:        2,
				},
			},
			"p2": {
				Wire:    config.WireAnthropicMessages,
				BaseURL: "http://example.invalid",
				Models:  []string{"m1"},
				Enabled: boolPtr(false),
			},
		},
		Routes:  map[string]string{"r1": "p1/m1", "r2": "p2/m1"},
		Aliases: map[string]string{"a2": "p1/m2"},
		Combos: map[string]config.Combo{
			"mixed":   {Targets: []config.Target{{Provider: "p1", Model: "m1"}, {Provider: "p2", Model: "m1"}}, Strategy: config.ComboFailover},
			"allgone": {Targets: []config.Target{{Provider: "p2", Model: "m1"}}, Strategy: config.ComboFailover},
		},
	}
}

func boolPtr(v bool) *bool { return &v }

func TestPlannerMapsPoolSettingsIntoTargetPolicy(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	if _, err := h.cfg.Update(policyDoc(), 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	planner := NewConfigPlanner(h.cfg)
	plan, ok := planner.Plan("p1/m1")
	if !ok || len(plan.Targets) != 1 {
		t.Fatalf("direct plan = %+v ok=%t", plan, ok)
	}
	want := account.SelectionPolicy{
		Strategy:            account.StrategyRoundRobin,
		AutoSwitch:          account.AutoSwitchOff,
		AutoSwitchThreshold: 0.9,
		Affinity:            account.AffinityOff,
		PinnedAccount:       "acct-1",
	}
	if plan.Targets[0].Policy != want {
		t.Fatalf("target policy = %+v, want %+v", plan.Targets[0].Policy, want)
	}
	if plan.Targets[0].MaxFailovers != 2 {
		t.Fatalf("max failovers = %d, want 2", plan.Targets[0].MaxFailovers)
	}
}

func TestPlannerOmitsDisabledProvidersAndModels(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	if _, err := h.cfg.Update(policyDoc(), 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	planner := NewConfigPlanner(h.cfg)
	for _, key := range []string{"p1/m2", "p2/m1", "r2", "a2"} {
		if _, ok := planner.Plan(canon.ModelID(key)); ok {
			t.Fatalf("disabled target %q planned", key)
		}
	}
	if _, ok := planner.Plan("r1"); !ok {
		t.Fatal("enabled route not planned")
	}
	plan, ok := planner.Plan("mixed")
	if !ok {
		t.Fatal("mixed combo not planned")
	}
	if len(plan.Targets) != 1 || plan.Targets[0].Provider != "p1" {
		t.Fatalf("mixed combo kept disabled target: %+v", plan.Targets)
	}
	if plan, ok = planner.Plan("allgone"); !ok || len(plan.Targets) != 0 {
		t.Fatalf("fully disabled combo = %+v ok=%t, want empty plan", plan, ok)
	}
}

func TestPlannerKeepsProviderTargetShape(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	if _, err := h.cfg.Update(policyDoc(), 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	planner := NewConfigPlanner(h.cfg)
	plan, ok := planner.Plan("r1")
	if !ok {
		t.Fatal("route not planned")
	}
	t0 := plan.Targets[0]
	if t0.Wire != provider.WireResponses || t0.BaseURL != "http://example.invalid" {
		t.Fatalf("target shape changed: %+v", t0)
	}
}

func TestModelsListOmitsDisabledKeys(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	if _, err := h.cfg.Update(policyDoc(), 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, "/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]bool{}
	for _, m := range out.Data {
		seen[m.ID] = true
	}
	if !seen["r1"] || !seen["mixed"] {
		t.Fatalf("enabled keys missing from /v1/models: %v", seen)
	}
	for _, gone := range []string{"r2", "a2", "allgone"} {
		if seen[gone] {
			t.Fatalf("disabled key %q still listed: %v", gone, seen)
		}
	}
}
