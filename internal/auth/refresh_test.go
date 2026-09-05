package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/store"
)

// --- harness ---

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: testNow()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// tokenServer serves the provider token endpoint with a configurable JSON
// payload and status; a non-nil hold blocks each token response until closed.
type tokenServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	count   int
	status  int
	payload []byte
	hold    chan struct{}
	entered chan struct{}
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	ts := &tokenServer{entered: make(chan struct{}, 64), status: http.StatusOK}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tokenEndpoint := r.URL.Path == "/" // the same sandbox also serves project APIs
		ts.mu.Lock()
		if tokenEndpoint {
			ts.count++
		}
		status, payload, hold := ts.status, ts.payload, ts.hold
		ts.mu.Unlock()
		if tokenEndpoint {
			ts.entered <- struct{}{}
			if hold != nil {
				<-hold
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if payload != nil {
			_, _ = w.Write(payload)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

func (ts *tokenServer) respond(status int, payload string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.status, ts.payload = status, []byte(payload)
}

func (ts *tokenServer) requests() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.count
}

// harness wires a store, repository, runtime pool, flow, and refresher for one
// provider against a sandbox directory.
type harness struct {
	t        *testing.T
	clock    *fakeClock
	dir      string
	file     *store.FileCredentialStore
	repo     *account.Repository
	provider account.ProviderID
	pool     interface {
		account.Pool
		Register(account.Account)
		AdvanceGeneration(account.AccountID, account.CredentialGeneration) error
		MarkNeedsReauth(account.AccountID) error
	}
	ref    *Refresher
	sink   *FileSink
	acc    account.AccountID
	lease  account.Lease
	tokens *tokenServer
}

func newRefreshHarness(t *testing.T, provider account.ProviderID) *harness {
	t.Helper()
	clock := newFakeClock()
	dir := t.TempDir()
	file := store.NewFileCredentialStore(dir)
	repo := account.OpenMeta(filepath.Join(dir, "accounts.json"))
	pool := account.New([]byte("test-secret"), clock.Now)
	var cfg ProviderConfig
	var flow Flow
	var err error
	tokens := newTokenServer(t)
	switch provider {
	case "codex":
		cfg = CodexProduction
		cfg.TokenURL = tokens.srv.URL
		flow, err = NewCodexFlow(cfg, Options{Now: clock.Now})
	default:
		cfg = AntigravityProduction
		cfg.TokenURL = tokens.srv.URL
		cfg.ProjectAPI = tokens.srv.URL
		cfg.OnboardAPI = tokens.srv.URL
		flow, err = NewAntigravityFlow(cfg, Options{Now: clock.Now, OnboardPoll: time.Millisecond})
	}
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	flows := map[account.ProviderID]Flow{provider: flow}
	ref, err := NewRefresher(RefresherOptions{
		File:  file,
		Repos: map[account.ProviderID]*account.Repository{provider: repo},
		Pool:  pool,
		Flows: flows,
		Now:   clock.Now,
		Wait:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("refresher: %v", err)
	}
	acc := AccountRowID(provider, "identity-1")
	sink := NewFileSink(file, map[account.ProviderID]*account.Repository{provider: repo}, pool)
	return &harness{
		t: t, clock: clock, dir: dir, file: file, repo: repo, provider: provider, pool: pool,
		ref: ref, sink: sink, acc: acc, tokens: tokens,
		lease: account.Lease{Provider: provider, Account: acc},
	}
}

func (h *harness) seed(cred account.Credential) {
	h.t.Helper()
	if err := h.file.PutIdempotent(context.Background(), h.provider, h.acc, 1, cred.Encode()); err != nil {
		h.t.Fatalf("seed blob: %v", err)
	}
	if err := h.repo.Ensure(h.provider, h.acc, 1, cred.Email); err != nil {
		h.t.Fatalf("seed row: %v", err)
	}
	h.pool.Register(account.Account{ID: h.acc, Provider: h.provider, State: account.Active, CredGen: 1, Version: 1})
	h.lease = account.Lease{Provider: h.provider, Account: h.acc, CredGen: 1, Version: 1}
}

func (h *harness) repoGen() account.CredentialGeneration {
	h.t.Helper()
	gen, err := h.repo.CurrentGeneration(h.provider, h.acc)
	if err != nil {
		h.t.Fatalf("current generation: %v", err)
	}
	return gen
}

func (h *harness) poolAccount() account.Account {
	h.t.Helper()
	for _, a := range h.pool.Snapshot().Accounts {
		if a.ID == h.acc {
			return a
		}
	}
	h.t.Fatalf("account %s missing from pool", h.acc)
	return account.Account{}
}

func (h *harness) blobAt(gen account.CredentialGeneration) (account.Credential, bool) {
	h.t.Helper()
	blob, ok, err := h.file.Get(context.Background(), h.provider, h.acc, gen)
	if err != nil || !ok {
		return account.Credential{}, false
	}
	cred, err := account.ParseCredential(blob)
	if err != nil {
		h.t.Fatalf("parse blob: %v", err)
	}
	return cred, true
}

func expiringCodex() account.Credential {
	return account.Credential{
		Access:    "old-access-token",
		Refresh:   "old-refresh-grant",
		ExpiresAt: testNow().Add(30 * time.Second),
		AccountID: "identity-1",
		Email:     "user@example.com",
	}
}

func expiringAntigravity() account.Credential {
	return account.Credential{
		Access:    "old-access-token",
		Refresh:   "old-refresh-grant",
		ExpiresAt: testNow().Add(4 * time.Minute),
		AccountID: "identity-1",
		Email:     "user@example.com",
		ProjectID: "project-old",
	}
}

func TestFreshCodexCredentialSkipsRefresh(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	cred := expiringCodex()
	cred.ExpiresAt = testNow().Add(90 * time.Second) // beyond the 60s skew
	h.seed(cred)
	got, err := h.ref.Credential(context.Background(), h.lease)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if got.Access != cred.Access || !got.ExpiresAt.Equal(cred.ExpiresAt) {
		t.Fatalf("fresh credential mutated: %+v", got)
	}
	if h.tokens.requests() != 0 || h.repoGen() != 1 || h.poolAccount().CredGen != 1 {
		t.Fatalf("refresh happened for a fresh credential: requests=%d gen=%d", h.tokens.requests(), h.repoGen())
	}
}

func TestFreshAntigravityCredentialSkipsRefresh(t *testing.T) {
	h := newRefreshHarness(t, "antigravity")
	cred := expiringAntigravity()
	cred.ExpiresAt = testNow().Add(6 * time.Minute) // beyond the 5m skew
	h.seed(cred)
	got, err := h.ref.Credential(context.Background(), h.lease)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if got.ProjectID != "project-old" {
		t.Fatalf("project id = %q", got.ProjectID)
	}
	if h.tokens.requests() != 0 || h.repoGen() != 1 {
		t.Fatalf("refresh happened for a fresh credential: requests=%d gen=%d", h.tokens.requests(), h.repoGen())
	}
}

// --- acceptance 2 and 3: expiring credentials refresh once ---

func TestExpiringCodexCredentialRefreshesOnce(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	h.tokens.respond(http.StatusOK, fmt.Sprintf(
		`{"access_token":"new-access-token","refresh_token":"new-refresh-grant","expires_in":3600,"id_token":%q}`,
		makeJWT(map[string]any{"chatgpt_account_id": "identity-1", "email": "user@example.com"})))
	got, err := h.ref.Credential(context.Background(), h.lease)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if got.Access != "new-access-token" || got.AccountID != "identity-1" {
		t.Fatalf("refreshed credential = %+v", got)
	}
	if got.Refresh == "old-refresh-grant" {
		t.Fatalf("refresh grant not rotated")
	}
	if h.tokens.requests() != 1 || h.repoGen() != 2 || h.poolAccount().CredGen != 2 {
		t.Fatalf("requests=%d gen=%d poolGen=%d", h.tokens.requests(), h.repoGen(), h.poolAccount().CredGen)
	}
	fresh, ok := h.blobAt(2)
	if !ok || fresh.ExpiresAt.IsZero() {
		t.Fatalf("generation 2 blob missing or malformed: %+v", fresh)
	}
}

func TestExpiringAntigravityCredentialRefreshesOnce(t *testing.T) {
	h := newRefreshHarness(t, "antigravity")
	h.seed(expiringAntigravity())
	h.tokens.respond(http.StatusOK, `{"access_token":"new-access-token","refresh_token":"new-refresh-grant","expires_in":3600,"cloudaicompanionProject":"project-new"}`)
	got, err := h.ref.Credential(context.Background(), h.lease)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if got.Access != "new-access-token" || got.ProjectID != "project-new" {
		t.Fatalf("refreshed credential = %+v", got)
	}
	if got.ExpiresAt.Before(testNow().Add(5 * time.Minute)) {
		t.Fatalf("expiry inside the antigravity skew: %v", got.ExpiresAt)
	}
	if h.tokens.requests() != 1 || h.repoGen() != 2 || h.poolAccount().CredGen != 2 {
		t.Fatalf("requests=%d gen=%d poolGen=%d", h.tokens.requests(), h.repoGen(), h.poolAccount().CredGen)
	}
}

// --- acceptance 4: single flight across turns and store clients ---

func TestConcurrentTurnsRefreshExactlyOnce(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	h.tokens.respond(http.StatusOK, `{"access_token":"new-access-token","refresh_token":"new-refresh-grant","expires_in":3600}`)
	hold := make(chan struct{})
	h.tokens.mu.Lock()
	h.tokens.hold = hold
	h.tokens.mu.Unlock()

	ctx := context.Background()
	type result struct {
		cred account.Credential
		err  error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			cred, err := h.ref.Credential(ctx, h.lease)
			results <- result{cred, err}
		}()
	}
	<-h.tokens.entered // winner reached the token endpoint
	time.Sleep(50 * time.Millisecond)
	close(hold)
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("waiter %d: %v", i, r.err)
		}
		if r.cred.Access != "new-access-token" {
			t.Fatalf("waiter %d got credential %+v", i, r.cred)
		}
	}
	if h.tokens.requests() != 1 {
		t.Fatalf("refresh HTTP requests = %d, want 1", h.tokens.requests())
	}
	if h.repoGen() != 2 || h.poolAccount().CredGen != 2 {
		t.Fatalf("gen=%d poolGen=%d, want 2", h.repoGen(), h.poolAccount().CredGen)
	}
}

