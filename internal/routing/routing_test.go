package routing

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/execution"
	"prism/internal/provider"
	"prism/internal/requestlog"
)

type fakeLifecycle struct{ state provider.CommitState }

func (f fakeLifecycle) CommitState() provider.CommitState { return f.state }

type leaseResult struct {
	lease account.Lease
	err   error
}

type fakeRecord struct {
	lease   account.Lease
	outcome account.Outcome
}

type fakePool struct {
	results []leaseResult
	calls   []account.AcquireRequest
	records []fakeRecord
	idx     int
}

func (p *fakePool) Acquire(_ context.Context, req account.AcquireRequest) (account.Lease, error) {
	p.calls = append(p.calls, req)
	if p.idx >= len(p.results) {
		return account.Lease{}, account.ErrNoAccount
	}
	r := p.results[p.idx]
	p.idx++
	return r.lease, r.err
}

func (p *fakePool) Record(_ context.Context, l account.Lease, o account.Outcome) error {
	p.records = append(p.records, fakeRecord{lease: l, outcome: o})
	return nil
}

func (p *fakePool) Pause(context.Context, account.AccountID, account.StateVersion) error {
	return nil
}

func (p *fakePool) Resume(context.Context, account.AccountID, account.StateVersion) error {
	return nil
}

func (p *fakePool) UpdatePriority(context.Context, account.AccountID, int, account.StateVersion) error {
	return nil
}

func (p *fakePool) Snapshot() account.Snapshot { return account.Snapshot{} }

type fakeRunners map[account.ProviderID]provider.Runner

func (f fakeRunners) Lookup(id account.ProviderID) (provider.Runner, bool) {
	r, ok := f[id]
	return r, ok
}

type fakePlanner map[canon.ModelID]Plan

func (f fakePlanner) Plan(m canon.ModelID) (Plan, bool) {
	p, ok := f[m]
	return p, ok
}

type runnerFunc func(provider.RunRequest, provider.Sink) error

func (f runnerFunc) Run(_ context.Context, req provider.RunRequest, sink provider.Sink) error {
	return f(req, sink)
}

type recordingSink struct {
	events []canon.Event
	failAt int
	err    error
}

func (s *recordingSink) Emit(ev canon.Event) error {
	if s.err != nil && len(s.events) == s.failAt {
		return s.err
	}
	s.events = append(s.events, ev)
	return nil
}

func testLease(pid account.ProviderID, n int) account.Lease {
	return account.Lease{
		Provider: pid,
		Account:  account.AccountID(fmt.Sprintf("%s:acct-%d", pid, n)),
		CredGen:  1,
		Version:  1,
	}
}

func poolWith(pid account.ProviderID, n int) *fakePool {
	p := &fakePool{}
	for i := 0; i < n; i++ {
		p.results = append(p.results, leaseResult{lease: testLease(pid, i)})
	}
	return p
}

func testTarget(pid account.ProviderID, maxFailovers int) provider.Target {
	return provider.Target{Provider: pid, Model: "gpt-5.2", MaxFailovers: maxFailovers}
}

func singlePlan(pid account.ProviderID, maxFailovers int, policy TurnPolicy) fakePlanner {
	return fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{testTarget(pid, maxFailovers)}, Policy: policy}}
}

func testFacts() execution.Facts {
	return execution.Facts{RequestID: "r1", Client: execution.ClientCodex, Session: "s1", Thread: "t1"}
}

func testJournal(t *testing.T) *requestlog.Journal {
	t.Helper()
	return requestlog.New(0, time.Now)
}

func successRunner(usage canon.Usage) runnerFunc {
	return runnerFunc(func(_ provider.RunRequest, sink provider.Sink) error {
		if err := sink.Emit(canon.TextDelta{ItemID: "i1", Text: "hi"}); err != nil {
			return err
		}
		return sink.Emit(canon.TurnFinished{Status: canon.Completed(), Usage: usage})
	})
}

func failRunner(err error) runnerFunc {
	return runnerFunc(func(provider.RunRequest, provider.Sink) error { return err })
}

func failThenSuccess(fails int, runErr error, usage canon.Usage) runnerFunc {
	calls := 0
	return runnerFunc(func(_ provider.RunRequest, sink provider.Sink) error {
		calls++
		if calls <= fails {
			return runErr
		}
		return successRunner(usage)(provider.RunRequest{}, sink)
	})
}

func emitThenFail(evs []canon.Event, re provider.RunError) runnerFunc {
	return runnerFunc(func(_ provider.RunRequest, sink provider.Sink) error {
		for _, ev := range evs {
			if err := sink.Emit(ev); err != nil {
				return err
			}
		}
		return re
	})
}

