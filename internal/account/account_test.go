package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"prism/internal/execution"
	"prism/internal/quota"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func limitPtr(v int64) *int64 { return &v }

func acct(id AccountID, prio int, state State, limit int64, used int64) Account {
	return Account{
		ID:       id,
		Provider: "codex",
		Priority: prio,
		State:    state,
		CredGen:  1,
		Version:  1,
		Quota:    quota.Snapshot{Used: used, Limit: limitPtr(limit)},
	}
}

func req(session string) AcquireRequest {
	return AcquireRequest{
		Provider:   "codex",
		Model:      "gpt-5.2",
		QuotaGroup: "default",
		Session:    execution.SessionKey(session),
		Thread:     execution.ThreadKey("t1"),
	}
}

func TestVersionSplitCredGenPinnedByLease(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	a := acct("a1", 1, Active, 200, 0)
	a.CredGen = 1
	p.Register(a)

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if l1.CredGen != 1 || l1.Version != 1 {
		t.Fatalf("lease pins generation/version: got %d/%d, want 1/1", l1.CredGen, l1.Version)
	}
	if err := p.Record(ctx, l1, AuthRejected{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	snap := p.Snapshot().Accounts[0]
	if snap.Version != 2 {
		t.Fatalf("state version after mutation: got %d, want 2", snap.Version)
	}
	if snap.CredGen != 1 {
		t.Fatalf("credential generation moved with state: got %d, want 1", snap.CredGen)
	}

	refreshed := snap
	refreshed.CredGen = 2
	refreshed.State = Active
	p.Register(refreshed)
	l2, err := p.Acquire(ctx, req("s2"))
	if err != nil {
		t.Fatalf("acquire after refresh: %v", err)
	}
	if l2.CredGen != 2 || l2.Version != 2 {
		t.Fatalf("lease after refresh: got gen/ver %d/%d, want 2/2", l2.CredGen, l2.Version)
	}
	if err := p.Record(ctx, l1, RateLimited{RetryAfter: time.Minute}); err != nil {
		t.Fatalf("stale record: %v", err)
	}
	snap = p.Snapshot().Accounts[0]
	if snap.CredGen != 2 || snap.Version != 2 {
		t.Fatalf("stale outcome moved generation or version: got %d/%d, want 2/2", snap.CredGen, snap.Version)
	}
}

func TestRecordStaleOutcomeNeverMutatesState(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l1, AuthRejected{}); err != nil {
		t.Fatalf("record auth rejected: %v", err)
	}
	restored := p.Snapshot().Accounts[0]
	if restored.State != NeedsReauth || restored.Version != 2 {
		t.Fatalf("auth rejected: got state %d version %d, want NeedsReauth/2", restored.State, restored.Version)
	}
	restored.State = Active
	p.Register(restored)
	l2, err := p.Acquire(ctx, req("s2"))
	if err != nil {
		t.Fatalf("acquire restored: %v", err)
	}
	if err := p.Record(ctx, l1, RateLimited{RetryAfter: time.Minute}); err != nil {
		t.Fatalf("stale record: %v", err)
	}
	snap := p.Snapshot().Accounts[0]
	if snap.State != Active || snap.Version != 2 {
		t.Fatalf("stale outcome mutated state: got %d/%d, want Active/2", snap.State, snap.Version)
	}
	if snap.InFlight != 0 {
		t.Fatalf("stale outcome did not release in-flight lease: got %d", snap.InFlight)
	}
	if err := p.Record(ctx, l2, RateLimited{RetryAfter: time.Minute}); err != nil {
		t.Fatalf("current record: %v", err)
	}
	snap = p.Snapshot().Accounts[0]
	if snap.State != CoolingDown || snap.Version != 3 {
		t.Fatalf("current outcome: got state %d version %d, want CoolingDown/3", snap.State, snap.Version)
	}
}

func TestAffinityStickyAcrossSessionsAndExpiresAtTTL(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a", 2, Active, 200, 0))
	p.Register(acct("b", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if l1.Account != "a" {
		t.Fatalf("first acquire: got %s, want a", l1.Account)
	}
	l2, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if l2.Account != "a" {
		t.Fatalf("session stuck: got %s, want a", l2.Account)
	}
	l3, err := p.Acquire(ctx, req("s2"))
	if err != nil {
		t.Fatalf("acquire other session: %v", err)
	}
	if l3.Account != "a" {
		t.Fatalf("other session: got %s, want a", l3.Account)
	}

	demoted := p.Snapshot().Accounts[0]
	demoted.Priority = 0
	p.Register(demoted)
	l4, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire after demotion: %v", err)
	}
	if l4.Account != "a" {
		t.Fatalf("session kept account while usable: got %s, want a", l4.Account)
	}

	clock.Advance(affinityIdleTTL + time.Minute)
	l5, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire after ttl: %v", err)
	}
	if l5.Account != "b" {
		t.Fatalf("affinity not expired: got %s, want b", l5.Account)
	}
}

func TestAffinityDroppedOnAuthRejected(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a", 2, Active, 200, 0))
	p.Register(acct("b", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if l1.Account != "a" {
		t.Fatalf("first acquire: got %s, want a", l1.Account)
	}
	if err := p.Record(ctx, l1, AuthRejected{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	restored := p.Snapshot().Accounts[0]
	restored.Priority = 0
	restored.State = Active
	p.Register(restored)
	l2, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if l2.Account != "b" {
		t.Fatalf("affinity entry survived auth rejection: got %s, want b", l2.Account)
	}
}

func TestProbeLeaseGatedOncePerFiveMinutes(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l1, RateLimited{RetryAfter: 30 * time.Minute}); err != nil {
		t.Fatalf("record rate limited: %v", err)
	}

	probe1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("probe acquire: %v", err)
	}
	if !probe1.Probe || probe1.Account != "a1" {
		t.Fatalf("expected probe lease on cooling account, got %+v", probe1)
	}
	if err := p.Record(ctx, probe1, ProbeFailed{}); err != nil {
		t.Fatalf("record probe failed: %v", err)
	}
	if _, err := p.Acquire(ctx, req("s1")); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("second probe within five minutes: got %v, want ErrNoAccount", err)
	}

	clock.Advance(probeInterval)
	probe2, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("probe after interval: %v", err)
	}
	if !probe2.Probe {
		t.Fatalf("expected probe lease after interval")
	}
	if err := p.Record(ctx, probe2, ProbeSucceeded{}); err != nil {
		t.Fatalf("record probe succeeded: %v", err)
	}
	normal, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire after probe success: %v", err)
	}
	if normal.Probe {
		t.Fatalf("expected normal lease after recovery, got probe")
	}
}