func TestSeparateStoreClientsRefreshExactlyOnce(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	// A second client over the same root simulates a second process; the
	// refresh lock lives on disk, not in either client.
	second := store.NewFileCredentialStore(h.dir)
	other, err := NewRefresher(RefresherOptions{
		File:  second,
		Repos: map[account.ProviderID]*account.Repository{h.lease.Provider: h.repo},
		Pool:  h.pool,
		Flows: map[account.ProviderID]Flow{"codex": h.ref.flows["codex"]},
		Now:   h.clock.Now,
		Wait:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("second refresher: %v", err)
	}
	h.tokens.respond(http.StatusOK, `{"access_token":"new-access-token","refresh_token":"new-refresh-grant","expires_in":3600}`)
	hold := make(chan struct{})
	h.tokens.mu.Lock()
	h.tokens.hold = hold
	h.tokens.mu.Unlock()

	type result struct {
		cred account.Credential
		err  error
	}
	results := make(chan result, 2)
	go func() {
		cred, err := h.ref.Credential(context.Background(), h.lease)
		results <- result{cred, err}
	}()
	go func() {
		cred, err := other.Credential(context.Background(), h.lease)
		results <- result{cred, err}
	}()
	<-h.tokens.entered
	time.Sleep(50 * time.Millisecond)
	close(hold)
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("client %d: %v", i, r.err)
		}
		if r.cred.Access != "new-access-token" {
			t.Fatalf("client %d got %+v", i, r.cred)
		}
	}
	if h.tokens.requests() != 1 {
		t.Fatalf("refresh HTTP requests = %d, want 1", h.tokens.requests())
	}
	if h.repoGen() != 2 {
		t.Fatalf("gen = %d, want 2", h.repoGen())
	}
}

