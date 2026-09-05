package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/integrations"
	"prism/internal/provider"
	"prism/internal/quota"
)

const testSecret = "sk-test-credential-value"

type fakePool struct {
	accounts  []account.Account
	pauseErr  error
	resumeErr error
	prioErr   error

	pauseCalled    bool
	resumeCalled   bool
	priorityCalled bool
	lastVersion    account.StateVersion
	lastPriority   int
	lastID         account.AccountID

	deleteErr    error
	deleteCalled bool
}

func (p *fakePool) Acquire(ctx context.Context, req account.AcquireRequest) (account.Lease, error) {
	return account.Lease{}, errors.New("not used")
}

func (p *fakePool) Record(ctx context.Context, l account.Lease, o account.Outcome) error {
	return nil
}

func (p *fakePool) Pause(ctx context.Context, id account.AccountID, ifVersion account.StateVersion) error {
	p.pauseCalled = true
	p.lastID = id
	p.lastVersion = ifVersion
	return p.pauseErr
}

func (p *fakePool) Resume(ctx context.Context, id account.AccountID, ifVersion account.StateVersion) error {
	p.resumeCalled = true
	p.lastID = id
	p.lastVersion = ifVersion
	return p.resumeErr
}

func (p *fakePool) UpdatePriority(ctx context.Context, id account.AccountID, prio int, ifVersion account.StateVersion) error {
	p.priorityCalled = true
	p.lastID = id
	p.lastPriority = prio
	p.lastVersion = ifVersion
	return p.prioErr
}

func (p *fakePool) Snapshot() account.Snapshot {
	return account.Snapshot{Accounts: append([]account.Account(nil), p.accounts...)}
}

func (p *fakePool) DeleteAccount(ctx context.Context, id account.AccountID) error {
	p.deleteCalled = true
	p.lastID = id
	return p.deleteErr
}

type fakeCreds struct {
	store     map[string][]byte
	putErr    error
	deleteErr error
}

func (c *fakeCreds) Put(ctx context.Context, id string, secret []byte) error {
	if c.putErr != nil {
		return c.putErr
	}
	c.store[id] = secret
	return nil
}

func (c *fakeCreds) Delete(ctx context.Context, id string) error {
	if c.deleteErr != nil {
		return c.deleteErr
	}
	delete(c.store, id)
	return nil
}

func (c *fakeCreds) Configured(ctx context.Context, id string) (bool, error) {
	_, ok := c.store[id]
	return ok, nil
}

type fakeCatalog struct {
	models []provider.Model
	err    error
}

func (c *fakeCatalog) Models(ctx context.Context) ([]provider.Model, error) {
	return c.models, c.err
}

type fakeQuotaSource struct {
	snapshots map[account.AccountID]quota.Snapshot
}

func (q *fakeQuotaSource) Quota(ctx context.Context, id account.AccountID) (quota.Snapshot, error) {
	s, ok := q.snapshots[id]
	if !ok {
		return quota.Snapshot{}, account.ErrNotFound
	}
	return s, nil
}

type fakeAuth struct {
	start        auth.AuthStart
	status       auth.AuthStatus
	callbackErr  error
	lastCallback auth.AuthCallback
}

func (a *fakeAuth) Start(ctx context.Context, provider account.ProviderID) (auth.AuthStart, error) {
	return a.start, nil
}

func (a *fakeAuth) Complete(ctx context.Context, provider account.ProviderID, cb auth.AuthCallback) error {
	a.lastCallback = cb
	return a.callbackErr
}

func (a *fakeAuth) Status(ctx context.Context, provider account.ProviderID, session auth.AuthSessionID) (auth.AuthStatus, error) {
	return a.status, nil
}

