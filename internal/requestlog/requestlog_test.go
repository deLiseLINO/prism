package requestlog

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/execution"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func testClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
}

func testFacts() execution.Facts {
	return execution.Facts{RequestID: "r1", Client: execution.ClientCodex, Session: "s1", Thread: "t1"}
}

func TestOpenPublishesOpenEntryAndCloseFinalizes(t *testing.T) {
	clock := testClock()
	j := New(4, clock.Now)
	turn := j.Open(testFacts(), "gpt-5.2")
	snap := j.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1 (open entry published)", len(snap))
	}
	if snap[0].Status != StatusOpen {
		t.Fatalf("status = %v, want open", snap[0].Status)
	}
	if snap[0].Duration != 0 {
		t.Fatalf("duration = %v, want 0 while open", snap[0].Duration)
	}
	if snap[0].Seq != 1 {
		t.Fatalf("seq = %d, want 1", snap[0].Seq)
	}
	if snap[0].Model != "gpt-5.2" || snap[0].RequestID != "r1" || snap[0].Client != execution.ClientCodex {
		t.Fatalf("entry = %+v, want facts copied", snap[0])
	}
	clock.advance(1500 * time.Millisecond)
	turn.Close(Terminal{Status: StatusCompleted, Usage: canon.Usage{TotalTokens: 5}})
	snap = j.Snapshot()
	if snap[0].Status != StatusCompleted {
		t.Fatalf("status = %v, want completed", snap[0].Status)
	}
	if snap[0].Duration != 1500*time.Millisecond {
		t.Fatalf("duration = %v, want 1.5s", snap[0].Duration)
	}
	if snap[0].Terminal.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v, want copied", snap[0].Terminal.Usage)
	}
}

func TestAttemptRecordsDurationAndFields(t *testing.T) {
	clock := testClock()
	j := New(4, clock.Now)
	turn := j.Open(testFacts(), "gpt-5.2")
	started := turn.Now()
	clock.advance(300 * time.Millisecond)
	turn.Attempt(AttemptInfo{
		Provider:  "codex",
		AccountID: "codex:a1",
		Model:     "gpt-5.2",
		StartedAt: started,
		Outcome:   AttemptSucceeded,
	})
	turn.Close(Terminal{Status: StatusCompleted})
	snap := j.Snapshot()
	if len(snap[0].Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(snap[0].Attempts))
	}
	a := snap[0].Attempts[0]
	if a.Duration != 300*time.Millisecond {
		t.Fatalf("attempt duration = %v, want 300ms", a.Duration)
	}
	if a.Provider != "codex" || a.AccountID != "codex:a1" || a.Model != "gpt-5.2" {
		t.Fatalf("attempt = %+v, want identity fields", a)
	}
	if a.Outcome != AttemptSucceeded {
		t.Fatalf("outcome = %v, want succeeded", a.Outcome)
	}
}

func TestCloseIdempotentAndAttemptAfterCloseNoOp(t *testing.T) {
	clock := testClock()
	j := New(4, clock.Now)
	turn := j.Open(testFacts(), "gpt-5.2")
	turn.Attempt(AttemptInfo{Provider: "codex", Outcome: AttemptServer})
	turn.Close(Terminal{Status: StatusFailed, Failed: true, Reason: canon.FailServerOverloaded})
	clock.advance(time.Second)
	turn.Close(Terminal{Status: StatusCompleted})
	turn.Attempt(AttemptInfo{Provider: "codex", Outcome: AttemptSucceeded})
	snap := j.Snapshot()
	if len(snap[0].Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1 (no append after close)", len(snap[0].Attempts))
	}
	if snap[0].Status != StatusFailed {
		t.Fatalf("status = %v, want failed (close is idempotent)", snap[0].Status)
	}
	if snap[0].Duration != 0 {
		t.Fatalf("duration = %v, want 0 at close time", snap[0].Duration)
	}
}

func TestAttemptsCapAt32AndCountDropped(t *testing.T) {
	clock := testClock()
	j := New(4, clock.Now)
	turn := j.Open(testFacts(), "gpt-5.2")
	for i := range 40 {
		turn.Attempt(AttemptInfo{Provider: account.ProviderID(fmt.Sprintf("p%d", i)), Outcome: AttemptServer})
	}
	turn.Close(Terminal{Status: StatusFailed})
	snap := j.Snapshot()
	if len(snap[0].Attempts) != maxAttemptsPerTurn {
		t.Fatalf("attempts = %d, want %d", len(snap[0].Attempts), maxAttemptsPerTurn)
	}
	if snap[0].AttemptsDropped != 8 {
		t.Fatalf("attemptsDropped = %d, want 8", snap[0].AttemptsDropped)
	}
	if snap[0].Attempts[0].Provider != "p0" || snap[0].Attempts[31].Provider != "p31" {
		t.Fatalf("first capped attempts = %q..%q, want p0..p31", snap[0].Attempts[0].Provider, snap[0].Attempts[31].Provider)
	}
}

