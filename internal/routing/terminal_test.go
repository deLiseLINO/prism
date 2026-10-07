package routing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/requestlog"
)

func TestHeldFailureCanFailOverWithoutLeakingTerminal(t *testing.T) {
	usage := canon.Usage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9}
	first := runnerFunc(func(_ provider.RunRequest, out provider.Sink) error {
		if err := out.Emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: "first failed"}, Usage: usage}); err != nil {
			return err
		}
		return provider.RunError{Kind: provider.TerminalEmitted, Class: provider.ClassServer}
	})
	pool := &fakePool{results: []leaseResult{{lease: testLease("edge", 0)}, {lease: testLease("backup", 0)}}}
	plan := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{testTarget("edge"), testTarget("backup")}}}
	sink := &recordingSink{}
	journal := requestlog.New(8, time.Now)
	backup := runnerFunc(func(_ provider.RunRequest, out provider.Sink) error {
		return out.Emit(canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{OutputTokens: 2, TotalTokens: 2}})
	})
	res := NewRouter(pool, fakeRunners{"edge": first, "backup": backup}, plan, "default", journal).Turn(context.Background(), canon.Request{Model: "gpt-5.2"}, execution.Facts{}, fakeLifecycle{}, sink)
	if _, ok := res.Terminal.(Finished); !ok || res.Attempts != 2 || len(sink.events) != 1 {
		t.Fatalf("result=%+v events=%+v", res, sink.events)
	}
	if _, ok := sink.events[0].(canon.TurnFinished); !ok || res.Trace[0].Usage != usage {
		t.Fatalf("terminal/failed usage=%+v %+v", sink.events, res.Trace)
	}
	if len(pool.records) != 2 || journal.Snapshot()[0].Attempts[0].Outcome != requestlog.AttemptServer {
		t.Fatalf("records=%+v journal=%+v", pool.records, journal.Snapshot())
	}
}

func TestNilRunnerErrorStillPublishesHeldFailure(t *testing.T) {
	usage := canon.Usage{TotalTokens: 9}
	first := runnerFunc(func(_ provider.RunRequest, out provider.Sink) error {
		return out.Emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: "failure"}, Usage: usage})
	})
	journal := requestlog.New(8, time.Now)
	sink := &recordingSink{}
	res := NewRouter(poolWith("edge", 1), fakeRunners{"edge": first}, singlePlan("edge", TurnPolicy{}), "default", journal).Turn(context.Background(), canon.Request{Model: "gpt-5.2"}, execution.Facts{}, fakeLifecycle{}, sink)
	failed, ok := res.Terminal.(Failed)
	if !ok || failed.Event.Failure.Reason != canon.FailServerOverloaded || failed.Event.Usage != usage || len(sink.events) != 0 || journal.Snapshot()[0].Terminal.Usage != usage {
		t.Fatalf("result=%+v events=%+v journal=%+v", res, sink.events, journal.Snapshot())
	}
}

func TestVisibleStateOrRejectedTerminalPreventsReplay(t *testing.T) {
	for _, state := range []bool{false, true} {
		sink := &recordingSink{}
		if !state {
			sink.err = errors.New("downstream rejected terminal")
		}
		first := runnerFunc(func(_ provider.RunRequest, out provider.Sink) error {
			if state {
				_ = out.Emit(canon.ItemStateAvailable{ItemID: "reasoning", State: canon.OpaqueRef{Store: canon.StoreWire, Key: "opaque"}})
			} else {
				_ = out.Emit(canon.TurnFinished{Status: canon.Completed()})
			}
			return provider.RunError{Kind: provider.Retryable, Class: provider.ClassTransport}
		})
		pool := &fakePool{results: []leaseResult{{lease: testLease("edge", 0)}, {lease: testLease("backup", 0)}}}
		plan := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{testTarget("edge"), testTarget("backup")}}}
		res := runTurn(t, pool, fakeRunners{"edge": first, "backup": successRunner(canon.Usage{})}, plan, provider.NotStarted, sink)
		if _, ok := res.Terminal.(Failed); !ok || len(pool.calls) != 1 {
			t.Fatalf("state=%t result=%+v calls=%d", state, res, len(pool.calls))
		}
		if _, ok := pool.records[0].outcome.(account.RequestRejected); !ok {
			t.Fatalf("outcome=%T", pool.records[0].outcome)
		}
	}
}

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
