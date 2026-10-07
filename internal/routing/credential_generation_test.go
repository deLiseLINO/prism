package routing

import (
	"context"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/requestlog"
)

func TestRefreshedRejectionTargetsOnlyDispatchedGeneration(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		pool := account.New()
		pool.Register(account.Account{ID: "selected", Provider: "edge", CredGen: 1, Version: 1, State: account.Active})
		runner := runnerFunc(func(req provider.RunRequest, out provider.Sink) error {
			if err := pool.AdvanceGeneration("selected", 2); err != nil {
				t.Fatal(err)
			}
			req.CredentialObserver(2)
			if replaced {
				pool.Register(account.Account{ID: "selected", Provider: "edge", CredGen: 3, Version: 2, State: account.Active})
			}
			return provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassUnauthorized}
		})
		res := NewRouter(pool, fakeRunners{"edge": runner}, singlePlan("edge", TurnPolicy{}), "default", requestlog.New(8, time.Now)).Turn(context.Background(), canon.Request{Model: "gpt-5.2"}, execution.Facts{}, fakeLifecycle{}, &recordingSink{})
		if _, ok := res.Terminal.(Failed); !ok {
			t.Fatalf("terminal=%T", res.Terminal)
		}
		current := pool.Snapshot().Accounts[0]
		want := account.NeedsReauth
		if replaced {
			want = account.Active
		}
		if current.State != want || current.InFlight != 0 {
			t.Fatalf("replaced=%t account=%+v", replaced, current)
		}
	}
}

func TestRefreshedOutcomeDoesNotAdoptNewUserPolicyEpoch(t *testing.T) {
	pool := account.New()
	pool.Register(account.Account{ID: "selected", Provider: "edge", CredGen: 1, Version: 1, State: account.Active})
	runner := runnerFunc(func(req provider.RunRequest, out provider.Sink) error {
		if err := pool.Pause(context.Background(), "selected", 1); err != nil {
			t.Fatal(err)
		}
		if err := pool.Resume(context.Background(), "selected", 2); err != nil {
			t.Fatal(err)
		}
		if err := pool.AdvanceGeneration("selected", 2); err != nil {
			t.Fatal(err)
		}
		req.CredentialObserver(2)
		return provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassUnauthorized}
	})
	res := NewRouter(pool, fakeRunners{"edge": runner}, singlePlan("edge", TurnPolicy{}), "default", requestlog.New(8, time.Now)).Turn(context.Background(), canon.Request{Model: "gpt-5.2"}, execution.Facts{}, fakeLifecycle{}, &recordingSink{})
	if _, ok := res.Terminal.(Failed); !ok {
		t.Fatalf("terminal=%T", res.Terminal)
	}
	current := pool.Snapshot().Accounts[0]
	if current.State != account.Active || current.Version != 3 || current.InFlight != 0 {
		t.Fatalf("account=%+v", current)
	}
}