type testEnv struct {
	srv     *Server
	handler http.Handler
	pool    *fakePool
	creds   *fakeCreds
	auth    *fakeAuth
	cfg     *config.Manager
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	m, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	pool := &fakePool{
		accounts: []account.Account{
			{
				ID:       "a1",
				Provider: "codex",
				Priority: 1,
				State:    account.Active,
				CredGen:  3,
				Version:  7,
				Quota: quota.Snapshot{
					Used:      120,
					Limit:     int64Ptr(500),
					WindowEnd: time.Unix(1_700_000_000, 0).UTC(),
					Source:    quota.SourceHeader,
				},
				InFlight: 2,
			},
			{
				ID:       "a2",
				Provider: "antigravity",
				Priority: 2,
				State:    account.Paused,
				Version:  4,
			},
		},
	}
	creds := &fakeCreds{store: map[string][]byte{}}
	fake := &fakeAuth{
		start:  auth.AuthStart{Session: "s1", URL: "https://oauth.example.com/authorize"},
		status: auth.AuthStatus{State: auth.StatusAuthorized},
	}
	catalog := &fakeCatalog{models: []provider.Model{
		{ID: "gpt-5.3", Alias: "gpt-5.3-codex", Caps: provider.ModelCaps{Reasoning: true, CustomTools: true}},
	}}
	srv := New(pool, m, catalog, &fakeQuotaSource{snapshots: map[account.AccountID]quota.Snapshot{
		"a1": {Used: 42, Limit: int64Ptr(100), Source: quota.SourceEndpoint},
	}}, creds, fake, integrations.NewRegistry())
	return &testEnv{srv: srv, handler: srv.Handler(), pool: pool, creds: creds, auth: fake, cfg: m}
}

func int64Ptr(v int64) *int64 { return &v }

func (e *testEnv) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %d: %v; body=%s", rec.Code, err, rec.Body.String())
	}
	return v
}

func assertErrorBody(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body.String())
	}
	var eb ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &eb); err != nil {
		t.Fatalf("error body decode: %v; body=%s", err, rec.Body.String())
	}
	if eb.Error.Code != code {
		t.Fatalf("error code = %q, want %q", eb.Error.Code, code)
	}
}

func TestHealthReturnsOK(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodGet, "/api/v1/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeBody[HealthResponse](t, rec)
	if got.Status != "ok" {
		t.Fatalf("status = %q, want ok", got.Status)
	}
}

func TestModelsRenderCatalog(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodGet, "/api/v1/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeBody[ModelsResponse](t, rec)
	if len(got.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(got.Models))
	}
	m := got.Models[0]
	if m.ID != "gpt-5.3" || m.Alias != "gpt-5.3-codex" || !m.Caps.Reasoning || !m.Caps.CustomTools {
		t.Fatalf("model mismatch: %+v", m)
	}
}

func TestProvidersListMasksCredentials(t *testing.T) {
	env := newEnv(t)
	if _, err := env.cfg.Update(config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"codex": {Wire: config.WireCodex, BaseURL: "https://api.example.com", DefaultModel: "gpt-5.3", Models: []string{"gpt-5.3"}},
		},
	}, 0); err != nil {
		t.Fatal(err)
	}
	env.creds.store["codex"] = []byte(testSecret)
	rec := env.do(t, http.MethodGet, "/api/v1/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeBody[ProvidersResponse](t, rec)
	if got.Generation != 1 {
		t.Fatalf("generation = %d, want 1", got.Generation)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(got.Providers))
	}
	p := got.Providers[0]
	if p.Credential.State != "set" {
		t.Fatalf("credential state = %q, want set", p.Credential.State)
	}
	if strings.Contains(rec.Body.String(), testSecret) {
		t.Fatal("response leaks credential")
	}
}

