package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"prism/internal/execution"
	"prism/internal/quota"
)

func pacct(id AccountID, providerID ProviderID, prio int, state State, limit, used int64) Account {
	return Account{
		ID:       id,
		Provider: providerID,
		Priority: prio,
		State:    state,
		CredGen:  1,
		Version:  1,
		Quota:    quota.Snapshot{Used: used, Limit: limitPtr(limit)},
	}
}

func preq(session string, policy SelectionPolicy) AcquireRequest {
	return AcquireRequest{
		Provider:   "codex",
		Model:      "gpt-5.2",
		QuotaGroup: "default",
		Session:    execution.SessionKey(session),
		Thread:     execution.ThreadKey("t1"),
		Policy:     policy,
	}
}

func TestAcquireFiltersCandidatesToRequestedProvider(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 1, Active, 100, 0))
	p.Register(pacct("g1", "antigravity", 9, Active, 100, 0))

	l, err := p.Acquire(ctx, preq("s1", SelectionPolicy{}))
	if err != nil {
		t.Fatalf("acquire codex: %v", err)
	}
	if l.Provider != "codex" || l.Account != "c1" {
		t.Fatalf("cross-provider lease: got %s/%s, want codex/c1", l.Provider, l.Account)
	}

	areq := preq("s1", SelectionPolicy{})
	areq.Provider = "antigravity"
	l2, err := p.Acquire(ctx, areq)
	if err != nil {
		t.Fatalf("acquire antigravity: %v", err)
	}
	if l2.Provider != "antigravity" || l2.Account != "g1" {
		t.Fatalf("cross-provider lease: got %s/%s, want antigravity/g1", l2.Provider, l2.Account)
	}
}

func TestAcquireFiltersProbesToRequestedProvider(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	c1 := pacct("c1", "codex", 1, CoolingDown, 100, 0)
	c1.CooldownUntil = clock.t.Add(10 * time.Minute)
	p.Register(c1)
	g1 := pacct("g1", "antigravity", 9, CoolingDown, 100, 0)
	g1.CooldownUntil = clock.t.Add(10 * time.Minute)
	p.Register(g1)

	l, err := p.Acquire(ctx, preq("s1", SelectionPolicy{}))
	if err != nil {
		t.Fatalf("probe acquire: %v", err)
	}
	if !l.Probe || l.Provider != "codex" || l.Account != "c1" {
		t.Fatalf("cross-provider probe: got %+v, want codex probe on c1", l)
	}
}

func TestPinnedAccountPreferredWithinProvider(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 9, Active, 100, 0))
	p.Register(pacct("c2", "codex", 1, Active, 100, 0))
	p.Register(pacct("g1", "antigravity", 9, Active, 100, 0))

	pol := SelectionPolicy{PinnedAccount: "c2"}
	l, err := p.Acquire(ctx, preq("s1", pol))
	if err != nil {
		t.Fatalf("acquire pinned: %v", err)
	}
	if l.Account != "c2" {
		t.Fatalf("pinned ignored: got %s, want c2", l.Account)
	}

	foreign := SelectionPolicy{PinnedAccount: "g1"}
	l2, err := p.Acquire(ctx, preq("s2", foreign))
	if err != nil {
		t.Fatalf("acquire foreign pin: %v", err)
	}
	if l2.Account != "c1" {
		t.Fatalf("foreign pin used: got %s, want c1", l2.Account)
	}

	missing := SelectionPolicy{PinnedAccount: "ghost"}
	l3, err := p.Acquire(ctx, preq("s3", missing))
	if err != nil {
		t.Fatalf("acquire missing pin: %v", err)
	}
	if l3.Account != "c1" {
		t.Fatalf("missing pin not ignored: got %s, want c1", l3.Account)
	}
}

