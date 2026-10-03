package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/quota"
)

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

func pin(session string, id AccountID) AcquireRequest {
	r := req(session)
	r.Policy.PinnedAccount = id
	return r
}

func TestVersionSplitCredGenPinnedByLease(t *testing.T) {
	p := New()
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
	if err := p.Record(ctx, l1, AuthRejected{}); err != nil {
		t.Fatalf("stale record: %v", err)
	}
	snap = p.Snapshot().Accounts[0]
	if snap.CredGen != 2 || snap.Version != 2 {
		t.Fatalf("stale outcome moved generation or version: got %d/%d, want 2/2", snap.CredGen, snap.Version)
	}
}

func TestRecordStaleOutcomeNeverMutatesState(t *testing.T) {
	p := New()
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
	if err := p.Record(ctx, l1, AuthRejected{}); err != nil {
		t.Fatalf("stale record: %v", err)
	}
	snap := p.Snapshot().Accounts[0]
	if snap.State != Active || snap.Version != 2 {
		t.Fatalf("stale outcome mutated state: got %d/%d, want Active/2", snap.State, snap.Version)
	}
	if snap.InFlight != 0 {
		t.Fatalf("stale outcome did not release in-flight lease: got %d", snap.InFlight)
	}
	if err := p.Record(ctx, l2, TurnSucceeded{}); err != nil {
		t.Fatalf("current record: %v", err)
	}
	snap = p.Snapshot().Accounts[0]
	if snap.State != Active || snap.Version != 2 || snap.InFlight != 0 {
		t.Fatalf("success on active account: got state %d version %d in-flight %d, want Active/2/0", snap.State, snap.Version, snap.InFlight)
	}
}

func TestSingleAccountIsImplicit(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(acct("only", 1, Active, 200, 0))

	l, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if l.Account != "only" {
		t.Fatalf("implicit account: got %s, want only", l.Account)
	}
}

func TestMultipleAccountsRequireExplicitSelection(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(acct("a", 9, Active, 200, 0))
	p.Register(acct("b", 1, Active, 200, 0))

	if _, err := p.Acquire(ctx, req("s1")); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("unselected multi-account acquire: got %v, want ErrNoAccount", err)
	}
	l, err := p.Acquire(ctx, pin("s1", "b"))
	if err != nil {
		t.Fatalf("explicit acquire: %v", err)
	}
	if l.Account != "b" {
		t.Fatalf("explicit selection: got %s, want b", l.Account)
	}
}