func TestPauseBlocksAndDrainsInFlight(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	l2, err := p.Acquire(ctx, req("s2"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if got := p.Snapshot().Accounts[0].InFlight; got != 2 {
		t.Fatalf("in-flight: got %d, want 2", got)
	}
	if err := p.Pause(ctx, "a1", 1); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := p.Acquire(ctx, req("s3")); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("acquire while paused: got %v, want ErrNoAccount", err)
	}
	if err := p.Record(ctx, l1, TurnSucceeded{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := p.Record(ctx, l2, TurnSucceeded{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	snap := p.Snapshot().Accounts[0]
	if snap.State != Paused || snap.InFlight != 0 || snap.Version != 2 {
		t.Fatalf("drain: got state %d in-flight %d version %d, want Paused/0/2", snap.State, snap.InFlight, snap.Version)
	}
	if err := p.Resume(ctx, "a1", 2); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := p.Acquire(ctx, req("s3")); err != nil {
		t.Fatalf("acquire after resume: %v", err)
	}
}

func TestRecordTransitions(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name    string
		outcome Outcome
		after   func() (State, time.Time, time.Time, StateVersion)
	}{
		{
			name:    "turn succeeded stays active",
			outcome: TurnSucceeded{},
			after:   func() (State, time.Time, time.Time, StateVersion) { return Active, time.Time{}, time.Time{}, 1 },
		},
		{
			name:    "auth rejected",
			outcome: AuthRejected{},
			after:   func() (State, time.Time, time.Time, StateVersion) { return NeedsReauth, time.Time{}, time.Time{}, 2 },
		},
		{
			name:    "rate limited",
			outcome: RateLimited{RetryAfter: 30 * time.Second},
			after: func() (State, time.Time, time.Time, StateVersion) {
				return CoolingDown, base.Add(30 * time.Second), time.Time{}, 2
			},
		},
		{
			name:    "quota exhausted",
			outcome: QuotaExhausted{RetryAfter: 45 * time.Second},
			after: func() (State, time.Time, time.Time, StateVersion) {
				return CoolingDown, base.Add(45 * time.Second), time.Time{}, 2
			},
		},
		{
			name:    "not found skips without state change",
			outcome: NotFound{},
			after:   func() (State, time.Time, time.Time, StateVersion) { return Active, time.Time{}, time.Time{}, 1 },
		},
		{
			name:    "request timeout skips without state change",
			outcome: RequestTimeout{RetryAfter: 5 * time.Second},
			after:   func() (State, time.Time, time.Time, StateVersion) { return Active, time.Time{}, time.Time{}, 1 },
		},
		{
			name:    "server error soft avoids",
			outcome: ServerError{},
			after: func() (State, time.Time, time.Time, StateVersion) {
				return SoftAvoid, time.Time{}, base.Add(softAvoidWindow), 2
			},
		},
		{
			name:    "transport failure soft avoids",
			outcome: TransportFailure{},
			after: func() (State, time.Time, time.Time, StateVersion) {
				return SoftAvoid, time.Time{}, base.Add(softAvoidWindow), 2
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{t: base}
			p := New([]byte("secret"), clock.Now)
			ctx := context.Background()
			p.Register(acct("a1", 1, Active, 200, 0))
			l, err := p.Acquire(ctx, req("s1"))
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			if err := p.Record(ctx, l, tc.outcome); err != nil {
				t.Fatalf("record: %v", err)
			}
			wantState, wantCooldown, wantSoftAvoid, wantVersion := tc.after()
			snap := p.Snapshot().Accounts[0]
			if snap.State != wantState {
				t.Errorf("state: got %d, want %d", snap.State, wantState)
			}
			if !snap.CooldownUntil.Equal(wantCooldown) {
				t.Errorf("cooldown until: got %s, want %s", snap.CooldownUntil, wantCooldown)
			}
			if !snap.SoftAvoidUntil.Equal(wantSoftAvoid) {
				t.Errorf("soft avoid until: got %s, want %s", snap.SoftAvoidUntil, wantSoftAvoid)
			}
			if snap.Version != wantVersion {
				t.Errorf("version: got %d, want %d", snap.Version, wantVersion)
			}
		})
	}
}

func TestProbeFailedLeavesCooldownUnchanged(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	clock := &fakeClock{t: base}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l1, RateLimited{RetryAfter: 30 * time.Minute}); err != nil {
		t.Fatalf("record: %v", err)
	}
	probe, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("probe acquire: %v", err)
	}
	if err := p.Record(ctx, probe, ProbeFailed{}); err != nil {
		t.Fatalf("record probe failed: %v", err)
	}
	snap := p.Snapshot().Accounts[0]
	if snap.State != CoolingDown || !snap.CooldownUntil.Equal(base.Add(30*time.Minute)) || snap.Version != 2 {
		t.Fatalf("probe failed mutated cooldown: got %+v", snap)
	}
}

func TestCooldownExtendsOnlyNeverShrinks(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	clock := &fakeClock{t: base}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l1, RateLimited{RetryAfter: 10 * time.Minute}); err != nil {
		t.Fatalf("record: %v", err)
	}
	probe, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("probe acquire: %v", err)
	}
	if err := p.Record(ctx, probe, RateLimited{RetryAfter: 30 * time.Second}); err != nil {
		t.Fatalf("record during cooldown: %v", err)
	}
	if got := p.Snapshot().Accounts[0].CooldownUntil; !got.Equal(base.Add(10 * time.Minute)) {
		t.Fatalf("shorter retry-after shrank cooldown: got %s, want %s", got, base.Add(10*time.Minute))
	}
	clock.Advance(probeInterval + time.Second)
	probe2, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("probe acquire: %v", err)
	}
	if err := p.Record(ctx, probe2, RateLimited{RetryAfter: 60 * time.Second}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := p.Snapshot().Accounts[0].CooldownUntil; !got.Equal(base.Add(10 * time.Minute)) {
		t.Fatalf("cooldown shrank on later rate limit: got %s, want %s", got, base.Add(10*time.Minute))
	}
}