func TestPinnedAccountExhaustedFallsThrough(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	pinned := pacct("c2", "codex", 1, Active, 100, 0)
	p.Register(pinned)
	p.Register(pacct("c1", "codex", 2, Active, 100, 0))

	pol := SelectionPolicy{PinnedAccount: "c2"}
	l, err := p.Acquire(ctx, preq("s1", pol))
	if err != nil {
		t.Fatalf("acquire pinned: %v", err)
	}
	if l.Account != "c2" {
		t.Fatalf("pinned ignored: got %s, want c2", l.Account)
	}
	if err := p.Record(ctx, l, RateLimited{RetryAfter: 30 * time.Minute}); err != nil {
		t.Fatalf("record: %v", err)
	}
	l2, err := p.Acquire(ctx, preq("s1", pol))
	if err != nil {
		t.Fatalf("acquire after pinned exhausted: %v", err)
	}
	if l2.Account != "c1" {
		t.Fatalf("exhausted pinned not bypassed: got %s, want c1", l2.Account)
	}
}

func TestPinnedExhaustionRebindsStickyAffinity(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c2", "codex", 2, Active, 100, 0))
	p.Register(pacct("c1", "codex", 1, Active, 100, 0))

	pol := SelectionPolicy{PinnedAccount: "c2"}
	l, err := p.Acquire(ctx, preq("s1", pol))
	if err != nil {
		t.Fatalf("pinned acquire: %v", err)
	}
	if l.Account != "c2" {
		t.Fatalf("pinned ignored: got %s, want c2", l.Account)
	}
	if err := p.Record(ctx, l, RateLimited{RetryAfter: 30 * time.Minute}); err != nil {
		t.Fatalf("record: %v", err)
	}

	l2, err := p.Acquire(ctx, preq("s1", pol))
	if err != nil {
		t.Fatalf("fallthrough acquire: %v", err)
	}
	if l2.Account != "c1" {
		t.Fatalf("exhausted pinned not bypassed: got %s, want c1", l2.Account)
	}

	clock.Advance(time.Hour)
	unpinned := SelectionPolicy{}
	l3, err := p.Acquire(ctx, preq("s1", unpinned))
	if err != nil {
		t.Fatalf("rebind acquire: %v", err)
	}
	if l3.Account != "c1" {
		t.Fatalf("sticky affinity not rebound after pinned exhaustion: got %s, want c1", l3.Account)
	}
}

func TestThresholdExcludesAtOrAboveRatio(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 9, Active, 100, 90))
	p.Register(pacct("c2", "codex", 1, Active, 100, 10))

	pol := SelectionPolicy{AutoSwitchThreshold: 0.9}
	l, err := p.Acquire(ctx, preq("s1", pol))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if l.Account != "c2" {
		t.Fatalf("threshold not enforced before dispatch: got %s, want c2", l.Account)
	}

	pol2 := SelectionPolicy{AutoSwitchThreshold: 0.91}
	l2, err := p.Acquire(ctx, preq("s2", pol2))
	if err != nil {
		t.Fatalf("acquire below threshold: %v", err)
	}
	if l2.Account != "c1" {
		t.Fatalf("account below threshold excluded: got %s, want c1", l2.Account)
	}
}

func TestThresholdExcludesSoleAccount(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 1, Active, 100, 95))

	pol := SelectionPolicy{AutoSwitchThreshold: 0.9}
	if _, err := p.Acquire(ctx, preq("s1", pol)); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("threshold-exhausted pool: got %v, want ErrNoAccount", err)
	}
}

func TestThresholdRebindsStickyAffinity(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 9, Active, 100, 0))
	p.Register(pacct("c2", "codex", 1, Active, 100, 0))

	pol := SelectionPolicy{AutoSwitchThreshold: 0.9}
	l1, err := p.Acquire(ctx, preq("s1", pol))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if l1.Account != "c1" {
		t.Fatalf("first acquire: got %s, want c1", l1.Account)
	}
	exhausted := p.Snapshot().Accounts[0]
	exhausted.Quota.Used = 90
	p.Register(exhausted)
	l2, err := p.Acquire(ctx, preq("s1", pol))
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if l2.Account != "c2" {
		t.Fatalf("sticky affinity held over-threshold account: got %s, want c2", l2.Account)
	}
}