func gatedRunner(evs []canon.Event, re provider.RunError, succeedSecond bool) runnerFunc {
	calls := 0
	return runnerFunc(func(_ provider.RunRequest, sink provider.Sink) error {
		calls++
		if succeedSecond && calls > 1 {
			return sink.Emit(canon.TurnFinished{Status: canon.Completed()})
		}
		for _, ev := range evs {
			if err := sink.Emit(ev); err != nil {
				return err
			}
		}
		return re
	})
}

func runTurn(t *testing.T, pool *fakePool, runners fakeRunners, planner fakePlanner, lifecycle provider.CommitState, sink provider.Sink) TurnResult {
	t.Helper()
	return NewRouter(pool, runners, planner, "default", testJournal(t)).Turn(context.Background(), canon.Request{Model: "gpt-5.2"}, testFacts(), fakeLifecycle{state: lifecycle}, sink)
}

func TestTurnSuccessRecordsUsageForwardsEventsAndAcquiresWithFacts(t *testing.T) {
	usage := canon.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	sink := &recordingSink{}
	pool := poolWith("codex", 1)
	runners := fakeRunners{"codex": successRunner(usage)}
	planner := singlePlan("codex", 0, TurnPolicy{})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, sink)
	fin, ok := res.Terminal.(Finished)
	if !ok {
		t.Fatalf("terminal is %T, want Finished", res.Terminal)
	}
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", res.Attempts)
	}
	if fin.Event.Usage != usage {
		t.Fatalf("terminal usage = %+v, want %+v", fin.Event.Usage, usage)
	}
	if fin.Event.Status.Kind() != canon.StatusCompleted {
		t.Fatalf("terminal status = %d, want completed", fin.Event.Status.Kind())
	}
	if len(sink.events) != 2 {
		t.Fatalf("client sink events = %d, want 2", len(sink.events))
	}
	if _, ok := sink.events[0].(canon.TextDelta); !ok {
		t.Fatalf("first client event is %T, want TextDelta", sink.events[0])
	}
	if _, ok := sink.events[1].(canon.TurnFinished); !ok {
		t.Fatalf("second client event is %T, want TurnFinished", sink.events[1])
	}
	if len(pool.records) != 1 {
		t.Fatalf("pool records = %d, want 1", len(pool.records))
	}
	succ, ok := pool.records[0].outcome.(account.TurnSucceeded)
	if !ok {
		t.Fatalf("recorded outcome is %T, want TurnSucceeded", pool.records[0].outcome)
	}
	if succ.Usage != usage {
		t.Fatalf("recorded usage = %+v, want %+v", succ.Usage, usage)
	}
	want := account.AcquireRequest{Provider: "codex", Model: "gpt-5.2", QuotaGroup: "default", Session: "s1", Thread: "t1"}
	if pool.calls[0] != want {
		t.Fatalf("acquire request = %+v, want %+v", pool.calls[0], want)
	}
}

func TestTurnSendsTargetModelOnTheWire(t *testing.T) {
	var got canon.Request
	runner := runnerFunc(func(req provider.RunRequest, sink provider.Sink) error {
		got = req.Request
		if err := sink.Emit(canon.TextDelta{ItemID: "i1", Text: "hi"}); err != nil {
			return err
		}
		return sink.Emit(canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 1, TotalTokens: 1}})
	})
	pool := poolWith("codex", 1)
	runners := fakeRunners{"codex": runner}
	planner := fakePlanner{"codex/gpt-5.6": Plan{Targets: []provider.Target{{Provider: "codex", Model: "gpt-5.6", MaxFailovers: 0}}}}
	res := NewRouter(pool, runners, planner, "default", testJournal(t)).Turn(context.Background(), canon.Request{Model: "codex/gpt-5.6"}, testFacts(), fakeLifecycle{state: provider.NotStarted}, &recordingSink{})
	if _, ok := res.Terminal.(Finished); !ok {
		t.Fatalf("terminal is %T, want Finished", res.Terminal)
	}
	if got.Model != "gpt-5.6" {
		t.Fatalf("wire model = %q, want target model gpt-5.6", got.Model)
	}
}

func TestQuotaOutcomeWithoutRetryAfterAppliesPolicyCooldownDefault(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassQuotaExhausted, ReplaySafe: true}
	pool := &fakePool{results: []leaseResult{{lease: testLease("codex", 0)}}}
	runners := fakeRunners{"codex": runnerFunc(func(provider.RunRequest, provider.Sink) error { return runErr })}
	target := provider.Target{Provider: "codex", Model: "gpt-5.2", Policy: account.SelectionPolicy{CooldownDefault: 90 * time.Second}}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{target}, Policy: TurnPolicy{MaxAccountFailovers: 1}}}
	res := NewRouter(pool, runners, planner, "default", testJournal(t)).Turn(context.Background(), canon.Request{Model: "gpt-5.2"}, testFacts(), fakeLifecycle{state: provider.NotStarted}, &recordingSink{})
	if _, ok := res.Terminal.(Failed); !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	want := account.QuotaExhausted{RetryAfter: 90 * time.Second}
	if len(pool.records) != 1 || pool.records[0].outcome != account.Outcome(want) {
		t.Fatalf("recorded outcome = %+v, want %+v", pool.records, want)
	}
}

