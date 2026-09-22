package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/auth"
	"prism/internal/buildinfo"
	"prism/internal/canon"
	"prism/internal/config"
	"prism/internal/execution"
	"prism/internal/integrations"
	"prism/internal/provider"
	"prism/internal/quota"
	"prism/internal/requestlog"
	"prism/internal/usage"
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
	snapshots  map[account.AccountID]quota.Snapshot
	refreshErr error
}

func (q *fakeQuotaSource) Quota(ctx context.Context, id account.AccountID) (quota.Snapshot, error) {
	s, ok := q.snapshots[id]
	if !ok {
		return quota.Snapshot{}, account.ErrNotFound
	}
	return s, nil
}

func (q *fakeQuotaSource) RefreshQuota(ctx context.Context, id account.AccountID) (quota.Snapshot, error) {
	if q.refreshErr != nil {
		return quota.Snapshot{}, q.refreshErr
	}
	return q.Quota(ctx, id)
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
					Windows: []quota.Window{
						{Label: "5 hour usage limit", Used: 120, Limit: int64Ptr(500), WindowEnd: time.Unix(1_700_000_000, 0).UTC()},
						{Label: "Weekly usage limit", Used: 25, Limit: int64Ptr(1000), WindowEnd: time.Unix(1_700_050_000, 0).UTC()},
					},
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
	}}, creds, fake, integrations.NewRegistry(), nil, nil)
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
	if got.Version != buildinfo.Version {
		t.Fatalf("version = %q, want %q", got.Version, buildinfo.Version)
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
	if len(a.Quota.Windows) != 2 || a.Quota.Windows[0].Label != "5 hour usage limit" || a.Quota.Windows[1].Label != "Weekly usage limit" {
		t.Fatalf("account quota windows mismatch: %+v", a.Quota.Windows)
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

func TestAccountQuotaRefresh(t *testing.T) {
	env := newEnv(t)
	rec := env.do(t, http.MethodPost, "/api/v1/accounts/a1/quota/refresh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[QuotaResponse](t, rec)
	if got.Account != "a1" || got.Quota.Used != 42 || got.Quota.Source != "endpoint" {
		t.Fatalf("quota mismatch: %+v", got)
	}
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/accounts/ghost/quota/refresh", ""), http.StatusNotFound, "not_found")
	assertErrorBody(t, env.do(t, http.MethodGet, "/api/v1/accounts/a1/quota/refresh", ""), http.StatusMethodNotAllowed, "method_not_allowed")
	env.srv.quota = &fakeQuotaSource{refreshErr: errors.New("upstream unavailable")}
	assertErrorBody(t, env.do(t, http.MethodPost, "/api/v1/accounts/a1/quota/refresh", ""), http.StatusBadGateway, "quota_refresh_failed")
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
	srv := New(plainPool, env.cfg, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}}, nil, integrations.NewRegistry(), nil, nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/a1", strings.NewReader(""))
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req)
	assertErrorBody(t, rec2, http.StatusNotImplemented, "unsupported")
}

func journalWithEntries(t *testing.T, count int) *requestlog.Journal {
	t.Helper()
	clock := &fakeJournalClock{t: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
	j := requestlog.New(8, clock.Now)
	for i := range count {
		turn := j.Open(execution.Facts{RequestID: execution.RequestID(fmt.Sprintf("r%d", i)), Client: execution.ClientCodex, Session: "s1"}, canon.ModelID(fmt.Sprintf("m%d", i)))
		started := turn.Now()
		if i%2 == 0 {
			turn.Attempt(requestlog.AttemptInfo{Provider: "codex", AccountID: "codex:a", Model: "m", StartedAt: started, Outcome: requestlog.AttemptServer, Error: "upstream 500"})
			turn.Close(requestlog.Terminal{Status: requestlog.StatusFailed, Failed: true, Reason: canon.FailServerOverloaded})
			continue
		}
		turn.Attempt(requestlog.AttemptInfo{Provider: "codex", AccountID: "codex:a", Model: "m", StartedAt: started, Outcome: requestlog.AttemptSucceeded})
		turn.Close(requestlog.Terminal{Status: requestlog.StatusCompleted, Usage: canon.Usage{TotalTokens: 7}})
	}
	return j
}

type fakeJournalClock struct{ t time.Time }

func (c *fakeJournalClock) Now() time.Time { return c.t }

func TestRequestsNewestFirstWithAttempts(t *testing.T) {
	env := newEnv(t)
	env.srv.SetRequestLog(journalWithEntries(t, 3))
	rec := env.do(t, http.MethodGet, "/api/v1/requests", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody[RequestsResponse](t, rec)
	if len(body.Requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(body.Requests))
	}
	if body.Requests[0].Model != "m2" || body.Requests[1].Model != "m1" || body.Requests[2].Model != "m0" {
		t.Fatalf("order = %s,%s,%s, want newest first m2,m1,m0", body.Requests[0].Model, body.Requests[1].Model, body.Requests[2].Model)
	}
	if body.Dropped != 0 {
		t.Fatalf("dropped = %d, want 0", body.Dropped)
	}
	newest := body.Requests[0]
	if newest.Status != "failed" || newest.Reason != "server_overloaded" {
		t.Fatalf("newest = %+v, want failed server_overloaded", newest)
	}
	if len(newest.Attempts) != 1 || newest.Attempts[0].Outcome != "server" {
		t.Fatalf("newest attempts = %+v, want one server outcome", newest.Attempts)
	}
	if newest.Attempts[0].Error != "upstream 500" {
		t.Fatalf("attempt error = %q, want classified error message", newest.Attempts[0].Error)
	}
	middle := body.Requests[1]
	if middle.Status != "completed" || middle.Reason != "" {
		t.Fatalf("middle = %+v, want completed without reason", middle)
	}
	if middle.Usage.Total != 7 {
		t.Fatalf("middle usage = %+v, want 7 total", middle.Usage)
	}
	if middle.Client != "codex" || middle.RequestID != "r1" || middle.Session != "s1" {
		t.Fatalf("middle identity = %+v", middle)
	}
	if middle.StartedAt != "2026-09-09T12:00:00Z" {
		t.Fatalf("startedAt = %q, want RFC3339 UTC", middle.StartedAt)
	}
}

func TestRequestsLimitClampsAndSlices(t *testing.T) {
	env := newEnv(t)
	env.srv.SetRequestLog(journalWithEntries(t, 3))
	rec := env.do(t, http.MethodGet, "/api/v1/requests?limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody[RequestsResponse](t, rec)
	if len(body.Requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(body.Requests))
	}
	if body.Requests[0].Model != "m2" || body.Requests[1].Model != "m1" {
		t.Fatalf("order = %s,%s, want newest two", body.Requests[0].Model, body.Requests[1].Model)
	}
	rec = env.do(t, http.MethodGet, "/api/v1/requests?limit=99", "")
	body = decodeBody[RequestsResponse](t, rec)
	if len(body.Requests) != 3 {
		t.Fatalf("requests = %d, want all 3 (limit clamped to ring size)", len(body.Requests))
	}
	rec = env.do(t, http.MethodGet, "/api/v1/requests?limit=0", "")
	body = decodeBody[RequestsResponse](t, rec)
	if len(body.Requests) != 3 {
		t.Fatalf("requests = %d, want all 3 (limit 0 means no limit)", len(body.Requests))
	}
	assertErrorBody(t, env.do(t, http.MethodGet, "/api/v1/requests?limit=-1", ""), http.StatusBadRequest, "invalid_limit")
	assertErrorBody(t, env.do(t, http.MethodGet, "/api/v1/requests?limit=x", ""), http.StatusBadRequest, "invalid_limit")
}

func TestRequestsUnconfiguredReturns503(t *testing.T) {
	env := newEnv(t)
	assertErrorBody(t, env.do(t, http.MethodGet, "/api/v1/requests", ""), http.StatusServiceUnavailable, "not_available")
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
	if strings.Contains(raw, `"limit":null`) {
		t.Fatalf("usage emits null limit: %s", raw)
	}
	got := decodeBody[UsageResponse](t, rec)
	if len(got.Accounts) != 2 {
		t.Fatalf("usage accounts = %d, want 2", len(got.Accounts))
	}
	u := got.Accounts[0]
	if u.Account != "a1" || u.Provider != "codex" || u.State != "active" || u.Quota.Used != 120 || u.Quota.Source != "header" {
		t.Fatalf("usage mismatch: %+v", u)
	}
	if len(u.Quota.Windows) != 2 || u.Quota.Windows[0].Label != "5 hour usage limit" || u.Quota.Windows[1].Label != "Weekly usage limit" {
		t.Fatalf("usage windows mismatch: %+v", u.Quota.Windows)
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

func TestContextWindowHierarchyAndGlobalEndpoint(t *testing.T) {
	env := newEnv(t)
	doc := config.Document{
		Version:       config.SchemaVersion,
		ContextWindow: 400000,
		Providers: map[string]config.Provider{
			"codex": {
				Wire:   config.WireCodex,
				Models: []string{"gpt-5.2", "gpt-5.2-codex"},
				ModelSettings: map[string]config.ModelSettings{
					"gpt-5.2": {ContextWindow: 200000, ImageInput: true, ReasoningEfforts: []string{"low", "high"}},
				},
			},
		},
	}
	if _, err := env.cfg.Update(doc, 0); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, http.MethodGet, "/api/v1/providers", "")
	got := decodeBody[ProvidersResponse](t, rec)
	if got.ContextWindow != 400000 {
		t.Fatalf("global contextWindow = %d, want 400000", got.ContextWindow)
	}
	p := got.Providers[0]
	s := p.ModelSettings["gpt-5.2"]
	if s.ContextWindow != 200000 || !s.ImageInput || len(s.ReasoningEfforts) != 2 {
		t.Fatalf("model settings = %+v", s)
	}

	bad := env.do(t, http.MethodPut, "/api/v1/context-window", `{"contextWindow":-1,"expectedGeneration":1}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("negative window status = %d, want 400", bad.Code)
	}
	ok := env.do(t, http.MethodPut, "/api/v1/context-window", `{"contextWindow":96000,"expectedGeneration":1}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200", ok.Code)
	}
	updated := decodeBody[ContextWindowWrite](t, ok)
	if updated.ContextWindow != 96000 || updated.ExpectedGeneration != 2 {
		t.Fatalf("updated = %d/%d, want 96000/2", updated.ContextWindow, updated.ExpectedGeneration)
	}
	if env.cfg.Get().Config.ResolveContextWindow("codex", "gpt-5.2-codex") != 96000 {
		t.Fatal("model without override did not fall back to the global window")
	}
	if env.cfg.Get().Config.ResolveContextWindow("codex", "gpt-5.2") != 200000 {
		t.Fatal("model override lost after global update")
	}
}

func TestVisionSidecarEndpoint(t *testing.T) {
	env := newEnv(t)
	doc := config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"router": {
				Wire:    config.WireOpenAIChat,
				BaseURL: "http://up.example/v1",
				Models:  []string{"glm-5.3", "gpt-5.6-luna"},
				ModelSettings: map[string]config.ModelSettings{
					"gpt-5.6-luna": {ImageInput: true},
				},
			},
		},
	}
	if _, err := env.cfg.Update(doc, 0); err != nil {
		t.Fatal(err)
	}
	bad := env.do(t, http.MethodPut, "/api/v1/vision-sidecar", `{"enabled":true,"target":"router/glm-5.3","expectedGeneration":1}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("text-only sidecar target status = %d, want 400", bad.Code)
	}
	ok := env.do(t, http.MethodPut, "/api/v1/vision-sidecar", `{"enabled":true,"target":"router/gpt-5.6-luna","expectedGeneration":1}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200", ok.Code)
	}
	got := decodeBody[VisionSidecarWrite](t, ok)
	if !got.Enabled || got.Target != "router/gpt-5.6-luna" || got.ExpectedGeneration != 2 {
		t.Fatalf("vision sidecar = %+v", got)
	}
	if s := env.cfg.Get().Config.VisionSidecar; !s.Enabled || s.Target != "router/gpt-5.6-luna" {
		t.Fatalf("config vision sidecar = %+v", s)
	}
	rec := env.do(t, http.MethodGet, "/api/v1/providers", "")
	listed := decodeBody[ProvidersResponse](t, rec)
	if !listed.VisionSidecar.Enabled || listed.VisionSidecar.Target != "router/gpt-5.6-luna" {
		t.Fatalf("listed vision sidecar = %+v", listed.VisionSidecar)
	}
}

type fakeUsageSource struct {
	overview  usage.Aggregate
	models    []usage.ModelAggregate
	providers []usage.ProviderAggregate
	err       error

	lastSince time.Time
	calls     int
}

func (f *fakeUsageSource) Overview(ctx context.Context, since time.Time) (usage.Aggregate, error) {
	f.calls++
	f.lastSince = since
	return f.overview, f.err
}

func (f *fakeUsageSource) ByModel(ctx context.Context, since time.Time) ([]usage.ModelAggregate, error) {
	f.lastSince = since
	return f.models, f.err
}

func (f *fakeUsageSource) ByProvider(ctx context.Context, since time.Time) ([]usage.ProviderAggregate, error) {
	f.lastSince = since
	return f.providers, f.err
}

func (f *fakeUsageSource) Insert(ctx context.Context, rec usage.Record) error { return nil }
func (f *fakeUsageSource) Close() error                                       { return nil }

func TestStatsUnavailableWithoutStore(t *testing.T) {
	env := newEnv(t)
	assertErrorBody(t, env.do(t, http.MethodGet, "/api/v1/stats", ""), http.StatusServiceUnavailable, "stats_unavailable")
}

func TestStatsReturnsAggregates(t *testing.T) {
	env := newEnv(t)
	src := &fakeUsageSource{
		overview: usage.Aggregate{
			Requests: 5, Completed: 4, Failed: 1,
			InputTokens: 1000, OutputTokens: 200, CachedTokens: 300, ReasoningTokens: 50,
			TotalTokens: 1200, Measured: 3,
		},
		models: []usage.ModelAggregate{
			{Model: "gpt-5.3", Provider: "codex", Aggregate: usage.Aggregate{Requests: 4, TotalTokens: 900}},
		},
		providers: []usage.ProviderAggregate{
			{Provider: "codex", Aggregate: usage.Aggregate{Requests: 5, TotalTokens: 1200}},
		},
	}
	env.srv.SetUsageStore(src)
	rec := env.do(t, http.MethodGet, "/api/v1/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[StatsResponse](t, rec)
	if got.Range != "1h" {
		t.Fatalf("default range = %q, want 1h", got.Range)
	}
	if got.Overview.Requests != 5 || got.Overview.TotalTokens != 1200 || got.Overview.Measured != 3 {
		t.Fatalf("overview mismatch: %+v", got.Overview)
	}
	if len(got.Models) != 1 || got.Models[0].Model != "gpt-5.3" || got.Models[0].Provider != "codex" || got.Models[0].TotalTokens != 900 {
		t.Fatalf("models mismatch: %+v", got.Models)
	}
	if len(got.Providers) != 1 || got.Providers[0].Provider != "codex" || got.Providers[0].Requests != 5 {
		t.Fatalf("providers mismatch: %+v", got.Providers)
	}
}

func TestStatsRangeParameter(t *testing.T) {
	env := newEnv(t)
	src := &fakeUsageSource{}
	env.srv.SetUsageStore(src)
	for _, tt := range []struct {
		query   string
		want    string
		allTime bool
	}{
		{"", "1h", false},
		{"range=24h", "24h", false},
		{"range=7d", "7d", false},
		{"range=30d", "30d", false},
		{"range=all", "all", true},
	} {
		rec := env.do(t, http.MethodGet, "/api/v1/stats?"+tt.query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("range %q status = %d, want 200; body=%s", tt.query, rec.Code, rec.Body.String())
		}
		got := decodeBody[StatsResponse](t, rec)
		if got.Range != tt.want {
			t.Fatalf("range %q echoed %q, want %q", tt.query, got.Range, tt.want)
		}
		if tt.allTime {
			if !src.lastSince.IsZero() {
				t.Fatalf("range %q since = %v, want zero", tt.query, src.lastSince)
			}
		} else if src.lastSince.IsZero() {
			t.Fatalf("range %q since = zero, want computed", tt.query)
		}
	}
	assertErrorBody(t, env.do(t, http.MethodGet, "/api/v1/stats?range=bogus", ""), http.StatusBadRequest, "invalid_range")
}

func TestStatsJSONFieldsSnakeCase(t *testing.T) {
	env := newEnv(t)
	src := &fakeUsageSource{overview: usage.Aggregate{Requests: 5}}
	env.srv.SetUsageStore(src)
	rec := env.do(t, http.MethodGet, "/api/v1/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"range"`,
		`"overview"`,
		`"requests"`,
		`"completed"`,
		`"failed"`,
		`"input_tokens"`,
		`"output_tokens"`,
		`"cached_tokens"`,
		`"reasoning_tokens"`,
		`"total_tokens"`,
		`"measured"`,
		`"models"`,
		`"providers"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "inputTokens") || strings.Contains(body, "totalTokens") || strings.Contains(body, "reasoningTokens") {
		t.Fatalf("body contains camelCase field: %s", body)
	}
}

type fakeSyncer struct {
	models map[string][]string
}

func (f *fakeSyncer) RemoteModels(ctx context.Context, id string, p config.Provider) ([]string, error) {
	return f.models[id], nil
}

func TestSyncModelsFoldsRawStateOntoLogicalIds(t *testing.T) {
	m, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	seed := config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"ag": {
				Wire:           config.WireAntigravity,
				Enabled:        &enabled,
				Models:         []string{"gemini-3.7-flash-low", "gemini-3.7-flash-high", "claude-opus-4", "chat_20706"},
				DisabledModels: []string{"gemini-3.7-flash-low"},
				ModelSettings: map[string]config.ModelSettings{
					"gemini-3.7-flash-high": {ContextWindow: 12345, ReasoningEfforts: []string{"low", "high"}},
					"claude-opus-4":         {ImageInput: true},
				},
			},
		},
	}
	snap, err := m.Update(seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&fakePool{}, m, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}},
		&fakeAuth{}, integrations.NewRegistry(), &fakeSyncer{models: map[string][]string{
			"ag": {"gemini-3.7-flash", "claude-opus-4"},
		}}, nil)
	handler := srv.Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/providers/ag/sync-models?expectedGeneration="+
		strconv.FormatUint(snap.Generation, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[ProviderMutationResponse](t, rec)
	p := got.Provider
	if got.Generation != snap.Generation+1 {
		t.Fatalf("generation = %d, want %d", got.Generation, snap.Generation+1)
	}
	wantModels := []string{"claude-opus-4", "gemini-3.7-flash"}
	if !reflect.DeepEqual(p.Models, wantModels) {
		t.Fatalf("models = %v, want %v", p.Models, wantModels)
	}
	if !reflect.DeepEqual(p.SyncedModels, []string{"gemini-3.7-flash", "claude-opus-4"}) {
		t.Fatalf("syncedModels = %v", p.SyncedModels)
	}
	if !reflect.DeepEqual(p.DisabledModels, []string{"gemini-3.7-flash"}) {
		t.Fatalf("disabledModels = %v, want [gemini-3.7-flash]", p.DisabledModels)
	}
	s, ok := p.ModelSettings["gemini-3.7-flash"]
	if !ok || s.ContextWindow != 12345 {
		t.Fatalf("settings folded = %+v, want contextWindow 12345", s)
	}
	if _, raw := p.ModelSettings["gemini-3.7-flash-high"]; raw {
		t.Fatalf("raw settings key survived: %+v", p.ModelSettings)
	}
	if s, ok := p.ModelSettings["claude-opus-4"]; !ok || !s.ImageInput {
		t.Fatalf("non-family settings entry must survive: %+v", p.ModelSettings)
	}
	wantRaw := []string{"gemini-3.7-flash-high", "gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-tiered"}
	if !reflect.DeepEqual(p.RawModels, wantRaw) {
		t.Fatalf("rawModels = %v, want %v", p.RawModels, wantRaw)
	}
	if !reflect.DeepEqual(p.ModelSettings["gemini-3.7-flash"].ReasoningEfforts, []string{"low", "high"}) {
		t.Fatalf("reasoningEfforts did not fold onto the logical id: %+v", p.ModelSettings)
	}

	stored := m.Get()
	storedP := stored.Config.Providers["ag"]
	if _, raw := storedP.ModelSettings["gemini-3.7-flash-high"]; raw {
		t.Fatalf("persisted raw settings key survived: %+v", storedP.ModelSettings)
	}
	if !reflect.DeepEqual(storedP.DisabledModels, []string{"gemini-3.7-flash"}) {
		t.Fatalf("persisted disabledModels = %v", storedP.DisabledModels)
	}
}

func TestFoldRawModelSettingsLeavesOtherWiresAndOwnedIdsAlone(t *testing.T) {
	doc := config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"anthropic": {
			Wire:           config.WireAnthropicMessages,
			Models:         []string{"claude-sonnet-4-6-thinking"},
			DisabledModels: []string{"claude-sonnet-4-6-thinking"},
			ModelSettings:  map[string]config.ModelSettings{"claude-sonnet-4-6-thinking": {ContextWindow: 500}},
		},
	}}
	foldRawModelSettings(&doc, "anthropic")
	p := doc.Providers["anthropic"]
	if _, ok := p.ModelSettings["claude-sonnet-4-6"]; ok {
		t.Fatalf("non-antigravity wire must not fold: %+v", p.ModelSettings)
	}
	if !reflect.DeepEqual(p.DisabledModels, []string{"claude-sonnet-4-6-thinking"}) {
		t.Fatalf("non-antigravity disabled must not fold: %v", p.DisabledModels)
	}

	doc = config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"ag": {
			Wire:           config.WireAntigravity,
			Models:         []string{"gemini-3.7-flash", "grok-5-thinking"},
			DisabledModels: []string{"grok-5-thinking"},
			ModelSettings: map[string]config.ModelSettings{
				"grok-5-thinking": {ContextWindow: 900},
				"gemini-3-flash":  {ContextWindow: 777},
			},
		},
	}}
	foldRawModelSettings(&doc, "ag")
	p = doc.Providers["ag"]
	if _, ok := p.ModelSettings["grok-5"]; ok {
		t.Fatalf("auto-pair ids must not fold statically: %+v", p.ModelSettings)
	}
	if _, ok := p.ModelSettings["grok-5-thinking"]; !ok {
		t.Fatalf("auto-pair id must keep its own entry: %+v", p.ModelSettings)
	}
	if _, ok := p.ModelSettings["gemini-3-flash"]; !ok {
		t.Fatalf("alias id must keep its own entry: %+v", p.ModelSettings)
	}
	if !reflect.DeepEqual(p.DisabledModels, []string{"grok-5-thinking"}) {
		t.Fatalf("table-unowned disabled entry must survive: %v", p.DisabledModels)
	}
}

func TestSyncModelsWithoutSyncerStaysUnsupported(t *testing.T) {
	env := newEnv(t)
	env.cfg.Update(config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"codex": {Wire: config.WireCodex, Models: []string{"gpt-5.3"}},
	}}, 0)
	rec := env.do(t, http.MethodPost, "/api/v1/providers/codex/sync-models?expectedGeneration=1", "")
	assertErrorBody(t, rec, http.StatusNotImplemented, "unsupported")
}

func TestModelModeSwitchRoundTripsModelList(t *testing.T) {
	m, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	seed := config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"ag": {
				Wire:           config.WireAntigravity,
				Enabled:        &enabled,
				Models:         []string{"gemini-3.7-flash", "chat_20706"},
				DisabledModels: []string{"gemini-3.7-flash"},
				SyncedModels:   []string{"gemini-3.7-flash", "chat_20706"},
				ModelSettings: map[string]config.ModelSettings{
					"gemini-3.7-flash": {ContextWindow: 12345},
				},
			},
			"codex": {Wire: config.WireCodex, Enabled: &enabled, Models: []string{"gpt-5.3"}},
		},
	}
	snap, err := m.Update(seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&fakePool{}, m, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}},
		&fakeAuth{}, integrations.NewRegistry(), &fakeSyncer{}, nil)
	handler := srv.Handler()

	switchMode := func(t *testing.T, id string, gen uint64, mode string) Provider {
		t.Helper()
		body := `{"mode":"` + mode + `","expectedGeneration":` + strconv.FormatUint(gen, 10) + `}`
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
			"/api/v1/providers/"+id+"/model-mode?expectedGeneration="+strconv.FormatUint(gen, 10),
			strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("switch %s status = %d, want 200; body=%s", mode, rec.Code, rec.Body.String())
		}
		got := decodeBody[ProviderMutationResponse](t, rec)
		return got.Provider
	}

	raw := switchMode(t, "ag", snap.Generation, "raw")
	wantRawModels := []string{"chat_20706", "gemini-3.7-flash-high", "gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-tiered"}
	if !reflect.DeepEqual(raw.Models, wantRawModels) {
		t.Fatalf("raw models = %v, want %v", raw.Models, wantRawModels)
	}
	if !reflect.DeepEqual(raw.SyncedModels, wantRawModels) {
		t.Fatalf("raw synced = %v, want %v; manual badges would flip on after a sync", raw.SyncedModels, wantRawModels)
	}
	if !reflect.DeepEqual(raw.DisabledModels, wantRawModels[1:]) {
		t.Fatalf("raw disabled = %v, want the expanded family", raw.DisabledModels)
	}
	if !reflect.DeepEqual(raw.RawModels, wantRawModels) {
		t.Fatalf("rawModels = %v, want %v", raw.RawModels, wantRawModels)
	}
	for _, member := range wantRawModels[1:] {
		if s, ok := raw.ModelSettings[member]; !ok || s.ContextWindow != 12345 {
			t.Fatalf("settings fan-out missing %s: %+v", member, raw.ModelSettings)
		}
	}

	stored := m.Get().Config.Providers["ag"]
	if stored.ModelMode != config.ModelModeRaw || !reflect.DeepEqual(stored.Models, wantRawModels) {
		t.Fatalf("persisted raw state = mode %q models %v", stored.ModelMode, stored.Models)
	}

	logical := switchMode(t, "ag", snap.Generation+1, "logical")
	wantLogical := []string{"chat_20706", "gemini-3.7-flash"}
	if !reflect.DeepEqual(logical.Models, wantLogical) {
		t.Fatalf("logical models = %v, want %v", logical.Models, wantLogical)
	}
	if !reflect.DeepEqual(logical.SyncedModels, wantLogical) {
		t.Fatalf("logical synced = %v, want %v; manual badges would flip on after a sync", logical.SyncedModels, wantLogical)
	}
	if !reflect.DeepEqual(logical.DisabledModels, []string{"gemini-3.7-flash"}) {
		t.Fatalf("logical disabled = %v", logical.DisabledModels)
	}
	if s, ok := logical.ModelSettings["gemini-3.7-flash"]; !ok || s.ContextWindow != 12345 {
		t.Fatalf("settings did not fold back: %+v", logical.ModelSettings)
	}

	// The codex wire has no families; the switch refuses rather than rewriting.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/providers/codex/model-mode?expectedGeneration=1", strings.NewReader(`{"mode":"raw"}`)))
	assertErrorBody(t, rec, http.StatusBadRequest, "invalid_value")
}