func TestRoundRobinDistributesWithinHighestTier(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 1, Active, 100, 0))
	p.Register(pacct("c2", "codex", 1, Active, 100, 0))
	p.Register(pacct("c3", "codex", 1, Active, 100, 0))
	p.Register(pacct("c0", "codex", 0, Active, 100, 0))

	pol := SelectionPolicy{Strategy: StrategyRoundRobin, Affinity: AffinityOff}
	seen := map[AccountID]bool{}
	for i := 0; i < 3; i++ {
		l, err := p.Acquire(ctx, preq(string(rune('s'+i)), pol))
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if seen[l.Account] {
			t.Fatalf("round-robin repeated %s within one cycle", l.Account)
		}
		seen[l.Account] = true
	}
	for _, id := range []AccountID{"c1", "c2", "c3"} {
		if !seen[id] {
			t.Fatalf("round-robin missed %s: seen %v", id, seen)
		}
	}
	if seen["c0"] {
		t.Fatal("round-robin left highest priority tier")
	}
	l, err := p.Acquire(ctx, preq("s9", pol))
	if err != nil {
		t.Fatalf("wrap acquire: %v", err)
	}
	if !seen[l.Account] {
		t.Fatalf("wrap picked new account %s", l.Account)
	}
}

func TestRoundRobinTiersDoNotDistributeLowerTier(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 2, Active, 100, 0))
	p.Register(pacct("c2", "codex", 1, Active, 100, 0))
	p.Register(pacct("c3", "codex", 1, Active, 100, 0))

	pol := SelectionPolicy{Strategy: StrategyRoundRobin, Affinity: AffinityOff}
	for i := 0; i < 4; i++ {
		l, err := p.Acquire(ctx, preq(string(rune('a'+i)), pol))
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if l.Account != "c1" {
			t.Fatalf("acquire %d left highest tier: got %s, want c1", i, l.Account)
		}
	}
}

func TestFillFirstStaysOnLowestIDUntilUnusable(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 1, Active, 100, 0))
	p.Register(pacct("c2", "codex", 1, Active, 100, 0))
	p.Register(pacct("c9", "codex", 2, Active, 100, 0))

	pol := SelectionPolicy{Strategy: StrategyFillFirst, Affinity: AffinityOff}
	for i := 0; i < 3; i++ {
		l, err := p.Acquire(ctx, preq(string(rune('a'+i)), pol))
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if l.Account != "c9" {
			t.Fatalf("fill-first moved off highest priority: got %s, want c9", l.Account)
		}
	}
	demoted := p.Snapshot().Accounts[2]
	demoted.Priority = 1
	demoted.State = Paused
	p.Register(demoted)
	l, err := p.Acquire(ctx, preq("z1", pol))
	if err != nil {
		t.Fatalf("acquire after demotion: %v", err)
	}
	if l.Account != "c1" {
		t.Fatalf("fill-first did not take lowest usable ID: got %s, want c1", l.Account)
	}
}

func TestAffinityOffReselectsEachTurn(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 2, Active, 100, 0))
	p.Register(pacct("c2", "codex", 1, Active, 100, 0))

	off := SelectionPolicy{Affinity: AffinityOff}
	l1, err := p.Acquire(ctx, preq("s1", off))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if l1.Account != "c1" {
		t.Fatalf("first acquire: got %s, want c1", l1.Account)
	}
	l2, err := p.Acquire(ctx, preq("s1", off))
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if l2.Account != "c1" {
		t.Fatalf("affinity off reselected: got %s, want c1", l2.Account)
	}
	demoted := p.Snapshot().Accounts[0]
	demoted.State = CoolingDown
	demoted.CooldownUntil = clock.t.Add(10 * time.Minute)
	p.Register(demoted)
	l3, err := p.Acquire(ctx, preq("s1", off))
	if err != nil {
		t.Fatalf("re-acquire after demotion: %v", err)
	}
	if l3.Account != "c2" {
		t.Fatalf("affinity off kept stale choice: got %s, want c2", l3.Account)
	}
}

func TestDefaultPolicyBindsRepeatedSession(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 1, Active, 100, 0))
	p.Register(pacct("c2", "codex", 1, Active, 100, 0))

	l1, err := p.Acquire(ctx, preq("s1", SelectionPolicy{}))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	l2, err := p.Acquire(ctx, preq("s1", SelectionPolicy{}))
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if l1.Account != l2.Account {
		t.Fatalf("zero policy did not bind session: %s then %s", l1.Account, l2.Account)
	}
}