func TestSoftAvoidWindowDecays(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	clock := &fakeClock{t: base}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l1, ServerError{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if l, err := p.Acquire(ctx, req("s1")); err != nil || !l.Probe {
		t.Fatalf("expected probe lease during soft avoid, got %+v err %v", l, err)
	}
	clock.Advance(softAvoidWindow + time.Second)
	if _, err := p.Acquire(ctx, req("s1")); err != nil {
		t.Fatalf("acquire after window decay: %v", err)
	}
}

func TestCooldownWindowDecays(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	clock := &fakeClock{t: base}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l1, RateLimited{RetryAfter: 30 * time.Second}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if l, err := p.Acquire(ctx, req("s1")); err == nil && !l.Probe {
		t.Fatalf("acquire during cooldown returned a normal lease")
	}
	clock.Advance(31 * time.Second)
	if _, err := p.Acquire(ctx, req("s1")); err != nil {
		t.Fatalf("acquire after cooldown: %v", err)
	}
}

func TestProbeSucceededClearsSoftAvoid(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	clock := &fakeClock{t: base}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l1, ServerError{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	probe, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("probe acquire: %v", err)
	}
	if !probe.Probe {
		t.Fatalf("expected probe lease on soft-avoided account")
	}
	if err := p.Record(ctx, probe, ProbeSucceeded{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	snap := p.Snapshot().Accounts[0]
	if snap.State != Active || !snap.SoftAvoidUntil.IsZero() || snap.Version != 3 {
		t.Fatalf("probe did not clear soft avoid: got %+v", snap)
	}
}

func TestSelectionPrefersPriorityThenHeadroom(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a", 1, Active, 200, 100))
	p.Register(acct("b", 2, Active, 200, 150))
	p.Register(acct("c", 2, Active, 200, 50))

	l, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if l.Account != "c" {
		t.Fatalf("priority tier then headroom: got %s, want c", l.Account)
	}
	if err := p.Record(ctx, l, TurnSucceeded{}); err != nil {
		t.Fatalf("record: %v", err)
	}

	p.Register(acct("d", 3, Active, 0, 0))
	l2, err := p.Acquire(ctx, req("s2"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if l2.Account != "d" {
		t.Fatalf("unlimited quota ranks top: got %s, want d", l2.Account)
	}
	if err := p.Record(ctx, l2, TurnSucceeded{}); err != nil {
		t.Fatalf("record: %v", err)
	}

	p.Register(acct("e", 4, Active, 100, 100))
	l3, err := p.Acquire(ctx, req("s3"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if l3.Account != "e" {
		t.Fatalf("highest priority tier wins even with zero headroom: got %s, want e", l3.Account)
	}
}

func TestManagementMutationsCAS(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))

	if err := p.Pause(ctx, "a1", 99); !errors.Is(err, ErrStale) {
		t.Fatalf("pause stale: got %v, want ErrStale", err)
	}
	if err := p.UpdatePriority(ctx, "a1", 5, 1); err != nil {
		t.Fatalf("update priority: %v", err)
	}
	snap := p.Snapshot().Accounts[0]
	if snap.Priority != 5 || snap.Version != 2 {
		t.Fatalf("update priority: got prio %d version %d, want 5/2", snap.Priority, snap.Version)
	}
	if err := p.Pause(ctx, "a1", 2); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := p.Resume(ctx, "a1", 1); !errors.Is(err, ErrStale) {
		t.Fatalf("resume stale: got %v, want ErrStale", err)
	}
	if err := p.Resume(ctx, "a1", 3); err != nil {
		t.Fatalf("resume: %v", err)
	}
	snap = p.Snapshot().Accounts[0]
	if snap.State != Active || snap.Version != 4 {
		t.Fatalf("resume: got state %d version %d, want Active/4", snap.State, snap.Version)
	}
	if err := p.Pause(ctx, "missing", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pause missing: got %v, want ErrNotFound", err)
	}
}

func TestSnapshotDeepCopy(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	p.Register(acct("a1", 1, Active, 200, 0))

	snap := p.Snapshot()
	snap.Accounts[0].Quota.Used = 999
	*snap.Accounts[0].Quota.Limit = 1
	if got := p.Snapshot().Accounts[0].Quota; got.Used != 0 || *got.Limit != 200 {
		t.Fatalf("snapshot is not a deep copy: got %+v", got)
	}
}

func TestNoAccounts(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	if _, err := p.Acquire(context.Background(), req("s1")); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("empty pool: got %v, want ErrNoAccount", err)
	}
}

func TestNeedsReauthNotEligibleAndNoProbe(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))

	l1, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l1, AuthRejected{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := p.Acquire(ctx, req("s1")); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("needs-reauth acquire: got %v, want ErrNoAccount", err)
	}
}

func TestAffinityKeyDistinguishesThreads(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := New([]byte("secret"), clock.Now)
	ctx := context.Background()
	p.Register(acct("a", 1, Active, 200, 0))

	r1 := req("s1")
	r2 := req("s1")
	r2.Thread = "t2"
	if p.affinityKey(r1) == p.affinityKey(r2) {
		t.Fatalf("thread not part of affinity key")
	}
	l1, err := p.Acquire(ctx, r1)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	l2, err := p.Acquire(ctx, r2)
	if err != nil {
		t.Fatalf("acquire other thread: %v", err)
	}
	if l1.Account != l2.Account {
		t.Fatalf("threads should share provider selection but got %s vs %s", l1.Account, l2.Account)
	}
}