// --- acceptance 5: re-login and refresh serialize by account identity ---

func TestReLoginSerializesAfterRefresh(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	h.tokens.respond(http.StatusOK, `{"access_token":"refreshed-access-token","refresh_token":"refreshed-grant","expires_in":3600}`)
	hold := make(chan struct{})
	h.tokens.mu.Lock()
	h.tokens.hold = hold
	h.tokens.mu.Unlock()

	refreshed := make(chan account.Credential, 1)
	go func() {
		cred, err := h.ref.Credential(context.Background(), h.lease)
		if err != nil {
			t.Errorf("refresh: %v", err)
			refreshed <- account.Credential{}
			return
		}
		refreshed <- cred
	}()
	<-h.tokens.entered

	relogged := make(chan account.Account, 1)
	go func() {
		acct, err := h.sink.Persist(context.Background(), h.provider, account.Credential{
			Access:    "relogin-access-token",
			Refresh:   "relogin-grant",
			ExpiresAt: testNow().Add(time.Hour),
			AccountID: "identity-1",
			Email:     "user@example.com",
		})
		if err == nil {
			h.sink.Register(acct) // the auth service registers the pool row after persisting
		}
		relogged <- acct
	}()
	time.Sleep(50 * time.Millisecond) // let the re-login reach the held lock
	close(hold)

	got := <-refreshed
	if got.Access != "refreshed-access-token" {
		t.Fatalf("refresh credential = %+v", got)
	}
	if acct := <-relogged; acct.ID == "" {
		t.Fatal("persist failed")
	}
	if h.repoGen() != 3 {
		t.Fatalf("gen = %d, want 3 (refresh then re-login)", h.repoGen())
	}
	relogin, ok := h.blobAt(3)
	if !ok || relogin.Access != "relogin-access-token" || relogin.Refresh != "relogin-grant" {
		t.Fatalf("re-login generation overwritten: %+v", relogin)
	}
	if h.poolAccount().CredGen != 3 {
		t.Fatalf("pool gen = %d, want 3", h.poolAccount().CredGen)
	}
}

