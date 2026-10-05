package management

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/config"
)

const fullPolicyBody = `{"id":"codex","wire":"codex","models":["m1","m2"],"disabledModels":["m2"],"enabled":false,` +
	`"pool":{"pinnedAccount":"acct-1","accountsPath":"accounts.json"},` +
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
	if p.Pool.PinnedAccount != "acct-1" || p.Pool.AccountsPath != "accounts.json" {
		t.Fatalf("pool round-trip: got %+v", p.Pool)
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
	if p.Pool == nil || p.Pool.PinnedAccount != "acct-1" || p.Pool.AccountsPath != "accounts.json" {
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
	if got.Pool == nil || got.Pool.AccountsPath != "accounts.json" || len(got.DisabledModels) != 1 {
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
	if body.Provider.Pool == nil || body.Provider.Pool.PinnedAccount != "acct-1" {
		t.Fatalf("pool lost on explicit replace: got %+v", body.Provider.Pool)
	}
}

func TestProviderReplaceRejectsInvalidPoolSettings(t *testing.T) {
	env := newEnv(t)
	if rec := env.do(t, http.MethodPost, "/api/v1/providers", fullPolicyBody); rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	bad := fmt.Sprintf(`{"wire":"codex","pool":{"pinnedAccount":"acct 1"},"expectedGeneration":%d}`, 1)
	rec := env.do(t, http.MethodPut, "/api/v1/providers/codex", bad)
	assertErrorBody(t, rec, http.StatusBadRequest, "invalid_document")
}

func createWaitProvider(t *testing.T, env *testEnv, wait string) {
	t.Helper()
	body := `{"id":"router","wire":"codex","models":["m1"],"wait":` + wait + `,"expectedGeneration":0}`
	if rec := env.do(t, http.MethodPost, "/api/v1/providers", body); rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func listedWait(t *testing.T, env *testEnv) *config.WaitSettings {
	t.Helper()
	rec := env.do(t, http.MethodGet, "/api/v1/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	listed := decodeBody[ProvidersResponse](t, rec)
	if len(listed.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(listed.Providers))
	}
	return listed.Providers[0].Wait
}

func TestProviderWaitRoundTripsExplicitZeroAndOmission(t *testing.T) {
	env := newEnv(t)
	createWaitProvider(t, env, `{"idleMs":0}`)
	rec := env.do(t, http.MethodGet, "/api/v1/providers", "")
	if !strings.Contains(rec.Body.String(), `"wait":{"idleMs":0}`) {
		t.Fatalf("wire body lost explicit zero or invented firstProgressMs: %s", rec.Body.String())
	}
	w := listedWait(t, env)
	if w == nil || w.FirstProgressMs != nil || w.IdleMs == nil || *w.IdleMs != 0 {
		t.Fatalf("wait = %+v, want only idleMs=0", w)
	}
}

func TestProviderReplaceWithoutWaitPreservesIt(t *testing.T) {
	env := newEnv(t)
	createWaitProvider(t, env, `{"firstProgressMs":900000,"idleMs":0}`)
	rec := env.do(t, http.MethodPut, "/api/v1/providers/router", `{"wire":"codex","enabled":true,"expectedGeneration":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle status = %d, body=%s", rec.Code, rec.Body.String())
	}
	for name, w := range map[string]*config.WaitSettings{
		"mutation response": decodeBody[ProviderMutationResponse](t, rec).Provider.Wait,
		"list":              listedWait(t, env),
	} {
		if w == nil || w.FirstProgressMs == nil || *w.FirstProgressMs != 900000 || w.IdleMs == nil || *w.IdleMs != 0 {
			t.Fatalf("%s: wait erased by partial write: %+v", name, w)
		}
	}
}

func TestProviderReplaceWaitReplacesWholeObject(t *testing.T) {
	env := newEnv(t)
	createWaitProvider(t, env, `{"firstProgressMs":900000,"idleMs":60000}`)
	rec := env.do(t, http.MethodPut, "/api/v1/providers/router", `{"wire":"codex","wait":{"idleMs":5000},"expectedGeneration":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace status = %d, body=%s", rec.Code, rec.Body.String())
	}
	w := listedWait(t, env)
	if w == nil || w.FirstProgressMs != nil || w.IdleMs == nil || *w.IdleMs != 5000 {
		t.Fatalf("wait = %+v, want only idleMs=5000 (omitted field must reset to default)", w)
	}
}

func TestProviderReplaceEmptyWaitResetsToDefaults(t *testing.T) {
	env := newEnv(t)
	createWaitProvider(t, env, `{"firstProgressMs":0,"idleMs":0}`)
	rec := env.do(t, http.MethodPut, "/api/v1/providers/router", `{"wire":"codex","wait":{},"expectedGeneration":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if w := listedWait(t, env); w != nil {
		t.Fatalf("wait = %+v after reset, want absent", w)
	}
}

func TestProviderWaitRejectsInvalidBudgetsWithoutChangingStoredValue(t *testing.T) {
	for _, bad := range []string{
		`{"firstProgressMs":-1}`,
		`{"idleMs":-5}`,
		`{"idleMs":9223372036854775807}`,
	} {
		env := newEnv(t)
		createWaitProvider(t, env, `{"idleMs":1000}`)
		rec := env.do(t, http.MethodPut, "/api/v1/providers/router", `{"wire":"codex","wait":`+bad+`,"expectedGeneration":1}`)
		assertErrorBody(t, rec, http.StatusBadRequest, "invalid_document")
		if w := listedWait(t, env); w == nil || w.IdleMs == nil || *w.IdleMs != 1000 {
			t.Fatalf("%s: rejected write changed stored wait: %+v", bad, w)
		}
	}
}
