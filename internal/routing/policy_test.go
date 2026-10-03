package routing

import (
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func policyTarget(pid account.ProviderID, pol account.SelectionPolicy) provider.Target {
	t := testTarget(pid)
	t.Policy = pol
	return t
}

func TestFailedSelectedAccountIsTheOnlyAttempt(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true, RetryAfter: 7 * time.Second}
	pool := poolWith("codex", 2)
	runners := fakeRunners{"codex": failThenSuccess(1, runErr, canon.Usage{OutputTokens: 3})}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{policyTarget("codex", account.SelectionPolicy{PinnedAccount: "codex:acct-0"})}}}
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", res.Attempts)
	}
	if _, ok := res.Terminal.(Failed); !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if len(pool.calls) != 1 {
		t.Fatalf("acquires = %d, want 1", len(pool.calls))
	}
	if pool.calls[0].Policy.PinnedAccount != "codex:acct-0" {
		t.Fatalf("acquire selection = %q, want codex:acct-0", pool.calls[0].Policy.PinnedAccount)
	}
	if len(pool.records) != 1 {
		t.Fatalf("records = %d, want 1", len(pool.records))
	}
	if _, ok := pool.records[0].outcome.(account.RequestRejected); !ok {
		t.Fatalf("recorded outcome is %T, want RequestRejected", pool.records[0].outcome)
	}
	if pool.records[0].lease.Account != "codex:acct-0" {
		t.Fatalf("recorded account = %s, want codex:acct-0", pool.records[0].lease.Account)
	}
}

func TestTurnDoesNotSwitchAccounts(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true, RetryAfter: 7 * time.Second}
	pool := poolWith("codex", 2)
	runners := fakeRunners{"codex": failThenSuccess(1, runErr, canon.Usage{OutputTokens: 3})}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{policyTarget("codex", account.SelectionPolicy{})}}}
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", res.Attempts)
	}
	if _, ok := res.Terminal.(Failed); !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if len(pool.calls) != 1 || pool.calls[0].Policy.PinnedAccount != "" {
		t.Fatalf("acquires = %+v, want one acquire with no selection", pool.calls)
	}
}

func TestAcquireCarriesSelectedAccount(t *testing.T) {
	usage := canon.Usage{OutputTokens: 1}
	pool := poolWith("codex", 1)
	runners := fakeRunners{"codex": successRunner(usage)}
	want := account.SelectionPolicy{PinnedAccount: "acct-1"}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{policyTarget("codex", want)}}}
	runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if len(pool.calls) != 1 {
		t.Fatalf("acquires = %d, want 1", len(pool.calls))
	}
	if pool.calls[0].Policy != want {
		t.Fatalf("acquire policy = %+v, want %+v", pool.calls[0].Policy, want)
	}
}