func TestCooldownMaxCapsRetryAfterForRateLimitedAndQuotaExhausted(t *testing.T) {
	cases := []struct {
		name       string
		class      provider.ErrorClass
		retryAfter time.Duration
		want       account.Outcome
	}{
		{
			name:       "rateLimited capped",
			class:      provider.ClassRateLimited,
			retryAfter: 24 * time.Hour,
			want:       account.Outcome(account.RateLimited{RetryAfter: 15 * time.Minute}),
		},
		{
			name:       "quotaExhausted capped",
			class:      provider.ClassQuotaExhausted,
			retryAfter: 24 * time.Hour,
			want:       account.Outcome(account.QuotaExhausted{RetryAfter: 15 * time.Minute}),
		},
		{
			name:       "rateLimited below cap passes through",
			class:      provider.ClassRateLimited,
			retryAfter: 7 * time.Second,
			want:       account.Outcome(account.RateLimited{RetryAfter: 7 * time.Second}),
		},
		{
			name:       "quotaExhausted below cap passes through",
			class:      provider.ClassQuotaExhausted,
			retryAfter: 7 * time.Second,
			want:       account.Outcome(account.QuotaExhausted{RetryAfter: 7 * time.Second}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runErr := provider.RunError{Kind: provider.Retryable, Class: tc.class, ReplaySafe: true, RetryAfter: tc.retryAfter}
			pool := &fakePool{results: []leaseResult{{lease: testLease("codex", 0)}}}
			runners := fakeRunners{"codex": failRunner(runErr)}
			target := provider.Target{Provider: "codex", Model: "gpt-5.2", Policy: account.SelectionPolicy{CooldownMax: 15 * time.Minute}}
			planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{target}, Policy: TurnPolicy{MaxAccountFailovers: 1}}}
			res := NewRouter(pool, runners, planner, "default", testJournal(t)).Turn(context.Background(), canon.Request{Model: "gpt-5.2"}, testFacts(), fakeLifecycle{state: provider.NotStarted}, &recordingSink{})
			if _, ok := res.Terminal.(Failed); !ok {
				t.Fatalf("terminal is %T, want Failed", res.Terminal)
			}
			if len(pool.records) != 1 || pool.records[0].outcome != tc.want {
				t.Fatalf("recorded outcome = %+v, want %+v", pool.records, tc.want)
			}
		})
	}
}

func TestTurnAccountFailoverPreservesRetryAfter(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true, RetryAfter: 7 * time.Second}
	usage := canon.Usage{OutputTokens: 3}
	pool := poolWith("codex", 2)
	runners := fakeRunners{"codex": failThenSuccess(1, runErr, usage)}
	planner := singlePlan("codex", 0, TurnPolicy{})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", res.Attempts)
	}
	if _, ok := res.Terminal.(Finished); !ok {
		t.Fatalf("terminal is %T, want Finished", res.Terminal)
	}
	if len(pool.calls) != 2 || pool.calls[0].Provider != "codex" || pool.calls[1].Provider != "codex" {
		t.Fatalf("acquires = %+v, want two on codex", pool.calls)
	}
	if len(pool.records) != 2 {
		t.Fatalf("pool records = %d, want 2", len(pool.records))
	}
	want := account.RateLimited{RetryAfter: 7 * time.Second}
	if pool.records[0].outcome != account.Outcome(want) {
		t.Fatalf("recorded outcome = %+v, want %+v", pool.records[0].outcome, want)
	}
	if _, ok := pool.records[1].outcome.(account.TurnSucceeded); !ok {
		t.Fatalf("second recorded outcome is %T, want TurnSucceeded", pool.records[1].outcome)
	}
}

func TestTurnTargetFailoverMovesToNextTarget(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassTransport, ReplaySafe: true}
	usage := canon.Usage{OutputTokens: 2}
	pool := &fakePool{results: []leaseResult{{lease: testLease("codex", 0)}, {lease: testLease("antigravity", 0)}}}
	runners := fakeRunners{"codex": failRunner(runErr), "antigravity": successRunner(usage)}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{testTarget("codex", 1), testTarget("antigravity", 1)}}}
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", res.Attempts)
	}
	if pool.calls[0].Provider != "codex" || pool.calls[1].Provider != "antigravity" {
		t.Fatalf("acquire providers = %q, %q, want codex then antigravity", pool.calls[0].Provider, pool.calls[1].Provider)
	}
	if _, ok := res.Terminal.(Finished); !ok {
		t.Fatalf("terminal is %T, want Finished", res.Terminal)
	}
}

