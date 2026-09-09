package server

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/config"
	"prism/internal/integrations"
	"prism/internal/management"
	"prism/internal/provider"
	"prism/internal/quota"
	"prism/internal/routing"
	"prism/internal/usage"
)

type fakePlanner struct {
	plans map[canon.ModelID]routing.Plan
}

func (f *fakePlanner) Plan(m canon.ModelID) (routing.Plan, bool) {
	p, ok := f.plans[m]
	return p, ok
}

type fakeScript struct {
	events []canon.Event
	err    error
	block  bool
}

type fakeRunner struct {
	mu      sync.Mutex
	calls   int
	scripts []fakeScript
}

func (f *fakeRunner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	f.mu.Lock()
	i := f.calls
	f.calls++
	var sc fakeScript
	if i < len(f.scripts) {
		sc = f.scripts[i]
	}
	f.mu.Unlock()
	for _, ev := range sc.events {
		if err := sink.Emit(ev); err != nil {
			return err
		}
	}
	if sc.block {
		<-ctx.Done()
		return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: ctx.Err()}
	}
	return sc.err
}

func (f *fakeRunner) callsMade() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type countingRunner struct {
	*fakeRunner
	tokens int64
}

func (r *countingRunner) CountTokens(ctx context.Context, req provider.CountTokensRequest) (provider.TokenCount, error) {
	return provider.TokenCount{InputTokens: r.tokens}, nil
}

type compactingRunner struct {
	*fakeRunner
	result provider.CompactResult
	err    error
	called bool
}

func (r *compactingRunner) Compact(ctx context.Context, req provider.CompactRequest) (provider.CompactResult, error) {
	r.called = true
	return r.result, r.err
}

type fakeClockWaiter struct {
	ch       chan time.Time
	deadline time.Time
}

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*fakeClockWaiter
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	deadline := c.now.Add(d)
	if !deadline.After(c.now) {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, &fakeClockWaiter{ch: ch, deadline: deadline})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var fired []*fakeClockWaiter
	var keep []*fakeClockWaiter
	for _, w := range c.waiters {
		if !w.deadline.After(c.now) {
			fired = append(fired, w)
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
	now := c.now
	c.mu.Unlock()
	for _, w := range fired {
		w.ch <- now
	}
}

type harness struct {
	handler   http.Handler
	pool      account.Pool
	cfg       *config.Manager
	mgmtToken string
}

func (h *harness) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

func activeAccount(id, providerID string) account.Account {
	limit := int64(1000)
	return account.Account{
		ID:       account.AccountID(id),
		Provider: account.ProviderID(providerID),
		Priority: 1,
		State:    account.Active,
		CredGen:  1,
		Version:  1,
		Quota:    quota.Snapshot{Used: 0, Limit: &limit},
	}
}

func newTestServer(t *testing.T, clock Clock, plans map[canon.ModelID]routing.Plan, register func(reg *provider.Registry)) *harness {
	t.Helper()
	return newTestServerWithUsage(t, clock, plans, register, nil)
}

func newTestServerWithUsage(t *testing.T, clock Clock, plans map[canon.ModelID]routing.Plan, register func(reg *provider.Registry), usageStore usage.Store) *harness {
	t.Helper()
	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	pool := account.New([]byte("test-secret"), func() time.Time { return base })
	pool.Register(activeAccount("a1", "p1"))
	pool.Register(activeAccount("a2", "p2"))
	pool.Register(activeAccount("a3", "p3"))
	pool.Register(activeAccount("a4", "p4"))
	reg := provider.NewRegistry()
	if register != nil {
		register(reg)
	}
	cfg, err := config.Open(filepath.Join(t.TempDir(), "prism.json"))
	if err != nil {
		t.Fatalf("config open: %v", err)
	}
	mgmt := management.New(pool, cfg, nil, nil, nil, nil, integrations.NewRegistry(), nil).Handler()
	srv := New(Options{
		Planner:         &fakePlanner{plans: plans},
		Registry:        reg,
		Pool:            pool,
		Config:          cfg,
		Management:      mgmt,
		ManagementToken: "test-mgmt-token",
		Clock:           clock,
		Usage:           usageStore,
	})
	return &harness{handler: srv.Handler(), pool: pool, cfg: cfg, mgmtToken: "test-mgmt-token"}
}
