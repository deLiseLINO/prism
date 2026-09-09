package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

var (
	base = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
)

func open(t *testing.T, path string) Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func dbPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "usage.db")
}

func mustInsert(t *testing.T, s Store, rec Record) {
	t.Helper()
	if err := s.Insert(context.Background(), rec); err != nil {
		t.Fatalf("insert %s: %v", rec.RequestID, err)
	}
}

func sampleRecords() []Record {
	return []Record{
		{
			RequestID: "req-1",
			Timestamp: base,
			Model:     "gpt-5",
			Protocol:  "responses",
			Status:    "completed",
			Reason:    "",
			Usage:     Usage{InputTokens: 1000, OutputTokens: 200, CachedInputTokens: 500, ReasoningTokens: 100, TotalTokens: 1200},
			UsageKind: StatusReported,
			Attempts: []Attempt{
				{Provider: "codex", Model: "gpt-5", Outcome: "failed", Usage: Usage{InputTokens: 10, OutputTokens: 0, TotalTokens: 10}},
				{Provider: "openai", Model: "gpt-5", Outcome: "completed", Usage: Usage{InputTokens: 1000, OutputTokens: 200, CachedInputTokens: 500, ReasoningTokens: 100, TotalTokens: 1200}},
			},
			Duration: 1500 * time.Millisecond,
		},
		{
			RequestID: "req-2",
			Timestamp: base.Add(time.Minute),
			Model:     "gpt-5",
			Protocol:  "responses",
			Status:    "failed",
			Reason:    "rate_limited",
			Usage:     Usage{},
			UsageKind: StatusZero,
			Attempts: []Attempt{
				{Provider: "codex", Model: "gpt-5", Outcome: "failed", Usage: Usage{InputTokens: 20, OutputTokens: 0, TotalTokens: 20}},
			},
			Duration: 300 * time.Millisecond,
		},
		{
			RequestID: "req-3",
			Timestamp: base.Add(2 * time.Minute),
			Model:     "claude-4",
			Protocol:  "messages",
			Status:    "incomplete",
			Reason:    "max_output_tokens",
			Usage:     Usage{InputTokens: 300, OutputTokens: 50, TotalTokens: 350},
			UsageKind: StatusReported,
			Attempts: []Attempt{
				{Provider: "anthropic", Model: "claude-4", Outcome: "incomplete", Usage: Usage{InputTokens: 300, OutputTokens: 50, TotalTokens: 350}},
			},
			Duration: 800 * time.Millisecond,
		},
	}
}

func TestOverviewSumsRequestsAndTokens(t *testing.T) {
	s := open(t, dbPath(t))
	ctx := context.Background()
	for _, rec := range sampleRecords() {
		mustInsert(t, s, rec)
	}
	got, err := s.Overview(ctx, time.Time{})
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	want := Aggregate{
		Requests:        3,
		Completed:       1,
		Failed:          1,
		InputTokens:     1300,
		OutputTokens:    250,
		CachedTokens:    500,
		ReasoningTokens: 100,
		TotalTokens:     1550,
		Measured:        2,
	}
	if got != want {
		t.Fatalf("overview: got %+v want %+v", got, want)
	}
}

func TestOverviewSinceFiltersOldRows(t *testing.T) {
	s := open(t, dbPath(t))
	ctx := context.Background()
	for _, rec := range sampleRecords() {
		mustInsert(t, s, rec)
	}
	got, err := s.Overview(ctx, base.Add(30*time.Second))
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	want := Aggregate{
		Requests:     2,
		Failed:       1,
		InputTokens:  300,
		OutputTokens: 50,
		TotalTokens:  350,
		Measured:     1,
	}
	if got != want {
		t.Fatalf("overview since: got %+v want %+v", got, want)
	}
}

func TestInsertIdempotentOnRequestID(t *testing.T) {
	path := dbPath(t)
	s := open(t, path)
	ctx := context.Background()
	recs := sampleRecords()
	mustInsert(t, s, recs[0])
	mustInsert(t, s, recs[0]) // duplicate request_id: ignored entirely

	var requests, attempts int
	if err := s.(*dbStore).db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM requests").Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if err := s.(*dbStore).db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM attempts").Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if requests != 1 || attempts != 2 {
		t.Fatalf("counts after duplicate insert: requests=%d attempts=%d want 1/2", requests, attempts)
	}

	// A different request_id with the same attempts content still writes its own rows.
	other := recs[0]
	other.RequestID = "req-other"
	mustInsert(t, s, other)
	if err := s.(*dbStore).db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM requests").Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests after distinct insert: %d want 2", requests)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	path := dbPath(t)
	s := open(t, path)
	ctx := context.Background()
	for _, rec := range sampleRecords() {
		mustInsert(t, s, rec)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	ov, err := s2.Overview(ctx, time.Time{})
	if err != nil {
		t.Fatalf("overview after reopen: %v", err)
	}
	if ov.Requests != 3 || ov.TotalTokens != 1550 {
		t.Fatalf("overview after reopen: got %+v", ov)
	}
	byModel, err := s2.ByModel(ctx, time.Time{})
	if err != nil {
		t.Fatalf("by model after reopen: %v", err)
	}
	if len(byModel) != 2 {
		t.Fatalf("by model after reopen: got %d rows want 2", len(byModel))
	}
}