func TestTurnJournalRecordsFailoverTrail(t *testing.T) {
	clock := &fakeJournalClock{t: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
	j := requestlog.New(8, clock.Now)
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true, RetryAfter: 7 * time.Second}
	usage := canon.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	pool := &fakePool{results: []leaseResult{{lease: testLease("codex", 0)}, {lease: testLease("codex", 1)}}}
	runners := fakeRunners{"codex": failThenSuccess(1, runErr, usage)}
	planner := singlePlan("codex", 0, TurnPolicy{})
	res := NewRouter(pool, runners, planner, "default", j).Turn(context.Background(), canon.Request{Model: "gpt-5.2"}, testFacts(), fakeLifecycle{state: provider.NotStarted}, &recordingSink{})
	if _, ok := res.Terminal.(Finished); !ok {
		t.Fatalf("terminal is %T, want Finished", res.Terminal)
	}
	snap := j.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("journal entries = %d, want 1", len(snap))
	}
	e := snap[0]
	if e.Status != requestlog.StatusCompleted {
		t.Fatalf("status = %v, want completed", e.Status)
	}
	if e.Model != "gpt-5.2" || e.RequestID != "r1" || e.Client != execution.ClientCodex || e.Session != "s1" {
		t.Fatalf("entry identity = %+v", e)
	}
	if e.Terminal.Usage != usage {
		t.Fatalf("terminal usage = %+v, want %+v", e.Terminal.Usage, usage)
	}
	if len(e.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(e.Attempts))
	}
	first, second := e.Attempts[0], e.Attempts[1]
	if first.Outcome != requestlog.AttemptRateLimited {
		t.Fatalf("first outcome = %v, want rate_limited", first.Outcome)
	}
	if first.Error != runErr.Error() {
		t.Fatalf("first error = %q, want RunError.Error()", first.Error)
	}
	if second.Outcome != requestlog.AttemptSucceeded {
		t.Fatalf("second outcome = %v, want succeeded", second.Outcome)
	}
	if second.Error != "" {
		t.Fatalf("second error = %q, want empty", second.Error)
	}
	if first.AccountID != "codex:acct-0" || second.AccountID != "codex:acct-1" {
		t.Fatalf("accounts = %q, %q, want distinct leases", first.AccountID, second.AccountID)
	}
	if first.Model != "gpt-5.2" {
		t.Fatalf("attempt model = %q, want wire model", first.Model)
	}
}

func TestTurnJournalFailedTurnMapsTerminalReason(t *testing.T) {
	j := requestlog.New(8, time.Now)
	runErr := provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: errors.New("bad request")}
	pool := poolWith("codex", 1)
	runners := fakeRunners{"codex": failRunner(runErr)}
	planner := singlePlan("codex", 0, TurnPolicy{})
	res := NewRouter(pool, runners, planner, "default", j).Turn(context.Background(), canon.Request{Model: "gpt-5.2"}, testFacts(), fakeLifecycle{state: provider.NotStarted}, &recordingSink{})
	if _, ok := res.Terminal.(Failed); !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	snap := j.Snapshot()
	if len(snap) != 1 || snap[0].Status != requestlog.StatusFailed {
		t.Fatalf("journal = %+v, want single failed entry", snap)
	}
	e := snap[0]
	if !e.Terminal.Failed || e.Terminal.Reason != canon.FailInvalidRequest {
		t.Fatalf("terminal = %+v, want failed invalid_request", e.Terminal)
	}
	if len(e.Attempts) != 1 || e.Attempts[0].Outcome != requestlog.AttemptInvalidRequest {
		t.Fatalf("attempts = %+v, want one invalid_request", e.Attempts)
	}
}

func TestTurnJournalNoAttemptPathsStillOpenAndClose(t *testing.T) {
	j := requestlog.New(8, time.Now)
	pool := poolWith("codex", 1)
	runners := fakeRunners{"codex": successRunner(canon.Usage{})}
	res := NewRouter(pool, runners, fakePlanner{}, "default", j).Turn(context.Background(), canon.Request{Model: "gpt-5.2"}, testFacts(), fakeLifecycle{state: provider.NotStarted}, &recordingSink{})
	if _, ok := res.Terminal.(Failed); !ok {
		t.Fatalf("terminal is %T, want Failed for unknown model", res.Terminal)
	}
	snap := j.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("journal entries = %d, want 1 (turn still journaled)", len(snap))
	}
	if snap[0].Status != requestlog.StatusFailed {
		t.Fatalf("status = %v, want failed", snap[0].Status)
	}
	if snap[0].Terminal.Reason != canon.FailNotFound {
		t.Fatalf("reason = %v, want not_found", snap[0].Terminal.Reason)
	}
	if len(snap[0].Attempts) != 0 {
		t.Fatalf("attempts = %d, want 0 (failed before any run)", len(snap[0].Attempts))
	}
}