func TestProviderCreateWritesCredentialAndConfig(t *testing.T) {
	env := newEnv(t)
	body := fmt.Sprintf(`{"id":"codex","wire":"codex","credential":%q,"expectedGeneration":0}`, testSecret)
	rec := env.do(t, http.MethodPost, "/api/v1/providers", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[ProviderMutationResponse](t, rec)
	if got.Generation != 1 {
		t.Fatalf("generation = %d, want 1", got.Generation)
	}
	if got.Provider.Credential.State != "set" {
		t.Fatalf("credential state = %q, want set", got.Provider.Credential.State)
	}
	if string(env.creds.store["codex"]) != testSecret {
		t.Fatalf("credential store missing secret: %q", env.creds.store["codex"])
	}
	if env.cfg.Get().Config.Providers["codex"].Wire != config.WireCodex {
		t.Fatal("config document not updated")
	}
	dup := env.do(t, http.MethodPost, "/api/v1/providers", body)
	assertErrorBody(t, dup, http.StatusConflict, "already_exists")
}

func TestProviderReplaceAndStaleGeneration(t *testing.T) {
	env := newEnv(t)
	create := env.do(t, http.MethodPost, "/api/v1/providers", `{"id":"codex","wire":"codex","expectedGeneration":0}`)
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", create.Code, create.Body.String())
	}
	stale := env.do(t, http.MethodPut, "/api/v1/providers/codex", `{"wire":"codex","defaultModel":"gpt-5.3","expectedGeneration":0}`)
	assertErrorBody(t, stale, http.StatusConflict, "stale_generation")
	ok := env.do(t, http.MethodPut, "/api/v1/providers/codex", `{"wire":"codex","defaultModel":"gpt-5.3","expectedGeneration":1}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("replace status = %d, body=%s", ok.Code, ok.Body.String())
	}
	got := decodeBody[ProviderMutationResponse](t, ok)
	if got.Generation != 2 || got.Provider.DefaultModel != "gpt-5.3" {
		t.Fatalf("replace mismatch: generation=%d provider=%+v", got.Generation, got.Provider)
	}
	missing := env.do(t, http.MethodPut, "/api/v1/providers/ghost", `{"wire":"codex","expectedGeneration":2}`)
	assertErrorBody(t, missing, http.StatusNotFound, "not_found")
}

func TestProviderCreateInvalidDocument(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodPost, "/api/v1/providers", `{"id":"bad","wire":"bogus","expectedGeneration":0}`)
	assertErrorBody(t, rec, http.StatusBadRequest, "invalid_document")
}

func TestProviderDelete(t *testing.T) {
	env := newEnv(t)
	if env.do(t, http.MethodPost, "/api/v1/providers", `{"id":"codex","wire":"codex","credential":"c1","expectedGeneration":0}`).Code != http.StatusOK {
		t.Fatal("create failed")
	}
	rec := env.do(t, http.MethodDelete, "/api/v1/providers/codex?expectedGeneration=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[GenerationResponse](t, rec)
	if got.Generation != 2 {
		t.Fatalf("generation = %d, want 2", got.Generation)
	}
	if _, ok := env.creds.store["codex"]; ok {
		t.Fatal("credential not deleted")
	}
	missing := env.do(t, http.MethodDelete, "/api/v1/providers/codex?expectedGeneration=2", "")
	assertErrorBody(t, missing, http.StatusNotFound, "not_found")
	noGen := env.do(t, http.MethodDelete, "/api/v1/providers/codex", "")
	assertErrorBody(t, noGen, http.StatusBadRequest, "invalid_generation")
}

func TestAccountsListRendersSnapshot(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodGet, "/api/v1/accounts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeBody[AccountsResponse](t, rec)
	if len(got.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(got.Accounts))
	}
	a := got.Accounts[0]
	if a.ID != "a1" || a.State != "active" || a.Version != 7 || a.CredentialGeneration != 3 || a.InFlight != 2 || a.Priority != 1 {
		t.Fatalf("account mismatch: %+v", a)
	}
	if a.Quota.Used != 120 || a.Quota.Limit == nil || *a.Quota.Limit != 500 || a.Quota.Source != "header" {
		t.Fatalf("quota mismatch: %+v", a.Quota)
	}
	if got.Accounts[1].State != "paused" {
		t.Fatalf("a2 state = %q, want paused", got.Accounts[1].State)
	}
}

func TestAccountMutationsDelegateCAS(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodPost, "/api/v1/accounts/a1/pause", `{"version":7}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pause status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !env.pool.pauseCalled || env.pool.lastID != "a1" || env.pool.lastVersion != 7 {
		t.Fatalf("pause delegation mismatch: called=%t id=%q version=%d", env.pool.pauseCalled, env.pool.lastID, env.pool.lastVersion)
	}
	rec = env.do(t, http.MethodPost, "/api/v1/accounts/a1/resume", `{"version":8}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !env.pool.resumeCalled || env.pool.lastID != "a1" || env.pool.lastVersion != 8 {
		t.Fatalf("resume delegation mismatch: called=%t id=%q version=%d", env.pool.resumeCalled, env.pool.lastID, env.pool.lastVersion)
	}
	rec = env.do(t, http.MethodPost, "/api/v1/accounts/a1/priority", `{"version":9,"priority":5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("priority status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !env.pool.priorityCalled || env.pool.lastID != "a1" || env.pool.lastVersion != 9 || env.pool.lastPriority != 5 {
		t.Fatalf("priority delegation mismatch: called=%t id=%q version=%d priority=%d", env.pool.priorityCalled, env.pool.lastID, env.pool.lastVersion, env.pool.lastPriority)
	}
}

func TestAccountMutationsStaleAndMissing(t *testing.T) {
	env := newEnv(t)
	env.pool.pauseErr = account.ErrStale
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/accounts/a1/pause", `{"version":1}`), http.StatusConflict, "stale_version")
	env.pool.pauseErr = account.ErrNotFound
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/accounts/a1/pause", `{"version":1}`), http.StatusNotFound, "not_found")
	env.pool.resumeErr = account.ErrStale
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/accounts/a1/resume", `{"version":1}`), http.StatusConflict, "stale_version")
	env.pool.resumeErr = account.ErrNotFound
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/accounts/a1/resume", `{"version":1}`), http.StatusNotFound, "not_found")
	env.pool.prioErr = account.ErrStale
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/accounts/a1/priority", `{"version":1}`), http.StatusConflict, "stale_version")
	env.pool.prioErr = account.ErrNotFound
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/accounts/a1/priority", `{"version":1}`), http.StatusNotFound, "not_found")
}

func TestAccountQuotaFromSource(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodGet, "/api/v1/accounts/a1/quota", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[QuotaResponse](t, rec)
	if got.Account != "a1" || got.Quota.Used != 42 || got.Quota.Source != "endpoint" {
		t.Fatalf("quota mismatch: %+v", got)
	}
	missing := env.do(t, http.MethodGet, "/api/v1/accounts/ghost/quota", "")
	assertErrorBody(t, missing, http.StatusNotFound, "not_found")
}

func TestAccountDeleteDelegatesAndUnsupported(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodDelete, "/api/v1/accounts/a1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !env.pool.deleteCalled || env.pool.lastID != "a1" {
		t.Fatal("delete not delegated")
	}
	env.pool.deleteErr = account.ErrNotFound
	assertErrorBody(t, env.do(t, http.MethodDelete, "/api/v1/accounts/a1", ""), http.StatusNotFound, "not_found")

	plainPool := &noDeletePool{}
	srv := New(plainPool, env.cfg, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}}, nil, integrations.NewRegistry())
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/a1", strings.NewReader(""))
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req)
	assertErrorBody(t, rec2, http.StatusNotImplemented, "unsupported")
}

