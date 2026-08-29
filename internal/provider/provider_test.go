package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/execution"
	"prism/internal/quota"
)

func TestCommitStateTotalOrder(t *testing.T) {
	states := []CommitState{NotStarted, ResponseStarted, OutputCommitted}
	for i, a := range states {
		for j, b := range states {
			switch {
			case i < j:
				if !(a < b) {
					t.Fatalf("CommitState(%d) must order before CommitState(%d)", a, b)
				}
			case i > j:
				if !(a > b) {
					t.Fatalf("CommitState(%d) must order after CommitState(%d)", a, b)
				}
			default:
				if !(a == b) {
					t.Fatalf("CommitState(%d) must equal itself", a)
				}
			}
		}
	}
	for i := range states {
		for j := range states {
			for k := range states {
				if states[i] < states[j] && states[j] < states[k] && !(states[i] < states[k]) {
					t.Fatalf("order not transitive at %d<%d<%d", i, j, k)
				}
			}
		}
	}
}

func TestCommitStateFailoverBoundary(t *testing.T) {
	cases := []struct {
		state   CommitState
		allowed bool
	}{
		{NotStarted, true},
		{ResponseStarted, true},
		{OutputCommitted, false},
	}
	for _, tc := range cases {
		if got := tc.state < OutputCommitted; got != tc.allowed {
			t.Fatalf("failover at CommitState(%d): got %v, want %v", tc.state, got, tc.allowed)
		}
	}
}

func TestRunErrorKindsClosedAndDistinct(t *testing.T) {
	kinds := []RunErrorKind{TerminalEmitted, TerminalOmitted, Retryable, UnsafeReplay}
	seen := make(map[RunErrorKind]bool, len(kinds))
	for _, k := range kinds {
		if k == 0 {
			t.Fatal("RunErrorKind must not reserve zero")
		}
		if seen[k] {
			t.Fatalf("duplicate RunErrorKind value %d", k)
		}
		seen[k] = true
	}
}

func TestRunErrorKindFailoverSemantics(t *testing.T) {
	cases := []struct {
		kind    RunErrorKind
		allowed bool
	}{
		{TerminalEmitted, false},
		{TerminalOmitted, true},
		{Retryable, true},
		{UnsafeReplay, false},
	}
	for _, tc := range cases {
		if got := tc.kind.FailoverAllowed(); got != tc.allowed {
			t.Fatalf("RunErrorKind(%d) failover: got %v, want %v", tc.kind, got, tc.allowed)
		}
	}
}

func TestRunErrorClassesClosedAndDistinct(t *testing.T) {
	classes := []ErrorClass{
		ClassUnauthorized, ClassRateLimited, ClassQuotaExhausted,
		ClassNotFound, ClassTimeout, ClassServer, ClassTransport,
		ClassInvalidRequest, ClassContextLength,
	}
	seen := make(map[ErrorClass]bool, len(classes))
	for _, c := range classes {
		if c == 0 {
			t.Fatal("ErrorClass must not reserve zero")
		}
		if seen[c] {
			t.Fatalf("duplicate ErrorClass value %d", c)
		}
		seen[c] = true
	}
}

func TestRunErrorClassFailoverSemantics(t *testing.T) {
	retryable := []ErrorClass{
		ClassUnauthorized, ClassRateLimited, ClassQuotaExhausted,
		ClassNotFound, ClassTimeout, ClassServer, ClassTransport,
	}
	for _, c := range retryable {
		if !c.FailoverAllowed() {
			t.Fatalf("ErrorClass(%d) must permit failover", c)
		}
	}
	terminal := []ErrorClass{ClassInvalidRequest, ClassContextLength}
	for _, c := range terminal {
		if c.FailoverAllowed() {
			t.Fatalf("ErrorClass(%d) must forbid failover", c)
		}
	}
}

func TestRunErrorAcceptedReplaySafeSemantics(t *testing.T) {
	retryable := RunError{Kind: Retryable, Class: ClassRateLimited, ReplaySafe: true, RetryAfter: 5 * time.Second}
	if retryable.Accepted {
		t.Fatal("pre-output Retryable must not be Accepted")
	}
	if !retryable.ReplaySafe {
		t.Fatal("Retryable must be ReplaySafe")
	}
	if !retryable.Kind.FailoverAllowed() || !retryable.Class.FailoverAllowed() {
		t.Fatal("retryable rate-limited error must permit failover")
	}
	unsafe := RunError{Kind: UnsafeReplay, Class: ClassTransport, Accepted: true}
	if unsafe.ReplaySafe {
		t.Fatal("UnsafeReplay must not be ReplaySafe")
	}
	if !unsafe.Accepted {
		t.Fatal("UnsafeReplay is post-acceptance and must be Accepted")
	}
	if unsafe.Kind.FailoverAllowed() {
		t.Fatal("UnsafeReplay must forbid failover even when the class permits it")
	}
	emitted := RunError{Kind: TerminalEmitted, Class: ClassServer}
	if emitted.Kind.FailoverAllowed() {
		t.Fatal("TerminalEmitted must forbid failover regardless of class")
	}
	omittedInvalid := RunError{Kind: TerminalOmitted, Class: ClassInvalidRequest}
	if omittedInvalid.Kind.FailoverAllowed() && omittedInvalid.Class.FailoverAllowed() {
		t.Fatal("terminal invalid request must not fail over")
	}
	if omittedInvalid.RetryAfter != 0 {
		t.Fatal("RunError without retry guidance must carry zero RetryAfter")
	}
}