type fakeJournalClock struct{ t time.Time }

func (c *fakeJournalClock) Now() time.Time { return c.t }

func TestTurnCommitStateAndRunErrorGateFailover(t *testing.T) {
	failure := canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailServerOverloaded, Message: "upstream died"}}
	cases := []struct {
		name          string
		state         provider.CommitState
		events        []canon.Event
		runErr        provider.RunError
		succeedSecond bool
		attempts      int
		success       bool
		reason        canon.FailureReason
		message       string
		records       int
	}{
		{
			name:          "notStarted retryable server fails over",
			state:         provider.NotStarted,
			runErr:        provider.RunError{Kind: provider.Retryable, Class: provider.ClassServer},
			succeedSecond: true,
			attempts:      2,
			success:       true,
			records:       2,
		},
		{
			name:          "responseStarted retryable server still fails over",
			state:         provider.ResponseStarted,
			runErr:        provider.RunError{Kind: provider.Retryable, Class: provider.ClassServer},
			succeedSecond: true,
			attempts:      2,
			success:       true,
			records:       2,
		},
		{
			name:     "outputCommitted never restarts",
			state:    provider.OutputCommitted,
			runErr:   provider.RunError{Kind: provider.Retryable, Class: provider.ClassServer},
			attempts: 1,
			reason:   canon.FailServerOverloaded,
			records:  1,
		},
		{
			name:     "terminalEmitted returns captured event",
			state:    provider.NotStarted,
			events:   []canon.Event{failure},
			runErr:   provider.RunError{Kind: provider.TerminalEmitted, Class: provider.ClassServer},
			attempts: 1,
			reason:   canon.FailServerOverloaded,
			message:  "upstream died",
			records:  1,
		},
		{
			name:     "terminalEmitted postCommit returns captured event",
			state:    provider.OutputCommitted,
			events:   []canon.Event{failure},
			runErr:   provider.RunError{Kind: provider.TerminalEmitted, Class: provider.ClassServer},
			attempts: 1,
			reason:   canon.FailServerOverloaded,
			message:  "upstream died",
			records:  1,
		},
		{
			name:     "terminalOmitted invalidRequest releases lease",
			state:    provider.NotStarted,
			runErr:   provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: errors.New("bad request")},
			attempts: 1,
			reason:   canon.FailInvalidRequest,
			records:  1,
		},
		{
			name:     "terminalOmitted contextLength releases lease",
			state:    provider.NotStarted,
			runErr:   provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassContextLength},
			attempts: 1,
			reason:   canon.FailContextLength,
			records:  1,
		},
		{
			name:     "unsafeReplay never fails over",
			state:    provider.NotStarted,
			runErr:   provider.RunError{Kind: provider.UnsafeReplay, Class: provider.ClassTransport, Accepted: true},
			attempts: 1,
			reason:   canon.FailUpstreamTransport,
			records:  1,
		},
		{
			name:     "unsafeReplay postCommit never fails over",
			state:    provider.OutputCommitted,
			runErr:   provider.RunError{Kind: provider.UnsafeReplay, Class: provider.ClassTransport, Accepted: true},
			attempts: 1,
			reason:   canon.FailUpstreamTransport,
			records:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := poolWith("codex", 5)
			runners := fakeRunners{"codex": gatedRunner(tc.events, tc.runErr, tc.succeedSecond)}
			planner := singlePlan("codex", 0, TurnPolicy{})
			res := runTurn(t, pool, runners, planner, tc.state, &recordingSink{})
			if res.Attempts != tc.attempts {
				t.Fatalf("attempts = %d, want %d", res.Attempts, tc.attempts)
			}
			if tc.success {
				fin, ok := res.Terminal.(Finished)
				if !ok {
					t.Fatalf("terminal is %T, want Finished", res.Terminal)
				}
				if fin.Event.Status.Kind() != canon.StatusCompleted {
					t.Fatalf("terminal status = %d, want completed", fin.Event.Status.Kind())
				}
			} else {
				f, ok := res.Terminal.(Failed)
				if !ok {
					t.Fatalf("terminal is %T, want Failed", res.Terminal)
				}
				if f.Event.Failure.Reason != tc.reason {
					t.Fatalf("failure reason = %d, want %d", f.Event.Failure.Reason, tc.reason)
				}
				if tc.message != "" && f.Event.Failure.Message != tc.message {
					t.Fatalf("failure message = %q, want %q", f.Event.Failure.Message, tc.message)
				}
			}
			if len(pool.records) != tc.records {
				t.Fatalf("pool records = %d, want %d", len(pool.records), tc.records)
			}
		})
	}
}

