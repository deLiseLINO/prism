package account

import (
	"context"
	"errors"
	"testing"

	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/quota"
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
	p := New()
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

func TestCustomProviderSingleDefaultRemainsImplicit(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(pacct("custom-1", "openai", 1, Active, 100, 0))

	areq := preq("s1", SelectionPolicy{})
	areq.Provider = "openai"
	l, err := p.Acquire(ctx, areq)
	if err != nil {
		t.Fatalf("acquire custom default: %v", err)
	}
	if l.Provider != "openai" || l.Account != "custom-1" {
		t.Fatalf("custom default: got %s/%s, want openai/custom-1", l.Provider, l.Account)
	}
}

func TestPinnedAccountIsExclusive(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 9, Active, 100, 0))
	p.Register(pacct("c2", "codex", 1, Active, 100, 0))
	p.Register(pacct("g1", "antigravity", 9, Active, 100, 0))

	l, err := p.Acquire(ctx, preq("s1", SelectionPolicy{PinnedAccount: "c2"}))
	if err != nil {
		t.Fatalf("acquire pinned: %v", err)
	}
	if l.Account != "c2" {
		t.Fatalf("selection ignored: got %s, want c2", l.Account)
	}

	if _, err := p.Acquire(ctx, preq("s2", SelectionPolicy{PinnedAccount: "g1"})); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("foreign selection fell through: got %v, want ErrNoAccount", err)
	}
	if _, err := p.Acquire(ctx, preq("s3", SelectionPolicy{PinnedAccount: "ghost"})); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("missing selection fell through: got %v, want ErrNoAccount", err)
	}
}

func TestSelectedAccountStaysExclusiveWhenPaused(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(pacct("c2", "codex", 1, Paused, 100, 0))
	p.Register(pacct("c1", "codex", 2, Active, 100, 0))

	if _, err := p.Acquire(ctx, preq("s1", SelectionPolicy{PinnedAccount: "c2"})); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("paused selection fell through: got %v, want ErrNoAccount", err)
	}
}

func TestMissingSelectionWithMultipleAccounts(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 9, Active, 100, 10))
	p.Register(pacct("c2", "codex", 1, Active, 100, 90))

	if _, err := p.Acquire(ctx, preq("s1", SelectionPolicy{})); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("missing selection: got %v, want ErrNoAccount", err)
	}
}

func TestExplicitSelectionAfterErrorStaysOnThatAccount(t *testing.T) {
	p := New()
	ctx := context.Background()
	p.Register(pacct("c1", "codex", 1, Active, 100, 0))
	p.Register(pacct("c2", "codex", 9, Active, 100, 0))

	pol := SelectionPolicy{PinnedAccount: "c1"}
	l1, err := p.Acquire(ctx, preq("s1", pol))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := p.Record(ctx, l1, RequestRejected{}); err != nil {
		t.Fatalf("record: %v", err)
	}
	l2, err := p.Acquire(ctx, preq("s1", pol))
	if err != nil {
		t.Fatalf("re-acquire after error: %v", err)
	}
	if l2.Account != "c1" {
		t.Fatalf("error switched accounts: got %s, want c1", l2.Account)
	}
}
