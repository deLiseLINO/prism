package codex

import (
	"net/http"
	"testing"
	"time"
)

func TestQuotaHeadersPrimaryWeekly(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "42.5")
	h.Set("x-codex-primary-reset-at", "1800000000")
	h.Set("x-codex-secondary-used-percent", "10")
	h.Set("x-codex-secondary-reset-at", "1790000000")
	res := ParseQuotaHeaders(h)
	if !res.OK {
		t.Fatalf("expected snapshot, warnings=%v", res.Warnings)
	}
	if res.Snapshot.Used != 4250 {
		t.Fatalf("used = %d want 4250", res.Snapshot.Used)
	}
	if res.Snapshot.Limit == nil || *res.Snapshot.Limit != 10000 {
		t.Fatalf("limit = %v", res.Snapshot.Limit)
	}
	if res.Snapshot.Source != 1 {
		t.Fatalf("source = %d want SourceHeader(1)", res.Snapshot.Source)
	}
	if res.Snapshot.WindowEnd.IsZero() {
		t.Fatalf("window end missing")
	}
	if want := time.Unix(1800000000, 0).UTC(); !res.Snapshot.WindowEnd.Equal(want) {
		t.Fatalf("window end = %v want %v", res.Snapshot.WindowEnd, want)
	}
}

func TestQuotaHeadersExplicitMonthlyPrimary(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "20")
	h.Set("x-codex-primary-window-minutes", "43200")
	h.Set("x-codex-secondary-used-percent", "70")
	res := ParseQuotaHeaders(h)
	if !res.OK {
		t.Fatalf("expected snapshot, warnings=%v", res.Warnings)
	}
	if res.Snapshot.Used != 7000 {
		t.Fatalf("used = %d want 7000 (secondary weekly governs)", res.Snapshot.Used)
	}
}

func TestQuotaHeadersShortBurstWindow(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "95")
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("x-codex-secondary-used-percent", "30")
	res := ParseQuotaHeaders(h)
	if !res.OK {
		t.Fatalf("expected snapshot, warnings=%v", res.Warnings)
	}
	if res.Snapshot.Used != 9500 {
		t.Fatalf("used = %d want 9500 (burst window governs)", res.Snapshot.Used)
	}
}

func TestQuotaHeadersTertiaryMonthlyFallback(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-tertiary-used-percent", "80")
	h.Set("x-codex-tertiary-reset-at", "1799999999")
	res := ParseQuotaHeaders(h)
	if !res.OK {
		t.Fatalf("expected snapshot, warnings=%v", res.Warnings)
	}
	if res.Snapshot.Used != 8000 {
		t.Fatalf("used = %d want 8000", res.Snapshot.Used)
	}
}

func TestQuotaHeadersMissingTolerated(t *testing.T) {
	res := ParseQuotaHeaders(http.Header{})
	if res.OK {
		t.Fatalf("expected no snapshot for missing headers")
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("missing headers must be tolerated without warnings, got %v", res.Warnings)
	}
}

func TestQuotaHeadersUnparseableTypedWarnings(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "abc")
	h.Set("x-codex-primary-reset-at", "-5")
	h.Set("x-codex-secondary-window-minutes", "oops")
	res := ParseQuotaHeaders(h)
	if res.OK {
		t.Fatalf("expected no snapshot")
	}
	want := []string{
		"quota_used_percent_unparseable:primary",
		"quota_reset_at_unparseable:primary",
		"quota_window_minutes_unparseable:secondary",
	}
	if len(res.Warnings) != len(want) {
		t.Fatalf("warnings = %v want %v", res.Warnings, want)
	}
	for i := range want {
		if res.Warnings[i] != want[i] {
			t.Fatalf("warning[%d] = %q want %q", i, res.Warnings[i], want[i])
		}
	}
}

func TestQuotaResetAtMilliseconds(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "1")
	h.Set("x-codex-primary-reset-at", "1800000000000")
	res := ParseQuotaHeaders(h)
	if !res.OK {
		t.Fatalf("expected snapshot")
	}
	if want := time.Unix(1800000000, 0).UTC(); !res.Snapshot.WindowEnd.Equal(want) {
		t.Fatalf("window end = %v want %v", res.Snapshot.WindowEnd, want)
	}
}

func TestQuotaPercentClamped(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "150")
	res := ParseQuotaHeaders(h)
	if !res.OK || res.Snapshot.Used != 10000 {
		t.Fatalf("used = %d, want clamped 10000", res.Snapshot.Used)
	}
}