func TestTargetMaxFailoversCapsAttemptsAndAbortsPoolExhausted(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true, RetryAfter: time.Second}
	pool := poolWith("antigravity", 6)
	runners := fakeRunners{"antigravity": failRunner(runErr)}
	planner := singlePlan("antigravity", 3, TurnPolicy{})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", res.Attempts)
	}
	if len(pool.calls) != 3 {
		t.Fatalf("acquires = %d, want 3", len(pool.calls))
	}
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailQuotaExhausted {
		t.Fatalf("failure reason = %d, want pool exhausted style %d", f.Event.Failure.Reason, canon.FailQuotaExhausted)
	}
	if len(pool.records) != 3 {
		t.Fatalf("pool records = %d, want 3", len(pool.records))
	}
}

func TestRequestAccountBudgetCapsSwitchesBeforeTargetBudget(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true}
	pool := poolWith("codex", 6)
	runners := fakeRunners{"codex": failRunner(runErr)}
	planner := singlePlan("codex", 5, TurnPolicy{MaxAccountFailovers: 2})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", res.Attempts)
	}
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailQuotaExhausted {
		t.Fatalf("failure reason = %d, want %d", f.Event.Failure.Reason, canon.FailQuotaExhausted)
	}
}

func TestPoolExhaustedMidTurnAborts(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true}
	pool := &fakePool{results: []leaseResult{{lease: testLease("codex", 0)}, {err: account.ErrNoAccount}}}
	runners := fakeRunners{"codex": failRunner(runErr)}
	planner := singlePlan("codex", 3, TurnPolicy{})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", res.Attempts)
	}
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailQuotaExhausted || f.Event.Failure.Message != "routing: pool exhausted" {
		t.Fatalf("failure = %+v, want pool exhausted", f.Event.Failure)
	}
}

func TestTargetSwitchBudgetExhaustedAborts(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassServer}
	pool := &fakePool{results: []leaseResult{{lease: testLease("codex", 0)}, {lease: testLease("antigravity", 0)}}}
	runners := fakeRunners{"codex": failRunner(runErr), "antigravity": failRunner(runErr)}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{testTarget("codex", 1), testTarget("antigravity", 1)}}}
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", res.Attempts)
	}
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailQuotaExhausted {
		t.Fatalf("failure reason = %d, want %d", f.Event.Failure.Reason, canon.FailQuotaExhausted)
	}
}

func TestDefaultTargetBudgetReachesThirdTarget(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassServer}
	usage := canon.Usage{OutputTokens: 1}
	pool := &fakePool{results: []leaseResult{
		{lease: testLease("codex", 0)},
		{lease: testLease("antigravity", 0)},
		{lease: testLease("openai-2", 0)},
	}}
	runners := fakeRunners{"codex": failRunner(runErr), "antigravity": failRunner(runErr), "openai-2": successRunner(usage)}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{
		testTarget("codex", 1),
		testTarget("antigravity", 1),
		testTarget("openai-2", 1),
	}}}
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", res.Attempts)
	}
	if _, ok := res.Terminal.(Finished); !ok {
		t.Fatalf("terminal is %T, want Finished", res.Terminal)
	}
}

func TestUntypedRunnerErrorStopsTurnReleasingLease(t *testing.T) {
	pool := poolWith("codex", 2)
	runners := fakeRunners{"codex": failRunner(errors.New("boom"))}
	planner := singlePlan("codex", 0, TurnPolicy{})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", res.Attempts)
	}
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailUnknown {
		t.Fatalf("failure reason = %d, want %d", f.Event.Failure.Reason, canon.FailUnknown)
	}
	if len(pool.records) != 1 {
		t.Fatalf("pool records = %d, want 1 (lease release)", len(pool.records))
	}
}

func TestSinkErrorStopsTurn(t *testing.T) {
	sink := &recordingSink{failAt: 0, err: errors.New("client gone")}
	pool := poolWith("codex", 2)
	re := provider.RunError{Kind: provider.Retryable, Class: provider.ClassServer}
	runners := fakeRunners{"codex": emitThenFail([]canon.Event{canon.TextDelta{ItemID: "i1", Text: "hi"}}, re)}
	planner := singlePlan("codex", 0, TurnPolicy{})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, sink)
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", res.Attempts)
	}
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailUnknown {
		t.Fatalf("failure reason = %d, want %d", f.Event.Failure.Reason, canon.FailUnknown)
	}
	if len(pool.calls) != 1 {
		t.Fatalf("acquires = %d, want 1", len(pool.calls))
	}
}

