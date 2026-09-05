package management

import (
	"fmt"
	"net/http"
	"testing"
)

const fullPolicyBody = `{"id":"codex","wire":"codex","models":["m1","m2"],"disabledModels":["m2"],"enabled":false,` +
	`"pool":{"strategy":"round_robin","autoSwitch":false,"autoSwitchThreshold":0.9,"affinity":"off","pinnedAccount":"acct-1","maxFailovers":2},` +
	`"expectedGeneration":0}`

func TestProviderCreateRoundTripsPolicyFields(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodPost, "/api/v1/providers", fullPolicyBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody[ProviderMutationResponse](t, rec)
	p := body.Provider
	if p.Enabled == nil || *p.Enabled {
		t.Fatalf("enabled round-trip: got %v, want false", p.Enabled)
	}
	if len(p.DisabledModels) != 1 || p.DisabledModels[0] != "m2" {
		t.Fatalf("disabledModels round-trip: got %v", p.DisabledModels)
	}
	if p.Pool == nil {
		t.Fatal("pool round-trip: got nil")
	}
	if p.Pool.Strategy != "round_robin" || p.Pool.AutoSwitchThreshold != 0.9 || p.Pool.Affinity != "off" || p.Pool.PinnedAccount != "acct-1" || p.Pool.MaxFailovers != 2 {
		t.Fatalf("pool round-trip: got %+v", p.Pool)
	}
	if p.Pool.AutoSwitch == nil || *p.Pool.AutoSwitch {
		t.Fatalf("pool autoSwitch round-trip: got %v, want false", p.Pool.AutoSwitch)
	}
}

func TestProviderReplacePartialTogglePreservesFields(t *testing.T) {
	env := newEnv(t)
	if rec := env.do(t, http.MethodPost, "/api/v1/providers", fullPolicyBody); rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	toggle := env.do(t, http.MethodPut, "/api/v1/providers/codex", `{"wire":"codex","enabled":true,"expectedGeneration":1}`)
	if toggle.Code != http.StatusOK {
		t.Fatalf("toggle status = %d, body=%s", toggle.Code, toggle.Body.String())
	}
	body := decodeBody[ProviderMutationResponse](t, toggle)
	p := body.Provider
	if p.Enabled == nil || !*p.Enabled {
		t.Fatalf("enabled toggle: got %v, want true", p.Enabled)
	}
	if len(p.Models) != 2 || p.Models[0] != "m1" || p.Models[1] != "m2" {
		t.Fatalf("models erased by partial toggle: got %v", p.Models)
	}
	if len(p.DisabledModels) != 1 || p.DisabledModels[0] != "m2" {
		t.Fatalf("disabledModels erased by partial toggle: got %v", p.DisabledModels)
	}
	if p.Pool == nil || p.Pool.Strategy != "round_robin" || p.Pool.PinnedAccount != "acct-1" {
		t.Fatalf("pool erased by partial toggle: got %+v", p.Pool)
	}

	list := env.do(t, http.MethodGet, "/api/v1/providers", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d", list.Code)
	}
	listed := decodeBody[ProvidersResponse](t, list)
	if len(listed.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(listed.Providers))
	}
	got := listed.Providers[0]
	if got.Pool == nil || got.Pool.Strategy != "round_robin" || len(got.DisabledModels) != 1 {
		t.Fatalf("persisted fields not round-tripped through list: %+v", got)
	}
}

func TestProviderReplaceExplicitEmptyModelsClears(t *testing.T) {
	env := newEnv(t)
	if rec := env.do(t, http.MethodPost, "/api/v1/providers", fullPolicyBody); rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec := env.do(t, http.MethodPut, "/api/v1/providers/codex", `{"wire":"codex","models":[],"disabledModels":[],"expectedGeneration":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody[ProviderMutationResponse](t, rec)
	if len(body.Provider.Models) != 0 {
		t.Fatalf("explicit empty models not cleared: got %v", body.Provider.Models)
	}
	if body.Provider.Pool == nil || body.Provider.Pool.Strategy != "round_robin" {
		t.Fatalf("pool lost on explicit replace: got %+v", body.Provider.Pool)
	}
}

func TestProviderReplaceRejectsInvalidPoolSettings(t *testing.T) {
	env := newEnv(t)
	if rec := env.do(t, http.MethodPost, "/api/v1/providers", fullPolicyBody); rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	bad := fmt.Sprintf(`{"wire":"codex","pool":{"strategy":"round_robin","affinity":"least_loaded"},"expectedGeneration":%d}`, 1)
	rec := env.do(t, http.MethodPut, "/api/v1/providers/codex", bad)
	assertErrorBody(t, rec, http.StatusBadRequest, "invalid_document")
}