func TestAttemptErrorTruncatedTo512Bytes(t *testing.T) {
	clock := testClock()
	j := New(4, clock.Now)
	turn := j.Open(testFacts(), "gpt-5.2")
	long := strings.Repeat("x", 4096)
	turn.Attempt(AttemptInfo{Provider: "codex", Error: long, StartedAt: turn.Now(), Outcome: AttemptTransport})
	turn.Close(Terminal{Status: StatusFailed})
	snap := j.Snapshot()
	got := snap[0].Attempts[0].Error
	if len(got) != maxErrorBytes {
		t.Fatalf("error len = %d, want %d", len(got), maxErrorBytes)
	}
	if got != long[:maxErrorBytes] {
		t.Fatal("truncated error is not the prefix of the original")
	}
}

func TestCapacityZeroDiscardsEverything(t *testing.T) {
	clock := testClock()
	j := New(0, clock.Now)
	if got := j.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot len = %d, want 0", len(got))
	}
	turn := j.Open(testFacts(), "gpt-5.2")
	turn.Attempt(AttemptInfo{Provider: "codex", Outcome: AttemptSucceeded})
	turn.Close(Terminal{Status: StatusCompleted})
	if got := j.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot len after turn = %d, want 0", len(got))
	}
	if got := j.Dropped(); got != 0 {
		t.Fatalf("dropped = %d, want 0 (discard is not eviction)", got)
	}
}

func TestNegativeCapacityDiscards(t *testing.T) {
	j := New(-1, testClock().Now)
	j.Open(testFacts(), "gpt-5.2").Close(Terminal{Status: StatusFailed})
	if got := j.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot len = %d, want 0", len(got))
	}
}

func TestRingEvictionKeepsNewestAndCountsDropped(t *testing.T) {
	clock := testClock()
	j := New(3, clock.Now)
	for i := range 5 {
		turn := j.Open(testFacts(), canon.ModelID(fmt.Sprintf("m%d", i)))
		turn.Close(Terminal{Status: StatusCompleted})
	}
	snap := j.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
	for i, e := range snap {
		want := canon.ModelID(fmt.Sprintf("m%d", i+2))
		if e.Model != want {
			t.Fatalf("snap[%d].model = %q, want %q", i, e.Model, want)
		}
	}
	if got := j.Dropped(); got != 2 {
		t.Fatalf("dropped = %d, want 2", got)
	}
}

func TestEvictionOfOpenEntryKeepsGroupAtomicForNewcomers(t *testing.T) {
	clock := testClock()
	j := New(2, clock.Now)
	open := j.Open(testFacts(), "m-open")
	open.Attempt(AttemptInfo{Provider: "codex", Outcome: AttemptServer, StartedAt: turnStart(t, open)})
	for i := range 3 {
		turn := j.Open(testFacts(), canon.ModelID(fmt.Sprintf("m%d", i)))
		turn.Attempt(AttemptInfo{Provider: "p", Outcome: AttemptSucceeded, StartedAt: turnStart(t, turn)})
		turn.Close(Terminal{Status: StatusCompleted})
	}
	snap := j.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	for _, e := range snap {
		if e.Model == "m-open" {
			t.Fatalf("evicted open entry must not be visible, got %+v", e)
		}
		if len(e.Attempts) != 1 {
			t.Fatalf("entry %q attempts = %d, want 1 (group intact)", e.Model, len(e.Attempts))
		}
	}
	open.Attempt(AttemptInfo{Provider: "codex", Outcome: AttemptSucceeded, StartedAt: open.Now()})
	open.Close(Terminal{Status: StatusCompleted})
	for _, e := range j.Snapshot() {
		if e.Model == "m-open" {
			t.Fatal("orphaned turn writes must stay invisible after eviction")
		}
	}
}

func turnStart(t *testing.T, turn *Turn) time.Time {
	t.Helper()
	return turn.Now()
}

func TestSnapshotIsDeepCopy(t *testing.T) {
	clock := testClock()
	j := New(4, clock.Now)
	turn := j.Open(testFacts(), "gpt-5.2")
	turn.Attempt(AttemptInfo{Provider: "codex", Outcome: AttemptSucceeded, StartedAt: turn.Now()})
	turn.Close(Terminal{Status: StatusCompleted})
	snap := j.Snapshot()
	snap[0].Attempts[0].Provider = "mutated"
	snap[0].Status = StatusFailed
	again := j.Snapshot()
	if again[0].Attempts[0].Provider != "codex" {
		t.Fatalf("journal attempt mutated through snapshot: %+v", again[0].Attempts[0])
	}
	if again[0].Status != StatusCompleted {
		t.Fatalf("journal status mutated through snapshot: %v", again[0].Status)
	}
}