func TestSuccessWithoutTerminalReleasesLease(t *testing.T) {
	pool := poolWith("codex", 2)
	runners := fakeRunners{"codex": failRunner(nil)}
	planner := singlePlan("codex", 0, TurnPolicy{})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", res.Attempts)
	}
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailUnknown {
		t.Fatalf("failure reason = %d, want %d", f.Event.Failure.Reason, canon.FailUnknown)
	}
	if len(pool.records) != 1 {
		t.Fatalf("pool records = %d, want 1 (lease release)", len(pool.records))
	}
}

func TestAcquireFailureStopsTurn(t *testing.T) {
	pool := &fakePool{results: []leaseResult{{err: errors.New("db down")}}}
	runners := fakeRunners{"codex": successRunner(canon.Usage{})}
	planner := singlePlan("codex", 0, TurnPolicy{})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if res.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", res.Attempts)
	}
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailUnknown {
		t.Fatalf("failure reason = %d, want %d", f.Event.Failure.Reason, canon.FailUnknown)
	}
}

func TestUnknownModelFailsNotFound(t *testing.T) {
	pool := poolWith("codex", 1)
	runners := fakeRunners{"codex": successRunner(canon.Usage{})}
	res := runTurn(t, pool, runners, fakePlanner{}, provider.NotStarted, &recordingSink{})
	if res.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", res.Attempts)
	}
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailNotFound {
		t.Fatalf("failure reason = %d, want %d", f.Event.Failure.Reason, canon.FailNotFound)
	}
	if len(pool.calls) != 0 {
		t.Fatalf("acquires = %d, want 0", len(pool.calls))
	}
}

func TestEmptyPlanFails(t *testing.T) {
	pool := poolWith("codex", 1)
	runners := fakeRunners{"codex": successRunner(canon.Usage{})}
	planner := fakePlanner{"gpt-5.2": Plan{}}
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailUnknown {
		t.Fatalf("failure reason = %d, want %d", f.Event.Failure.Reason, canon.FailUnknown)
	}
}

func TestMissingRunnerFails(t *testing.T) {
	pool := poolWith("codex", 1)
	runners := fakeRunners{}
	planner := singlePlan("codex", 0, TurnPolicy{})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	f, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal is %T, want Failed", res.Terminal)
	}
	if f.Event.Failure.Reason != canon.FailUnknown {
		t.Fatalf("failure reason = %d, want %d", f.Event.Failure.Reason, canon.FailUnknown)
	}
	if res.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", res.Attempts)
	}
}

func TestTurnReleasesLeaseForNonMappableError(t *testing.T) {
	pid := account.ProviderID("p1")
	pool := poolWith(pid, 1)
	err := provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest}
	runTurn(t, pool, fakeRunners{pid: failRunner(err)}, singlePlan(pid, 1, TurnPolicy{}), provider.NotStarted, &recordingSink{})
	if len(pool.records) != 1 {
		t.Fatalf("records = %d, want lease release", len(pool.records))
	}
}

func TestTurnReleasesLeaseForUntypedRunnerError(t *testing.T) {
	pid := account.ProviderID("p1")
	pool := poolWith(pid, 1)
	runTurn(t, pool, fakeRunners{pid: failRunner(errors.New("boom"))}, singlePlan(pid, 1, TurnPolicy{}), provider.NotStarted, &recordingSink{})
	if len(pool.records) != 1 {
		t.Fatalf("records = %d, want lease release", len(pool.records))
	}
}

func TestTurnReleasesLeaseWhenRunnerOmitsTerminal(t *testing.T) {
	pid := account.ProviderID("p1")
	pool := poolWith(pid, 1)
	runner := runnerFunc(func(provider.RunRequest, provider.Sink) error { return nil })
	runTurn(t, pool, fakeRunners{pid: runner}, singlePlan(pid, 1, TurnPolicy{}), provider.NotStarted, &recordingSink{})
	if len(pool.records) != 1 {
		t.Fatalf("records = %d, want lease release", len(pool.records))
	}
}

func TestTurnRejectsImageInputForTextOnlyTarget(t *testing.T) {
	pid := account.ProviderID("p")
	runner := runnerFunc(func(req provider.RunRequest, sink provider.Sink) error {
		t.Fatal("runner must not be called for a text-only image request")
		return nil
	})
	target := targetWithImageInput(testTarget(pid, 1), false)
	req := canon.Request{
		Model: "gpt-5.2",
		Input: []canon.Item{canon.Message{
			Role:    canon.RoleUser,
			Content: []canon.Content{canon.TextContent{Text: "read"}, canon.ImageContent{MIMEType: "image/png", Data: []byte{1}}},
		}},
	}
	res := NewRouter(poolWith(pid, 1), fakeRunners{pid: runner}, fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{target}}}, account.QuotaGroup("default"), testJournal(t)).Turn(
		context.Background(), req, execution.Facts{}, fakeLifecycle{}, &recordingSink{},
	)
	failed, ok := res.Terminal.(Failed)
	if !ok {
		t.Fatalf("terminal = %#v, want Failed", res.Terminal)
	}
	if failed.Event.Failure.Reason != canon.FailInvalidRequest {
		t.Fatalf("reason = %v, want FailInvalidRequest", failed.Event.Failure.Reason)
	}
}