func TestByModelGroupsAndUsesTerminalAttemptProvider(t *testing.T) {
	s := open(t, dbPath(t))
	ctx := context.Background()
	for _, rec := range sampleRecords() {
		mustInsert(t, s, rec)
	}
	got, err := s.ByModel(ctx, time.Time{})
	if err != nil {
		t.Fatalf("by model: %v", err)
	}
	// req-2 (the most recent gpt-5 request) terminated on the codex attempt,
	// so codex is the terminal provider for the model.
	want := []ModelAggregate{
		{
			Model:    "gpt-5",
			Provider: "codex",
			Aggregate: Aggregate{
				Requests:        2,
				Completed:       1,
				Failed:          1,
				InputTokens:     1000,
				OutputTokens:    200,
				CachedTokens:    500,
				ReasoningTokens: 100,
				TotalTokens:     1550 - 350,
				Measured:        1,
			},
		},
		{
			Model:    "claude-4",
			Provider: "anthropic",
			Aggregate: Aggregate{
				Requests:     1,
				InputTokens:  300,
				OutputTokens: 50,
				TotalTokens:  350,
				Measured:     1,
			},
		},
	}
	if len(got) != len(want) {
		t.Fatalf("by model: got %d rows want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("by model[%d]: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestByProviderAttributesRequestsToTerminalAttempt(t *testing.T) {
	s := open(t, dbPath(t))
	ctx := context.Background()
	for _, rec := range sampleRecords() {
		mustInsert(t, s, rec)
	}
	got, err := s.ByProvider(ctx, time.Time{})
	if err != nil {
		t.Fatalf("by provider: %v", err)
	}
	want := []ProviderAggregate{
		{
			Provider: "openai",
			Aggregate: Aggregate{
				Requests:        1,
				Completed:       1,
				InputTokens:     1000,
				OutputTokens:    200,
				CachedTokens:    500,
				ReasoningTokens: 100,
				TotalTokens:     1200,
				Measured:        1,
			},
		},
		{
			Provider: "anthropic",
			Aggregate: Aggregate{
				Requests:     1,
				InputTokens:  300,
				OutputTokens: 50,
				TotalTokens:  350,
				Measured:     1,
			},
		},
		{
			Provider: "codex",
			Aggregate: Aggregate{
				Requests: 1,
				Failed:   1,
				Measured: 0,
			},
		},
	}
	if len(got) != len(want) {
		t.Fatalf("by provider: got %d rows want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("by provider[%d]: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestZeroUsageExcludedFromMeasured(t *testing.T) {
	s := open(t, dbPath(t))
	ctx := context.Background()
	mustInsert(t, s, Record{
		RequestID: "req-zero",
		Timestamp: base,
		Model:     "gpt-5",
		Protocol:  "responses",
		Status:    "completed",
		Usage:     Usage{},
		UsageKind: StatusZero,
		Attempts: []Attempt{
			{Provider: "openai", Model: "gpt-5", Outcome: "completed", Usage: Usage{}},
		},
	})

	var kind string
	if err := s.(*dbStore).db.QueryRowContext(ctx,
		"SELECT usage_kind FROM requests WHERE request_id = 'req-zero'").Scan(&kind); err != nil {
		t.Fatalf("read usage_kind: %v", err)
	}
	if kind != "zero" {
		t.Fatalf("usage_kind: got %q want zero", kind)
	}

	ov, err := s.Overview(ctx, time.Time{})
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if ov.Requests != 1 || ov.Measured != 0 {
		t.Fatalf("overview: requests=%d measured=%d want 1/0", ov.Requests, ov.Measured)
	}
	byProvider, err := s.ByProvider(ctx, time.Time{})
	if err != nil {
		t.Fatalf("by provider: %v", err)
	}
	if len(byProvider) != 1 || byProvider[0].Measured != 0 || byProvider[0].Requests != 1 {
		t.Fatalf("by provider: got %+v", byProvider)
	}
}

func TestOpenMissingDirectoryFailsCleanly(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "no-such-dir", "usage.db"))
	if err == nil {
		t.Fatal("open in missing directory: expected error")
	}
}