func TestConcurrentTurnsUnderRace(t *testing.T) {
	clock := testClock()
	j := New(16, clock.Now)
	const writers = 8
	const turnsPerWriter = 32
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for n := range turnsPerWriter {
				turn := j.Open(execution.Facts{RequestID: execution.RequestID(fmt.Sprintf("r%d-%d", w, n)), Client: execution.ClientOMP}, "gpt-5.2")
				started := turn.Now()
				turn.Attempt(AttemptInfo{Provider: "codex", AccountID: "codex:a", Model: "gpt-5.2", StartedAt: started, Outcome: AttemptSucceeded})
				turn.Attempt(AttemptInfo{Provider: "antigravity", AccountID: "antigravity:a", Model: "gemini", StartedAt: started, Outcome: AttemptServer, Error: "boom"})
				turn.Close(Terminal{Status: StatusCompleted, Usage: canon.Usage{TotalTokens: 3}})
				if s := j.Snapshot(); len(s) > 16 {
					t.Errorf("snapshot len = %d exceeds capacity", len(s))
				}
			}
		}(w)
	}
	wg.Wait()
	snap := j.Snapshot()
	if len(snap) != 16 {
		t.Fatalf("snapshot len = %d, want 16", len(snap))
	}
	for _, e := range snap {
		if e.Status != StatusCompleted {
			t.Fatalf("entry %d status = %v, want completed", e.Seq, e.Status)
		}
		if len(e.Attempts) != 2 {
			t.Fatalf("entry %d attempts = %d, want 2", e.Seq, len(e.Attempts))
		}
		if e.Terminal.Usage.TotalTokens != 3 {
			t.Fatalf("entry %d usage = %+v, want 3 total", e.Seq, e.Terminal.Usage)
		}
	}
	if j.Dropped() != uint64(writers*turnsPerWriter-16) {
		t.Fatalf("dropped = %d, want %d", j.Dropped(), writers*turnsPerWriter-16)
	}
}

func TestSeqMonotonicAcrossEvictions(t *testing.T) {
	j := New(2, testClock().Now)
	for i := range 4 {
		turn := j.Open(testFacts(), "m")
		turn.Close(Terminal{Status: StatusCompleted})
		_ = i
	}
	snap := j.Snapshot()
	if snap[0].Seq >= snap[1].Seq {
		t.Fatalf("seq order = %d, %d, want ascending", snap[0].Seq, snap[1].Seq)
	}
	if snap[1].Seq != 4 {
		t.Fatalf("newest seq = %d, want 4", snap[1].Seq)
	}
}

func TestNilClockFallsBackToTimeNow(t *testing.T) {
	j := New(2, nil)
	turn := j.Open(testFacts(), "gpt-5.2")
	if turn.Now().IsZero() {
		t.Fatal("Turn.Now with nil journal clock returned zero time")
	}
	turn.Attempt(AttemptInfo{Provider: "codex", StartedAt: turn.Now(), Outcome: AttemptSucceeded})
	turn.Close(Terminal{Status: StatusCompleted})
	snap := j.Snapshot()
	if snap[0].Duration <= 0 {
		t.Fatalf("duration = %v, want positive under real clock", snap[0].Duration)
	}
	if snap[0].Attempts[0].Duration < 0 {
		t.Fatalf("attempt duration = %v, want non-negative", snap[0].Attempts[0].Duration)
	}
}

func TestOutcomeStringRoundTrip(t *testing.T) {
	names := map[Outcome]string{
		AttemptSucceeded:      "succeeded",
		AttemptUnauthorized:   "unauthorized",
		AttemptForbidden:      "forbidden",
		AttemptRateLimited:    "rate_limited",
		AttemptQuotaExhausted: "quota_exhausted",
		AttemptNotFound:       "not_found",
		AttemptTimeout:        "timeout",
		AttemptServer:         "server",
		AttemptTransport:      "transport",
		AttemptInvalidRequest: "invalid_request",
		AttemptContextLength:  "context_length",
		AttemptClientClosed:   "client_closed",
		AttemptNoTerminal:     "no_terminal",
		AttemptRejected:       "rejected",
	}
	seen := make(map[string]bool, len(names))
	for o, want := range names {
		if got := o.String(); got != want {
			t.Fatalf("Outcome(%d).String() = %q, want %q", o, got, want)
		}
		seen[want] = true
	}
	if len(seen) != len(names) {
		t.Fatal("outcome names are not all distinct")
	}
	if got := Outcome(0).String(); got != "unknown" {
		t.Fatalf("zero Outcome.String() = %q, want unknown", got)
	}
}