func TestTurnAllowsImageInputForVisionTarget(t *testing.T) {
	pid := account.ProviderID("p")
	runner := runnerFunc(func(req provider.RunRequest, sink provider.Sink) error {
		return sink.Emit(canon.TurnFinished{})
	})
	target := targetWithImageInput(testTarget(pid, 1), true)
	req := canon.Request{
		Model: "gpt-5.2",
		Input: []canon.Item{canon.Message{
			Role:    canon.RoleUser,
			Content: []canon.Content{canon.TextContent{Text: "read"}, canon.ImageContent{MIMEType: "image/png", Data: []byte{1}}},
		}},
	}
	res := NewRouter(poolWith(pid, 1), fakeRunners{pid: runner}, fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{target}}}, account.QuotaGroup("default"), testJournal(t)).Turn(
		context.Background(), req, execution.Facts{}, fakeLifecycle{}, &recordingSink{},
	)
	if _, ok := res.Terminal.(Finished); !ok {
		t.Fatalf("terminal = %#v, want Finished", res.Terminal)
	}
}

func TestTurnTraceOnSuccess(t *testing.T) {
	usage := canon.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
	res := runTurn(t, poolWith("codex", 1), fakeRunners{"codex": successRunner(usage)}, singlePlan("codex", 1, TurnPolicy{}), provider.NotStarted, &recordingSink{})
	if len(res.Trace) != 1 {
		t.Fatalf("trace length = %d, want 1", len(res.Trace))
	}
	want := AttemptTrace{Provider: "codex", Model: "gpt-5.2", Outcome: "completed", Usage: usage}
	if res.Trace[0] != want {
		t.Fatalf("trace = %+v, want %+v", res.Trace[0], want)
	}
}

func TestTurnTraceOnFailover(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassTransport, ReplaySafe: true}
	pool := &fakePool{results: []leaseResult{{lease: testLease("codex", 0)}, {lease: testLease("antigravity", 0)}}}
	usage := canon.Usage{OutputTokens: 2, TotalTokens: 2}
	runners := fakeRunners{"codex": failRunner(runErr), "antigravity": successRunner(usage)}
	planner := fakePlanner{"gpt-5.2": Plan{Targets: []provider.Target{testTarget("codex", 1), testTarget("antigravity", 1)}}}
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if len(res.Trace) != 2 {
		t.Fatalf("trace length = %d, want 2", len(res.Trace))
	}
	if res.Trace[0] != (AttemptTrace{Provider: "codex", Model: "gpt-5.2", Outcome: "failed"}) {
		t.Fatalf("first trace = %+v, want codex failed with zero usage", res.Trace[0])
	}
	if res.Trace[1] != (AttemptTrace{Provider: "antigravity", Model: "gpt-5.2", Outcome: "completed", Usage: usage}) {
		t.Fatalf("second trace = %+v, want antigravity completed with usage", res.Trace[1])
	}
}

func TestTurnTraceOnFailureCarriesProviderAttempts(t *testing.T) {
	runErr := provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true}
	pool := poolWith("codex", 2)
	runners := fakeRunners{"codex": failRunner(runErr)}
	planner := singlePlan("codex", 2, TurnPolicy{MaxAccountFailovers: 1})
	res := runTurn(t, pool, runners, planner, provider.NotStarted, &recordingSink{})
	if _, ok := res.Terminal.(Failed); !ok {
		t.Fatalf("terminal = %T, want Failed", res.Terminal)
	}
	if len(res.Trace) != 2 {
		t.Fatalf("trace length = %d, want 2", len(res.Trace))
	}
	for i, want := range []AttemptTrace{{Provider: "codex", Model: "gpt-5.2", Outcome: "failed"}, {Provider: "codex", Model: "gpt-5.2", Outcome: "failed"}} {
		if res.Trace[i] != want {
			t.Fatalf("trace[%d] = %+v, want %+v", i, res.Trace[i], want)
		}
	}
}

func TestTurnTraceEmptyWhenNoPlan(t *testing.T) {
	res := runTurn(t, poolWith("codex", 1), fakeRunners{}, fakePlanner{}, provider.NotStarted, &recordingSink{})
	if len(res.Trace) != 0 {
		t.Fatalf("trace length = %d, want 0", len(res.Trace))
	}
}