func TestTargetFieldCompleteness(t *testing.T) {
	target := Target{
		Provider:     "codex-main",
		Wire:         WireCodex,
		BaseURL:      "https://example.internal",
		APIKeyRef:    "env:OPENAI_API_KEY",
		Model:        "gpt-5.2",
		Timeout:      30 * time.Second,
		MaxFailovers: 3,
	}
	if target.Provider != "codex-main" {
		t.Fatal("Target.Provider not preserved")
	}
	if target.Wire != WireCodex {
		t.Fatal("Target.Wire not preserved")
	}
	if target.BaseURL != "https://example.internal" {
		t.Fatal("Target.BaseURL not preserved")
	}
	if target.APIKeyRef != "env:OPENAI_API_KEY" {
		t.Fatal("Target.APIKeyRef not preserved")
	}
	if target.Model != "gpt-5.2" {
		t.Fatal("Target.Model not preserved")
	}
	if target.Timeout != 30*time.Second {
		t.Fatal("Target.Timeout not preserved")
	}
	if target.MaxFailovers != 3 {
		t.Fatal("Target.MaxFailovers not preserved")
	}
}

func TestWireClosedSet(t *testing.T) {
	wires := []Wire{WireCodex, WireAntigravity, WireResponses, WireMessages}
	seen := make(map[Wire]bool, len(wires))
	for _, w := range wires {
		if w == 0 {
			t.Fatal("Wire must not reserve zero")
		}
		if seen[w] {
			t.Fatalf("duplicate Wire value %d", w)
		}
		seen[w] = true
	}
}

type stubRunner struct{}

func (stubRunner) Run(context.Context, RunRequest, Sink) error { return nil }

func TestRegistryRegisterLookupDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("codex-main", stubRunner{}); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}
	if err := r.Register("codex-main", stubRunner{}); !errors.Is(err, ErrDuplicateProvider) {
		t.Fatalf("duplicate registration: got %v, want ErrDuplicateProvider", err)
	}
	got, ok := r.Lookup("codex-main")
	if !ok {
		t.Fatal("lookup missed registered provider")
	}
	if got == nil {
		t.Fatal("lookup returned nil runner")
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Fatal("lookup returned hit for unregistered provider")
	}
}

func TestRegistryRejectsInvalidRegistrations(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("", stubRunner{}); err == nil {
		t.Fatal("empty provider id must be rejected")
	}
	if err := r.Register("codex-main", nil); err == nil {
		t.Fatal("nil runner must be rejected")
	}
	if _, ok := r.Lookup("codex-main"); ok {
		t.Fatal("rejected registration must not be visible")
	}
}

type captureSink struct{ events []Event }

func (s *captureSink) Emit(ev Event) error {
	s.events = append(s.events, ev)
	return nil
}

type testEvent struct{ n int }

func (testEvent) event() {}

func TestRunnerContractEmitsAndReturns(t *testing.T) {
	runner := funcRunner(func(ctx context.Context, req RunRequest, sink Sink) error {
		if req.Target.MaxFailovers != 3 {
			t.Fatal("RunRequest did not carry target failover budget")
		}
		if req.Lease.Provider == "" {
			t.Fatal("RunRequest did not carry lease")
		}
		if req.Facts.RequestID == "" {
			t.Fatal("RunRequest did not carry facts")
		}
		return sink.Emit(testEvent{n: 1})
	})
	sink := &captureSink{}
	req := RunRequest{
		Request: canon.Request{Model: "gpt-5.2"},
		Target:  Target{Provider: "codex-main", Model: "gpt-5.2", MaxFailovers: 3},
		Lease:   account.Lease{Provider: "codex-main", Account: "codex-main:a1", CredGen: 7, Version: 2},
		Facts:   execution.Facts{RequestID: "req-1", Session: "s1", Thread: "t1"},
	}
	if err := runner.Run(context.Background(), req, sink); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("sink received %d events, want 1", len(sink.events))
	}
}

type funcRunner func(ctx context.Context, req RunRequest, sink Sink) error

func (f funcRunner) Run(ctx context.Context, req RunRequest, sink Sink) error {
	return f(ctx, req, sink)
}

func TestQuotaSourceAndCompactorAndCounterContracts(t *testing.T) {
	var _ QuotaSource = stubQuotaSource{}
	var _ Compactor = stubCompactor{}
	var _ TokenCounter = stubCounter{}
	var _ Catalog = stubCatalog{}
}

type stubQuotaSource struct{}

func (stubQuotaSource) Quota(context.Context, account.AccountID) (quota.Snapshot, error) {
	return quota.Snapshot{}, nil
}

type stubCompactor struct{}

func (stubCompactor) Compact(context.Context, CompactRequest) (CompactResult, error) {
	return CompactResult{}, nil
}

type stubCounter struct{}

func (stubCounter) CountTokens(context.Context, CountTokensRequest) (TokenCount, error) {
	return TokenCount{}, nil
}

type stubCatalog struct{}

func (stubCatalog) Models(context.Context) ([]Model, error) {
	return []Model{{ID: "gpt-5.2", Alias: "gpt-5.2", Caps: ModelCaps{Reasoning: true}}}, nil
}
