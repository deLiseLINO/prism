package routing

import (
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/provider"
)

func policyTarget(pid account.ProviderID, pol account.SelectionPolicy) provider.Target {
	t := testTarget(pid, 0)
	t.Policy = pol
	return t
}

func TestAutoSwitchOffStopsAfterSelectedAccount(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true, RetryAfter: 7 * time.Second}
	pool := poolWith("codex", 2)
	runners := fakeRunners{"codex": failThenSuccess(1, runErr, canon.Usage{OutputTokens: 3})}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{policyTarget("codex", account.SelectionPolicy{AutoSwitch: account.AutoSwitchOff})}}}
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 with auto-switch off", res.Attempts)
	}
	if _, ok := res.Terminal.(Failed); !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if len(pool.calls) != 1 {
		t.Fatalf("acquires = %d, want 1", len(pool.calls))
	}
	if len(pool.records) != 1 {
		t.Fatalf("records = %d, want 1", len(pool.records))
	}
}

func TestAutoSwitchOnFailsOverBeforeOutputCommit(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true, RetryAfter: 7 * time.Second}
	usage := canon.Usage{OutputTokens: 3}
	pool := poolWith("codex", 2)
	runners := fakeRunners{"codex": failThenSuccess(1, runErr, usage)}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{policyTarget("codex", account.SelectionPolicy{})}}}
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 with auto-switch on", res.Attempts)
	}
	if _, ok := res.Terminal.(Finished); !ok {
		t.Fatalf("terminal is %T, want Finished", res.Terminal)
	}
}

func TestAcquireCarriesSelectionPolicy(t *testing.T) {
	usage := canon.Usage{OutputTokens: 1}
	pool := poolWith("codex", 1)
	runners := fakeRunners{"codex": successRunner(usage)}
	want := account.SelectionPolicy{
		Strategy:            account.StrategyRoundRobin,
		AutoSwitch:          account.AutoSwitchOn,
		AutoSwitchThreshold: 0.9,
		Affinity:            account.AffinityOff,
		PinnedAccount:       "acct-1",
	}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{policyTarget("codex", want)}}}
	runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if len(pool.calls) != 1 {
		t.Fatalf("acquires = %d, want 1", len(pool.calls))
	}
	if pool.calls[0].Policy != want {
		t.Fatalf("acquire policy = %+v, want %+v", pool.calls[0].Policy, want)
	}
}