func TestStaleRefreshAdoptsReLoginGeneration(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	// A re-login advances the generation while the lease still points at 1.
	if err := h.file.PutIdempotent(context.Background(), h.provider, h.acc, 2, account.Credential{
		Access: "relogin-access-token", Refresh: "relogin-grant",
		ExpiresAt: testNow().Add(time.Hour), AccountID: "identity-1",
	}.Encode()); err != nil {
		t.Fatalf("seed re-login blob: %v", err)
	}
	if err := h.repo.Ensure(h.provider, h.acc, 2, "user@example.com"); err != nil {
		t.Fatalf("seed re-login row: %v", err)
	}
	got, err := h.ref.Credential(context.Background(), h.lease)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if got.Access != "relogin-access-token" {
		t.Fatalf("stale refresh returned %+v", got)
	}
	if h.tokens.requests() != 0 || h.repoGen() != 2 {
		t.Fatalf("requests=%d gen=%d; the re-login generation must be adopted untouched", h.tokens.requests(), h.repoGen())
	}
}

// --- acceptance 6: grant preservation and fail-closed malformed responses ---

func TestRefreshWithoutGrantPreservesOldGrant(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	h.tokens.respond(http.StatusOK, `{"access_token":"new-access-token","expires_in":3600}`)
	got, err := h.ref.Credential(context.Background(), h.lease)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if got.Refresh != "old-refresh-grant" {
		t.Fatalf("refresh grant = %q, want the preserved old grant", got.Refresh)
	}
	stored, ok := h.blobAt(2)
	if !ok || stored.Refresh != "old-refresh-grant" {
		t.Fatalf("stored grant = %+v", stored)
	}
}

func TestMalformedRefreshResponsesFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"invalid json", `{"access_token":`},
		{"missing access token", `{"refresh_token":"x","expires_in":3600}`},
		{"unreadable identity token", `{"access_token":"a","id_token":"not-a-jwt","expires_in":3600}`},
		{"identity for another account", `{"access_token":"a","expires_in":3600,"id_token":"` + makeJWT(map[string]any{"chatgpt_account_id": "someone-else"}) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRefreshHarness(t, "codex")
			h.seed(expiringCodex())
			h.tokens.respond(http.StatusOK, tc.payload)
			_, err := h.ref.Credential(context.Background(), h.lease)
			if err == nil {
				t.Fatal("expected failure")
			}
			if errors.Is(err, account.ErrNeedsReauth) || errors.Is(err, account.ErrRefreshTransient) {
				t.Fatalf("malformed response misclassified: %v", err)
			}
			if h.repoGen() != 1 {
				t.Fatalf("gen = %d, want 1", h.repoGen())
			}
			if _, ok := h.blobAt(1); !ok {
				t.Fatal("prior credential generation must remain")
			}
			if h.poolAccount().CredGen != 1 {
				t.Fatalf("pool gen = %d, want 1", h.poolAccount().CredGen)
			}
		})
	}
}

// --- acceptance 7: invalid grants vs transient failures ---