type noDeletePool struct {
	accounts []account.Account
}

func (p *noDeletePool) Acquire(ctx context.Context, req account.AcquireRequest) (account.Lease, error) {
	return account.Lease{}, errors.New("not used")
}

func (p *noDeletePool) Record(ctx context.Context, l account.Lease, o account.Outcome) error {
	return nil
}

func (p *noDeletePool) Pause(ctx context.Context, id account.AccountID, ifVersion account.StateVersion) error {
	return nil
}

func (p *noDeletePool) Resume(ctx context.Context, id account.AccountID, ifVersion account.StateVersion) error {
	return nil
}

func (p *noDeletePool) UpdatePriority(ctx context.Context, id account.AccountID, prio int, ifVersion account.StateVersion) error {
	return nil
}

func (p *noDeletePool) Snapshot() account.Snapshot {
	return account.Snapshot{Accounts: p.accounts}
}

func TestComboWritesDelegatedToConfigGeneration(t *testing.T) {
	env := newEnv(t)
	if env.do(t, http.MethodPost, "/api/v1/providers", `{"id":"codex","wire":"codex","models":["gpt-5.3"],"expectedGeneration":0}`).Code != http.StatusOK {
		t.Fatal("provider seed failed")
	}
	rec := env.do(t, http.MethodPut, "/api/v1/combos/main", `{"targets":[{"provider":"codex","model":"gpt-5.3","weight":1}],"strategy":"failover","stickyLimit":2,"expectedGeneration":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("combo put status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[CombosResponse](t, rec)
	if got.Generation != 2 || len(got.Combos) != 1 || got.Combos[0].ID != "main" || got.Combos[0].StickyLimit != 2 {
		t.Fatalf("combo mismatch: %+v", got)
	}
	stale := env.do(t, http.MethodPut, "/api/v1/combos/main", `{"targets":[{"provider":"codex","model":"gpt-5.3","weight":1}],"strategy":"failover","expectedGeneration":1}`)
	assertErrorBody(t, stale, http.StatusConflict, "stale_generation")
	invalid := env.do(t, http.MethodPut, "/api/v1/combos/main", `{"targets":[{"provider":"codex","model":"gpt-5.3","weight":1}],"strategy":"bogus","expectedGeneration":2}`)
	assertErrorBody(t, invalid, http.StatusBadRequest, "invalid_document")
	del := env.do(t, http.MethodDelete, "/api/v1/combos/main?expectedGeneration=2", "")
	if del.Code != http.StatusOK {
		t.Fatalf("combo delete status = %d, body=%s", del.Code, del.Body.String())
	}
	if len(decodeBody[CombosResponse](t, del).Combos) != 0 {
		t.Fatal("combo not deleted")
	}
	missing := env.do(t, http.MethodDelete, "/api/v1/combos/main?expectedGeneration=3", "")
	assertErrorBody(t, missing, http.StatusNotFound, "not_found")
}

func TestRouteWritesDelegatedToConfigGeneration(t *testing.T) {
	env := newEnv(t)
	if env.do(t, http.MethodPost, "/api/v1/providers", `{"id":"codex","wire":"codex","models":["gpt-5.3"],"expectedGeneration":0}`).Code != http.StatusOK {
		t.Fatal("provider seed failed")
	}
	rec := env.do(t, http.MethodPut, "/api/v1/routes/default", `{"value":"codex/gpt-5.3","expectedGeneration":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("route put status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[RoutesResponse](t, rec)
	if got.Generation != 2 || got.Routes["default"] != "codex/gpt-5.3" {
		t.Fatalf("route mismatch: %+v", got)
	}
	stale := env.do(t, http.MethodPut, "/api/v1/routes/default", `{"value":"codex/gpt-5.3","expectedGeneration":1}`)
	assertErrorBody(t, stale, http.StatusConflict, "stale_generation")
	invalid := env.do(t, http.MethodPut, "/api/v1/routes/default", `{"value":"ghost/model","expectedGeneration":2}`)
	assertErrorBody(t, invalid, http.StatusBadRequest, "invalid_document")
	del := env.do(t, http.MethodDelete, "/api/v1/routes/default?expectedGeneration=2", "")
	if del.Code != http.StatusOK {
		t.Fatalf("route delete status = %d, body=%s", del.Code, del.Body.String())
	}
	list := decodeBody[RoutesResponse](t, env.do(t, http.MethodGet, "/api/v1/routes", ""))
	if _, ok := list.Routes["default"]; ok {
		t.Fatal("route not deleted")
	}
}

func TestUsageRendersQuotaDetails(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodGet, "/api/v1/usage", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	raw := rec.Body.String()
	if !strings.Contains(raw, `"used":0`) {
		t.Fatalf("usage drops zero used: %s", raw)
	}
	if !strings.Contains(raw, `"limit":null`) {
		t.Fatalf("usage drops nil limit: %s", raw)
	}
	got := decodeBody[UsageResponse](t, rec)
	if len(got.Accounts) != 2 {
		t.Fatalf("usage accounts = %d, want 2", len(got.Accounts))
	}
	u := got.Accounts[0]
	if u.Account != "a1" || u.Provider != "codex" || u.State != "active" || u.Used != 120 || u.Source != "header" {
		t.Fatalf("usage mismatch: %+v", u)
	}
}

func TestResponsesNeverLeakCredentials(t *testing.T) {
	env := newEnv(t)
	create := fmt.Sprintf(`{"id":"codex","wire":"codex","credential":%q,"expectedGeneration":0}`, testSecret)
	rec := env.do(t, http.MethodPost, "/api/v1/providers", create)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	priority := env.do(t, http.MethodPost, "/api/v1/accounts/a1/priority", `{"version":7,"priority":3}`)
	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/health", ""},
		{http.MethodGet, "/api/v1/models", ""},
		{http.MethodGet, "/api/v1/providers", ""},
		{http.MethodGet, "/api/v1/accounts", ""},
		{http.MethodGet, "/api/v1/accounts/a1/quota", ""},
		{http.MethodGet, "/api/v1/combos", ""},
		{http.MethodGet, "/api/v1/routes", ""},
		{http.MethodGet, "/api/v1/usage", ""},
	}
	bodies := map[string]string{"POST /api/v1/providers": rec.Body.String(), "POST /api/v1/accounts/a1/priority": priority.Body.String()}
	for _, ep := range endpoints {
		res := env.do(t, ep.method, ep.path, ep.body)
		bodies[ep.method+" "+ep.path] = res.Body.String()
	}
	for name, body := range bodies {
		walkForLeaks(t, name, body)
	}
	for name, body := range bodies {
		var v any
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("%s: body not JSON: %v", name, err)
		}
		checkCredentialMasking(t, name, v)
	}
}

func walkForLeaks(t *testing.T, name, body string) {
	t.Helper()
	if strings.Contains(body, testSecret) {
		t.Errorf("%s response leaks credential: %s", name, body)
	}
}

func checkCredentialMasking(t *testing.T, name string, v any) {
	t.Helper()
	switch val := v.(type) {
	case map[string]any:
		for k, sub := range val {
			if k == "credential" {
				switch m := sub.(type) {
				case string:
					if m != "set" && m != "unset" {
						t.Errorf("%s: credential field not masked: %#v", name, sub)
					}
				case map[string]any:
					state, ok := m["state"].(string)
					if !ok || (state != "set" && state != "unset") {
						t.Errorf("%s: credential field not masked: %#v", name, sub)
					}
				default:
					t.Errorf("%s: credential field not masked: %#v", name, sub)
				}
				continue
			}
			checkCredentialMasking(t, name, sub)
		}
	case []any:
		for _, sub := range val {
			checkCredentialMasking(t, name, sub)
		}
	}
}

func TestUnknownRouteAndWrongMethod(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodGet, "/api/v1/nope", "")
	assertErrorBody(t, rec, http.StatusNotFound, "not_found")
	rec = env.do(t, http.MethodPatch, "/api/v1/health", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	rec = env.do(t, http.MethodDelete, "/api/v1/combos/main", "")
	assertErrorBody(t, rec, http.StatusBadRequest, "invalid_generation")
}

func TestMalformedJSONBody(t *testing.T) {
	env := newEnv(t)
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/accounts/a1/pause", "{"), http.StatusBadRequest, "malformed_json")
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/providers", "not json"), http.StatusBadRequest, "malformed_json")
	assertErrorBody(t, env.do(t, http.MethodPut, "/api/v1/combos/main", `{"targets":`), http.StatusBadRequest, "malformed_json")
}

func TestAuthRoutesDelegate(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodPost, "/api/v1/auth/codex/start", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body=%s", rec.Code, rec.Body.String())
	}
	start := decodeBody[AuthStartResponse](t, rec)
	if start.Session != "s1" || start.URL != "https://oauth.example.com/authorize" {
		t.Fatalf("start = %+v", start)
	}
	rec = env.do(t, http.MethodPost, "/api/v1/auth/codex/callback", `{"session":"s1","code":"abc","state":"st1"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("callback status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if env.auth.lastCallback.Session != "s1" || env.auth.lastCallback.Code != "abc" || env.auth.lastCallback.State != "st1" {
		t.Fatalf("callback = %+v", env.auth.lastCallback)
	}
	rec = env.do(t, http.MethodGet, "/api/v1/auth/codex/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	got := decodeBody[AuthStatusResponse](t, rec)
	if got.Provider != "codex" || got.State != "authorized" {
		t.Fatalf("auth status mismatch: %+v", got)
	}
}

func TestAuthRoutesWithoutSeam(t *testing.T) {
	env := newEnv(t)
	env.srv.auth = nil
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/auth/codex/start", ""), http.StatusNotImplemented, "unsupported")
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/auth/codex/callback", `{"code":"x"}`), http.StatusNotImplemented, "unsupported")
	assertErrorBody(t, env.do(t, http.MethodGet, "/api/v1/auth/codex/status", ""), http.StatusNotImplemented, "unsupported")
}