func TestSelectedAccountFailureDoesNotSwitch(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(acct("a", 1, Active, 200, 0))
	p.Register(acct("b", 9, Active, 200, 0))

	l1, err := p.Acquire(ctx, pin("s1", "a"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l1, RequestRejected{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	l2, err := p.Acquire(ctx, pin("s1", "a"))
	if err != nil {
		t.Fatalf("re-acquire after failure: %v", err)
	}
	if l2.Account != "a" {
		t.Fatalf("failure switched accounts: got %s, want a", l2.Account)
	}
	if err := p.Record(ctx, l2, AuthRejected{}); err != nil {
		t.Fatalf("record auth rejected: %v", err)
	}
	if _, err := p.Acquire(ctx, pin("s2", "a")); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("rejected selection fell through: got %v, want ErrNoAccount", err)
	}
	other, err := p.Acquire(ctx, pin("s2", "b"))
	if err != nil {
		t.Fatalf("other explicit selection: %v", err)
	}
	if other.Account != "b" {
		t.Fatalf("other selection: got %s, want b", other.Account)
	}
}

func TestPauseBlocksAndDrainsInFlight(t *testing.T) {
	p := New()
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

func TestRecordAuthRejectedNeedsReauth(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(acct("a1", 1, Active, 200, 0))
	l, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l, AuthRejected{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	snap := p.Snapshot().Accounts[0]
	if snap.State != NeedsReauth || snap.Version != 2 || snap.InFlight != 0 {
		t.Fatalf("auth rejected: got state %d version %d in-flight %d, want NeedsReauth/2/0", snap.State, snap.Version, snap.InFlight)
	}
}

func TestNonAuthOutcomesDoNotGateSelection(t *testing.T) {
	cases := []Outcome{TurnSucceeded{}, RequestRejected{}}
	for _, outcome := range cases {
		p := New()
		ctx := context.Background()
		p.Register(acct("a1", 1, Active, 200, 0))
		l, err := p.Acquire(ctx, req("s1"))
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		if err := p.Record(ctx, l, outcome); err != nil {
			t.Fatalf("record %T: %v", outcome, err)
		}
		snap := p.Snapshot().Accounts[0]
		if snap.State != Active || snap.Version != 1 || snap.InFlight != 0 {
			t.Fatalf("%T gated or mutated selection: got state %d version %d in-flight %d", outcome, snap.State, snap.Version, snap.InFlight)
		}
		if _, err := p.Acquire(ctx, req("s2")); err != nil {
			t.Fatalf("%T blocked the same account: %v", outcome, err)
		}
	}
}

func TestCooldownDoesNotBlockSelectedAccount(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	p := New()
	ctx := context.Background()
	cooling := acct("a1", 1, CoolingDown, 200, 0)
	cooling.CooldownUntil = base.Add(30 * time.Minute)
	p.Register(cooling)
	p.Register(acct("a2", 9, Active, 200, 0))

	l, err := p.Acquire(ctx, pin("s1", "a1"))
	if err != nil {
		t.Fatalf("cooling selected account: %v", err)
	}
	if l.Account != "a1" {
		t.Fatalf("cooldown switched accounts: got %s, want a1", l.Account)
	}
}

func TestTurnSucceededClearsCoolingDown(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	p := New()
	ctx := context.Background()
	cooling := acct("a1", 1, CoolingDown, 200, 0)
	cooling.CooldownUntil = base.Add(30 * time.Minute)
	p.Register(cooling)

	l, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l, TurnSucceeded{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	snap := p.Snapshot().Accounts[0]
	if snap.State != Active || !snap.CooldownUntil.IsZero() || snap.Version != 2 {
		t.Fatalf("success did not clear cooling: got %+v", snap)
	}
}

func TestManagementMutationsCAS(t *testing.T) {
	p := New()
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
	p := New()
	p.Register(acct("a1", 1, Active, 200, 0))

	snap := p.Snapshot()
	snap.Accounts[0].Quota.Used = 999
	*snap.Accounts[0].Quota.Limit = 1
	if got := p.Snapshot().Accounts[0].Quota; got.Used != 0 || *got.Limit != 200 {
		t.Fatalf("snapshot is not a deep copy: got %+v", got)
	}
}

func TestNoAccounts(t *testing.T) {
	p := New()
	if _, err := p.Acquire(context.Background(), req("s1")); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("empty pool: got %v, want ErrNoAccount", err)
	}
}

func TestNeedsReauthNotEligible(t *testing.T) {
	p := New()
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

func TestAdvanceGenerationMovesForwardOnly(t *testing.T) {
	p := New()
	p.Register(acct("a", 1, Active, 200, 0))
	if err := p.AdvanceGeneration("a", 2); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if got := p.Snapshot().Accounts[0].CredGen; got != 2 {
		t.Fatalf("gen = %d, want 2", got)
	}
	p.Register(func() Account { a := acct("a", 1, Active, 200, 0); a.CredGen = 5; return a }())
	if err := p.AdvanceGeneration("a", 3); err != nil {
		t.Fatalf("advance behind: %v", err)
	}
	if got := p.Snapshot().Accounts[0].CredGen; got != 5 {
		t.Fatalf("gen = %d, want 5", got)
	}
	if err := p.AdvanceGeneration("missing", 9); err != nil {
		t.Fatalf("advance unknown account: %v", err)
	}
}

func TestMarkNeedsReauthBlocksWithoutSwitching(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(acct("a", 2, Active, 200, 0))
	p.Register(acct("b", 1, Active, 200, 0))

	if err := p.MarkNeedsReauth("a"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	var marked Account
	for _, snap := range p.Snapshot().Accounts {
		if snap.ID == "a" {
			marked = snap
		}
	}
	if marked.State != NeedsReauth || marked.CredGen != 1 {
		t.Fatalf("marked account: got state %d gen %d, want NeedsReauth/1", marked.State, marked.CredGen)
	}
	if _, err := p.Acquire(ctx, pin("s1", "a")); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("needs-reauth selection fell through: got %v, want ErrNoAccount", err)
	}
	l, err := p.Acquire(ctx, pin("s1", "b"))
	if err != nil {
		t.Fatalf("other selection: %v", err)
	}
	if l.Account != "b" {
		t.Fatalf("other selection: got %s, want b", l.Account)
	}
}

func TestRecordReleasesLeaseAfterContextCancellation(t *testing.T) {
	p := New()
	p.Register(acct("a1", 1, Active, 200, 0))
	lease, err := p.Acquire(context.Background(), req("cancelled"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = p.Record(ctx, lease, RequestRejected{})
	if got := p.Snapshot().Accounts[0].InFlight; got != 0 {
		t.Fatalf("in-flight = %d, want release after cancellation", got)
	}
}

func TestDeleteAccountRemovesAccount(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(acct("b1", 1, Active, 200, 0))
	p.Register(acct("a1", 2, Active, 200, 0))

	if _, err := p.Acquire(ctx, req("s1")); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("unselected multi-account acquire: got %v, want ErrNoAccount", err)
	}
	if err := p.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatalf("delete a1: %v", err)
	}
	if err := p.DeleteAccount(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete ghost: got %v, want ErrNotFound", err)
	}
	snap := p.Snapshot()
	if len(snap.Accounts) != 1 || snap.Accounts[0].ID != "b1" {
		t.Fatalf("after delete: got %+v, want only b1", snap.Accounts)
	}
	l, err := p.Acquire(ctx, req("s1"))
	if err != nil {
		t.Fatalf("acquire remaining account: %v", err)
	}
	if l.Account != "b1" {
		t.Fatalf("remaining account: got %s, want b1", l.Account)
	}
}

func TestUpdateQuotaStoresDeepCopy(t *testing.T) {
	p := New()
	p.Register(acct("a1", 1, Active, 200, 0))

	limit := int64(500)
	windowEnd := time.Unix(1_700_000_000, 0).Add(time.Hour)
	snap := quota.Snapshot{Used: 120, Limit: &limit, WindowEnd: windowEnd, Source: quota.SourceEndpoint}
	if err := p.UpdateQuota("a1", snap); err != nil {
		t.Fatalf("update quota: %v", err)
	}
	snap.Used = 999
	limit = 1
	snap.WindowEnd = time.Time{}
	snap.Source = quota.SourceReport

	stored := p.Snapshot().Accounts[0].Quota
	if stored.Used != 120 || stored.Limit == nil || *stored.Limit != 500 {
		t.Fatalf("pool quota not deep-copied: got %+v", stored)
	}
	if !stored.WindowEnd.Equal(windowEnd) || stored.Source != quota.SourceEndpoint {
		t.Fatalf("quota fields not stored: got %+v", stored)
	}
	if err := p.UpdateQuota("ghost", snap); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update quota ghost: got %v, want ErrNotFound", err)
	}
}
