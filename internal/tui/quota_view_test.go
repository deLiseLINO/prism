package tui

import (
	"strings"
	"testing"
	"time"

	"prism/internal/management"
)

func TestQuotaWindowsFromViewFallback(t *testing.T) {
	limit := int64(200)
	windows := quotaWindowsFromView(management.QuotaView{Used: 50, Limit: &limit, WindowEnd: time.Now().Add(2 * time.Hour)})
	if len(windows) != 1 {
		t.Fatalf("windows = %d, want 1 fallback", len(windows))
	}
	window := windows[0]
	if !window.HasPercent {
		t.Fatal("expected percent available with positive limit")
	}
	if window.LeftPercent != 75 {
		t.Fatalf("leftPercent = %v, want 75", window.LeftPercent)
	}
}

func TestQuotaWindowsFromViewSortsShortFirst(t *testing.T) {
	limit := int64(100)
	view := management.QuotaView{
		Windows: []management.QuotaWindowView{
			{Label: "Weekly usage limit", Used: 10, Limit: &limit},
			{Label: "5 hour usage limit", Used: 10, Limit: &limit},
		},
	}
	windows := quotaWindowsFromView(view)
	if len(windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(windows))
	}
	if windows[0].WindowSec != windowSecShort {
		t.Fatalf("first windowSec = %d, want %d", windows[0].WindowSec, windowSecShort)
	}
	if windows[1].WindowSec != windowSecWeekly {
		t.Fatalf("second windowSec = %d, want %d", windows[1].WindowSec, windowSecWeekly)
	}
	if windows[0].Label == "Current usage limit" || windows[1].Label == "Current usage limit" {
		t.Fatal("windows present but fallback label used")
	}
}

func TestQuotaWindowsFromViewUnknownLimit(t *testing.T) {
	windows := quotaWindowsFromView(management.QuotaView{Used: 50})
	window := windows[0]
	if window.HasPercent {
		t.Fatal("expected unknown percent with nil limit")
	}
}

func TestQuotaWindowsFromViewZeroLimit(t *testing.T) {
	zero := int64(0)
	windows := quotaWindowsFromView(management.QuotaView{Used: 50, Limit: &zero})
	window := windows[0]
	if window.HasPercent {
		t.Fatal("expected unknown percent with zero limit")
	}
}

func TestQuotaWindowClampsPercent(t *testing.T) {
	limit := int64(10)
	over := quotaWindowsFromView(management.QuotaView{Used: 40, Limit: &limit})[0]
	if over.LeftPercent != 0 {
		t.Fatalf("leftPercent = %v, want 0", over.LeftPercent)
	}
	under := quotaWindowsFromView(management.QuotaView{Used: -5, Limit: &limit})[0]
	if under.LeftPercent != 100 {
		t.Fatalf("leftPercent = %v, want 100", under.LeftPercent)
	}
}

func TestRenderSmoothBarFull(t *testing.T) {
	bar := renderSmoothBar(10, 1, "#6C63FF", "#D46DFF")
	width := lipglossWidth(bar)
	if width != 10 {
		t.Fatalf("width = %d, want 10", width)
	}
}

func TestRenderSmoothBarEmpty(t *testing.T) {
	bar := renderSmoothBar(10, 0, "#6C63FF", "#D46DFF")
	if lipglossWidth(bar) != 10 {
		t.Fatalf("width = %d, want 10", lipglossWidth(bar))
	}
}

func TestRenderSmoothBarPartial(t *testing.T) {
	bar := renderSmoothBar(8, 0.5, "#6C63FF", "#D46DFF")
	if lipglossWidth(bar) != 8 {
		t.Fatalf("width = %d, want 8", lipglossWidth(bar))
	}
}

