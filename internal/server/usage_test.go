package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/canon"
	"prism/internal/provider"
	"prism/internal/routing"
	"prism/internal/usage"
)

type fakeUsageStore struct {
	mu      sync.Mutex
	records []usage.Record
}

func (f *fakeUsageStore) Insert(_ context.Context, rec usage.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, rec)
	return nil
}

func (f *fakeUsageStore) Close() error { return nil }

func (f *fakeUsageStore) Overview(context.Context, time.Time) (usage.Aggregate, error) {
	return usage.Aggregate{}, nil
}

func (f *fakeUsageStore) ByModel(context.Context, time.Time) ([]usage.ModelAggregate, error) {
	return nil, nil
}

func (f *fakeUsageStore) ByProvider(context.Context, time.Time) ([]usage.ProviderAggregate, error) {
	return nil, nil
}

func (f *fakeUsageStore) snapshot() []usage.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]usage.Record(nil), f.records...)
}

// clockAdvancingRunner emits the given events, advancing the fake clock by d
// before the terminal so the recorded duration is deterministic.
type clockAdvancingRunner struct {
	clock *fakeClock
	d     time.Duration
	usage canon.Usage
}

func (r *clockAdvancingRunner) Run(_ context.Context, _ provider.RunRequest, sink provider.Sink) error {
	if err := sink.Emit(canon.TurnFinished{Status: canon.Completed(), Usage: r.usage}); err != nil {
		return err
	}
	r.clock.Advance(r.d)
	return nil
}

func TestUsageRecordedPerTurn(t *testing.T) {
	clock := newFakeClock()
	store := &fakeUsageStore{}
	h := newTestServerWithUsage(t, clock, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", &clockAdvancingRunner{clock: clock, d: 1500 * time.Millisecond, usage: canon.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}); err != nil {
			t.Fatal(err)
		}
	}, store)
	rec := postJSON(t, h, "/v1/responses", `{"model":"test-model","stream":true,"input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	records := store.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	got := records[0]
	if got.RequestID == "" {
		t.Fatalf("request id empty")
	}
	if !strings.Contains(rec.Body.String(), got.RequestID) {
		t.Fatalf("recorded request id %q not the response id in body:\n%s", got.RequestID, rec.Body)
	}
	if got.Model != "test-model" {
		t.Fatalf("model = %q, want test-model", got.Model)
	}
	if got.Protocol != "responses" {
		t.Fatalf("protocol = %q, want responses", got.Protocol)
	}
	if got.Status != "completed" {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if got.Usage != (usage.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}) {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if got.UsageKind != usage.StatusReported {
		t.Fatalf("usage kind = %s, want reported", got.UsageKind)
	}
	if len(got.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(got.Attempts))
	}
	wantAttempt := usage.Attempt{Provider: "p1", Model: "m1", Outcome: "completed", Usage: usage.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}
	if got.Attempts[0] != wantAttempt {
		t.Fatalf("attempt = %+v, want %+v", got.Attempts[0], wantAttempt)
	}
	if got.Duration != 1500*time.Millisecond {
		t.Fatalf("duration = %v, want 1.5s", got.Duration)
	}
	if !got.Timestamp.Equal(clock.Now().Add(-got.Duration)) {
		t.Fatalf("timestamp = %v, want turn start", got.Timestamp)
	}
}

func TestUsageRecordsZeroTerminal(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		err: provider.RunError{Kind: provider.UnsafeReplay, Class: provider.ClassTransport},
	}}}
	store := &fakeUsageStore{}
	h := newTestServerWithUsage(t, nil, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	}, store)
	rec := postJSON(t, h, "/v1/responses", `{"model":"test-model","stream":true,"input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	records := store.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	got := records[0]
	if got.RequestID == "" {
		t.Fatalf("request id empty")
	}
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Reason != "upstream_transport" {
		t.Fatalf("reason = %q, want upstream_transport", got.Reason)
	}
	if got.UsageKind != usage.StatusZero {
		t.Fatalf("usage kind = %s, want zero", got.UsageKind)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].Outcome != "failed" {
		t.Fatalf("attempts = %+v, want one failed attempt", got.Attempts)
	}
	if got.Duration < 0 {
		t.Fatalf("duration = %v, want >= 0", got.Duration)
	}
}

func TestUsageDisabledWhenStoreNil(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{TotalTokens: 5}}},
	}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"prov/m1": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/chat/completions", `{"model":"prov/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("stream not completed:\n%s", rec.Body)
	}
}

func TestUsageRecordProtocolAndIncomplete(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "partial")},
			canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteMaxOutputTokens), Usage: canon.Usage{OutputTokens: 1}},
		},
	}}}
	store := &fakeUsageStore{}
	h := newTestServerWithUsage(t, nil, map[canon.ModelID]routing.Plan{"claude-p1--m1": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	}, store)
	rec := postJSON(t, h, "/v1/messages", `{"model":"claude-p1--m1","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	records := store.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	got := records[0]
	if got.Protocol != "messages" {
		t.Fatalf("protocol = %q, want messages", got.Protocol)
	}
	if got.Status != "incomplete" {
		t.Fatalf("status = %q, want incomplete", got.Status)
	}
	if got.Reason != "max_output_tokens" {
		t.Fatalf("reason = %q, want max_output_tokens", got.Reason)
	}
	if got.UsageKind != usage.StatusReported {
		t.Fatalf("usage kind = %s, want reported", got.UsageKind)
	}
}
