package tui

import (
	"strings"
	"testing"

	"prism/internal/management"
)

func TestTabVisibleRange(t *testing.T) {
	cases := []struct {
		total, active, max int
		start, end         int
	}{
		{3, 0, 3, 0, 3},
		{5, 0, 3, 0, 3},
		{5, 2, 3, 1, 4},
		{5, 4, 3, 2, 5},
	}
	for _, tc := range cases {
		start, end := tabVisibleRange(tc.total, tc.active, tc.max)
		if start != tc.start || end != tc.end {
			t.Fatalf("tabVisibleRange(%d, %d, %d) = %d, %d; want %d, %d", tc.total, tc.active, tc.max, start, end, tc.start, tc.end)
		}
	}
}

func TestTruncateLabel(t *testing.T) {
	if got := truncateLabel("abcdef", 4); got != "abc…" {
		t.Fatalf("truncateLabel = %q", got)
	}
	if got := truncateLabel("ab", 4); got != "ab" {
		t.Fatalf("truncateLabel = %q", got)
	}
	if got := truncateLabelFromLeft("abcdef", 4); got != "…def" {
		t.Fatalf("truncateLabelFromLeft = %q", got)
	}
}

func TestRenderWindowsLoadingSkeletonShowsBothWindows(t *testing.T) {
	m := testModel(nil, management.Account{ID: "codex:default", Provider: "codex"})
	skeleton := m.renderWindowsLoadingSkeleton()

	if !strings.Contains(skeleton, "5 hour") {
		t.Fatal("skeleton missing 5 hour window header")
	}
	if !strings.Contains(skeleton, "Weekly") {
		t.Fatal("skeleton missing Weekly window header")
	}
	if strings.Count(skeleton, "Loading...") != 4 {
		t.Fatalf("loading rows = %d, want 4", strings.Count(skeleton, "Loading..."))
	}
	if !strings.Contains(skeleton, "Gemini Models") || !strings.Contains(skeleton, "Claude and GPT models") {
		t.Fatal("skeleton missing group headers")
	}
}

func TestOnlyPinnedAccountHasBadge(t *testing.T) {
	m := testModel(nil, management.Account{ID: "codex:main", Provider: "codex"})

	if got := m.renderAccountBadge(m.Accounts[0]); got != "" {
		t.Fatalf("unpinned badge = %q, want empty", got)
	}

	m.PinnedAccounts = map[string]string{"codex": "codex:main"}
	if got := m.renderAccountBadge(m.Accounts[0]); !strings.Contains(got, "P") {
		t.Fatalf("pinned badge = %q, want P", got)
	}
}
