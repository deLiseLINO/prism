package tui

import (
	"strings"
	"testing"
	"time"

	"prism/internal/management"
)

func testWindowsModel() Model {
	limit := int64(100)
	m := testModel(nil, management.Account{ID: "codex:default", Provider: "codex", State: "active"})
	m.Width = 160
	m.Height = 40
	m.Loading = false
	m.UsageData["codex:default"] = quotaWindowsFromView(management.QuotaView{
		Windows: []management.QuotaWindowView{
			{Label: "Weekly usage limit", Used: 0, Limit: &limit, WindowEnd: time.Now().Add(7 * 24 * time.Hour)},
			{Label: "5 hour usage limit", Used: 0, Limit: &limit, WindowEnd: time.Now().Add(5 * time.Hour)},
		},
	})
	return m
}

func TestRenderWindowsViewShowsBothWindows(t *testing.T) {
	view := testWindowsModel().renderWindowsView()

	if !strings.Contains(view, "5 hour") {
		t.Fatal("missing 5 hour header")
	}
	if !strings.Contains(view, "Weekly") {
		t.Fatal("missing Weekly header")
	}
	if strings.Contains(view, "Current usage limit") {
		t.Fatal("rendered fallback label while windows present")
	}
}

func TestRenderWindowsViewShortWindowFirst(t *testing.T) {
	view := testWindowsModel().renderWindowsView()

	shortAt := strings.Index(view, "5 hour usage limit")
	weeklyAt := strings.Index(view, "Weekly usage limit")
	if shortAt == -1 || weeklyAt == -1 {
		t.Fatalf("view = %q", view)
	}
	if shortAt > weeklyAt {
		t.Fatal("5 hour window must render before Weekly")
	}
}

func TestRenderWindowsViewBlankLineBetweenBlocks(t *testing.T) {
	view := testWindowsModel().renderWindowsView()
	blocks := strings.Split(strings.TrimSuffix(view, "\n"), "\n\n")
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 separated by blank line\nview = %q", len(blocks), view)
	}
}

func TestFetchAccountCmdReturnsSortedWindows(t *testing.T) {
	limit := int64(100)
	client := newFakeClient(management.Account{ID: "codex:default", Provider: "codex", State: "active"})
	client.quota["codex:default"] = management.QuotaView{
		Windows: []management.QuotaWindowView{
			{Label: "Weekly usage limit", Used: 0, Limit: &limit},
			{Label: "5 hour usage limit", Used: 0, Limit: &limit},
		},
	}
	m := testModel(client, management.Account{ID: "codex:default", Provider: "codex", State: "active"})

	cmd := m.fetchAccountCmd("codex:default")
	if cmd == nil {
		t.Fatal("fetch cmd is nil")
	}
	msg, ok := cmd().(DataMsg)
	if !ok {
		t.Fatalf("msg = %T", cmd())
	}
	if msg.AccountKey != "codex:default" {
		t.Fatalf("accountKey = %q", msg.AccountKey)
	}
	if len(msg.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(msg.Windows))
	}
	if msg.Windows[0].WindowSec != windowSecShort || msg.Windows[1].WindowSec != windowSecWeekly {
		t.Fatalf("order = [%d, %d], want [%d, %d]",
			msg.Windows[0].WindowSec, msg.Windows[1].WindowSec, windowSecShort, windowSecWeekly)
	}
}