func TestFormatResetText(t *testing.T) {
	if got := formatResetText(time.Time{}); got != "Resets unknown" {
		t.Fatalf("zero time = %q", got)
	}
	past := time.Now().Add(-time.Minute)
	if got := formatResetText(past); got != "Resets now" {
		t.Fatalf("past time = %q", got)
	}
	future := time.Now().Add(24*time.Hour + 4*time.Hour + 11*time.Minute + 20*time.Second)
	got := formatResetText(future)
	if !strings.HasPrefix(got, "Resets "+future.Local().Format("Mon 15:04")+" (1d 4h") {
		t.Fatalf("future = %q", got)
	}
}

func TestFormatRemainingShort(t *testing.T) {
	cases := []struct {
		remaining time.Duration
		want      string
	}{
		{30 * time.Second, "<1m"},
		{12 * time.Minute, "12m"},
		{5 * time.Hour, "5h"},
		{5*time.Hour + 12*time.Minute, "5h 12m"},
		{26 * time.Hour, "1d 2h"},
		{48 * time.Hour, "2d"},
	}
	for _, tc := range cases {
		if got := formatRemainingShort(tc.remaining); got != tc.want {
			t.Fatalf("formatRemainingShort(%v) = %q, want %q", tc.remaining, got, tc.want)
		}
	}
}

func TestWindowSecForLabel(t *testing.T) {
	cases := []struct {
		label string
		want  int64
	}{
		{"5 hour usage limit", 18000},
		{"Weekly usage limit", 604800},
		{"Custom plan window", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := windowSecForLabel(tc.label); got != tc.want {
			t.Fatalf("windowSecForLabel(%q) = %d, want %d", tc.label, got, tc.want)
		}
	}
}

func TestQuotaWindowFromDetailViewSetsWindowSec(t *testing.T) {
	limit := int64(100)
	short := quotaWindowFromDetailView(management.QuotaWindowView{Label: "5 hour usage limit", Used: 10, Limit: &limit})
	if short.WindowSec != 18000 {
		t.Fatalf("short windowSec = %d, want 18000", short.WindowSec)
	}
	weekly := quotaWindowFromDetailView(management.QuotaWindowView{Label: "Weekly usage limit", Used: 10, Limit: &limit})
	if weekly.WindowSec != 604800 {
		t.Fatalf("weekly windowSec = %d, want 604800", weekly.WindowSec)
	}
}

func TestWindowHeaderShortLabels(t *testing.T) {
	if got := windowRowLabel(quotaWindow{Label: "5 hour usage limit", WindowSec: 18000}); got != "5 hour" {
		t.Fatalf("short header = %q, want 5 hour", got)
	}
	if got := windowRowLabel(quotaWindow{Label: "Weekly usage limit", WindowSec: 604800}); got != "Weekly" {
		t.Fatalf("weekly header = %q, want Weekly", got)
	}
	if got := windowRowLabel(quotaWindow{Label: "Custom window"}); got != "Custom window" {
		t.Fatalf("custom header = %q, want label passthrough", got)
	}
}

func TestQuotaWindowsFromAntigravityLabels(t *testing.T) {
	limit := int64(10000)
	view := management.QuotaView{
		Windows: []management.QuotaWindowView{
			{Label: "Gemini Weekly", Used: 2000, Limit: &limit},
			{Label: "Gemini 5 hour", Used: 7500, Limit: &limit},
			{Label: "Claude 5 hour", Used: 6000, Limit: &limit},
		},
	}
	windows := quotaWindowsFromView(view)
	if len(windows) != 3 {
		t.Fatalf("windows = %d, want 3", len(windows))
	}
	if windows[0].Label != "Gemini 5 hour" || windows[0].WindowSec != windowSecShort {
		t.Fatalf("first = %+v, want Gemini 5 hour short window", windows[0])
	}
	if windows[1].Label != "Claude 5 hour" || windows[1].WindowSec != windowSecShort {
		t.Fatalf("second = %+v, want Claude 5 hour short window", windows[1])
	}
	if windows[2].Label != "Gemini Weekly" || windows[2].WindowSec != windowSecWeekly {
		t.Fatalf("third = %+v, want Gemini Weekly weekly window", windows[2])
	}
}
