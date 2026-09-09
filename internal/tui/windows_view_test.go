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

	shortAt := strings.Index(view, "5 hour")
	weeklyAt := strings.Index(view, "Weekly")
	if shortAt == -1 || weeklyAt == -1 {
		t.Fatalf("view = %q", view)
	}
	if shortAt > weeklyAt {
		t.Fatal("5 hour window must render before Weekly")
	}
}

func TestRenderWindowsViewSingleGroupNoBlankLineInside(t *testing.T) {
	view := testWindowsModel().renderWindowsView()
	if strings.Contains(view, "\n\n") {
		t.Fatalf("single group must not contain a blank line\nview = %q", view)
	}
	if !strings.Contains(view, "5 hour") || !strings.Contains(view, "Weekly") {
		t.Fatalf("view = %q", view)
	}
}

func TestRenderWindowsViewGroupsAntigravityFamilies(t *testing.T) {
	limit := int64(10000)
	m := testModel(nil, management.Account{ID: "antigravity:default", Provider: "antigravity", State: "active"})
	m.Width = 160
	m.Height = 40
	m.UsageData["antigravity:default"] = quotaWindowsFromView(management.QuotaView{
		Windows: []management.QuotaWindowView{
			{Label: "Gemini Weekly", Used: 100, Limit: &limit, WindowEnd: time.Now().Add(7 * 24 * time.Hour)},
			{Label: "Claude 5 hour", Used: 4000, Limit: &limit, WindowEnd: time.Now().Add(5 * time.Hour)},
			{Label: "Gemini 5 hour", Used: 7500, Limit: &limit, WindowEnd: time.Now().Add(5 * time.Hour)},
			{Label: "Claude Weekly", Used: 0, Limit: &limit, WindowEnd: time.Now().Add(7 * 24 * time.Hour)},
		},
	})
	view := m.renderWindowsView()

	geminiAt := strings.Index(view, "Gemini Models")
	claudeAt := strings.Index(view, "Claude and GPT models")
	if geminiAt == -1 || claudeAt == -1 {
		t.Fatalf("view missing group headers: %q", view)
	}
	if geminiAt > claudeAt {
		t.Fatal("Gemini group must render before Claude group")
	}
	geminiBlockEnd := strings.Index(view[geminiAt:], "Claude and GPT models")
	if geminiBlockEnd == -1 {
		t.Fatalf("view = %q", view)
	}
	geminiBlock := view[geminiAt : geminiAt+geminiBlockEnd]
	if !strings.Contains(geminiBlock, "5 hour") || !strings.Contains(geminiBlock, "Weekly") {
		t.Fatalf("Gemini block missing window rows: %q", geminiBlock)
	}
	if strings.Count(geminiBlock, "\n") != 4 {
		t.Fatalf("Gemini block rows = %d, want header + 2 rows + group separator\n%q", strings.Count(geminiBlock, "\n"), geminiBlock)
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