func TestInvalidGrantMarksNeedsReauthAndDropsCredentials(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	h.tokens.respond(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"token leaked-secret-value invalidated"}`)
	_, err := h.ref.Credential(context.Background(), h.lease)
	if !errors.Is(err, account.ErrNeedsReauth) {
		t.Fatalf("err = %v, want ErrNeedsReauth", err)
	}
	if strings.Contains(err.Error(), "leaked-secret-value") || strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error leaks the upstream body: %v", err)
	}
	if _, ok := h.blobAt(1); ok {
		t.Fatal("credentials must be removed after an invalid grant")
	}
	rows, err := h.repo.Load(h.provider)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 1 || rows[0].State != account.NeedsReauth {
		t.Fatalf("repo state = %+v, want needs_reauth", rows)
	}
	if pa := h.poolAccount(); pa.State != account.NeedsReauth {
		t.Fatalf("pool state = %v, want NeedsReauth", pa.State)
	}
}

func TestTransientRefreshFailuresStayRetryable(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		payload string
	}{
		{"server error", http.StatusInternalServerError, `{"error":"server_error"}`},
		{"rate limited", http.StatusTooManyRequests, `{"error":"slow_down"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRefreshHarness(t, "codex")
			h.seed(expiringCodex())
			h.tokens.respond(tc.status, tc.payload)
			_, err := h.ref.Credential(context.Background(), h.lease)
			if !errors.Is(err, account.ErrRefreshTransient) {
				t.Fatalf("err = %v, want ErrRefreshTransient", err)
			}
			if h.repoGen() != 1 {
				t.Fatalf("gen = %d, want 1", h.repoGen())
			}
			if _, ok := h.blobAt(1); !ok {
				t.Fatal("prior credential must survive a transient failure")
			}
			if pa := h.poolAccount(); pa.State != account.Active {
				t.Fatalf("pool state = %v, want Active", pa.State)
			}
		})
	}
}

// --- acceptance 8: crash seams ---

func TestOrphanedGenerationIgnoredOnRestart(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	cred := expiringCodex()
	cred.ExpiresAt = testNow().Add(time.Hour)
	h.seed(cred)
	// Crash between the blob write and the repository update: an unreferenced
	// generation 2 blob lingers.
	if err := h.file.PutIdempotent(context.Background(), h.provider, h.acc, 2, account.Credential{Access: "orphan-token"}.Encode()); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	got, err := h.ref.Credential(context.Background(), h.lease)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if got.Access != cred.Access {
		t.Fatalf("restart must use the repository-referenced generation, got %+v", got)
	}
	if h.tokens.requests() != 0 || h.repoGen() != 1 {
		t.Fatalf("requests=%d gen=%d", h.tokens.requests(), h.repoGen())
	}
}

func TestOrphanedGenerationClearedBeforeRefresh(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	if err := h.file.PutIdempotent(context.Background(), h.provider, h.acc, 2, account.Credential{Access: "orphan-token"}.Encode()); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	h.tokens.respond(http.StatusOK, `{"access_token":"new-access-token","refresh_token":"new-grant","expires_in":3600}`)
	got, err := h.ref.Credential(context.Background(), h.lease)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if got.Access != "new-access-token" {
		t.Fatalf("credential = %+v", got)
	}
	stored, ok := h.blobAt(2)
	if !ok || stored.Access != "new-access-token" {
		t.Fatalf("generation 2 = %+v, want the refreshed credential", stored)
	}
	if h.tokens.requests() != 1 || h.repoGen() != 2 {
		t.Fatalf("requests=%d gen=%d", h.tokens.requests(), h.repoGen())
	}
}

func TestPoolLaggingBehindRepositoryAligned(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	// Crash after the repository update but before the pool CAS.
	if err := h.file.PutIdempotent(context.Background(), h.provider, h.acc, 2, account.Credential{
		Access: "recovered-token", Refresh: "recovered-grant",
		ExpiresAt: testNow().Add(time.Hour), AccountID: "identity-1",
	}.Encode()); err != nil {
		t.Fatalf("seed recovered blob: %v", err)
	}
	if err := h.repo.Ensure(h.provider, h.acc, 2, "user@example.com"); err != nil {
		t.Fatalf("seed recovered row: %v", err)
	}
	got, err := h.ref.Credential(context.Background(), h.lease)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if got.Access != "recovered-token" {
		t.Fatalf("credential = %+v", got)
	}
	if h.poolAccount().CredGen != 2 {
		t.Fatalf("pool gen = %d, want 2", h.poolAccount().CredGen)
	}
	if h.tokens.requests() != 0 {
		t.Fatalf("requests = %d, want 0", h.tokens.requests())
	}
}

// --- context handling ---

func TestContextCancellationEndsRefreshWait(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	lock, err := h.file.AcquireRefreshLock(context.Background(), refreshFingerprint(h.provider, h.acc))
	if err != nil {
		t.Fatalf("hold lock: %v", err)
	}
	defer lock.Release()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.ref.Credential(ctx, h.lease)
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("credential call did not end after cancellation")
	}
}

func TestRefreshUsesContextOnHTTPRequest(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	h.seed(expiringCodex())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.ref.Credential(ctx, h.lease); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
