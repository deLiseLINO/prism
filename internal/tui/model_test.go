package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"prism/internal/management"
)

func testModel(client Client, accounts ...management.Account) Model {
	m := InitialModel(client, false)
	m.Accounts = accounts
	m.ActiveAccountIx = 0
	m.UsageData = make(map[string][]quotaWindow)
	m.LoadingMap = make(map[string]bool)
	m.ErrorsMap = make(map[string]error)
	m.compactBarAnimations = make(map[string]compactBarAnimation)
	m.tabWindowAnimations = make(map[string]tabWindowAnimation)
	return m
}

func TestUpdateStoresQuotaWindow(t *testing.T) {
	limit := int64(100)
	m := testModel(newFakeClient(management.Account{ID: "acc-1"}), management.Account{ID: "acc-1"})

	next, _ := m.Update(DataMsg{
		AccountKey: "acc-1",
		Windows:    quotaWindowsFromView(management.QuotaView{Used: 25, Limit: &limit}),
	})
	updated := next.(Model)

	windows, ok := updated.UsageData["acc-1"]
	if !ok {
		t.Fatal("quota window missing")
	}
	window := windows[0]
	if window.LeftPercent != 75 {
		t.Fatalf("leftPercent = %v, want 75", window.LeftPercent)
	}
	if updated.LoadingMap["acc-1"] {
		t.Fatal("account still marked loading")
	}
}

func TestUpdateErrMsgActiveAccount(t *testing.T) {
	m := testModel(newFakeClient(management.Account{ID: "acc-1"}), management.Account{ID: "acc-1"})

	next, _ := m.Update(ErrMsg{AccountKey: "acc-1", Err: errors.New("boom")})
	updated := next.(Model)

	if updated.Err == nil {
		t.Fatal("expected active error")
	}
	if updated.ErrorsMap["acc-1"] == nil {
		t.Fatal("expected per-account error")
	}
}

func TestUpdateErrMsgBackgroundAccount(t *testing.T) {
	m := testModel(nil,
		management.Account{ID: "acc-1"},
		management.Account{ID: "acc-2"},
	)
	m.ActiveAccountIx = 0

	next, _ := m.Update(ErrMsg{AccountKey: "acc-2", Err: errors.New("bg failure")})
	updated := next.(Model)

	if updated.Err != nil {
		t.Fatalf("background error leaked to global state: %v", updated.Err)
	}
	if updated.ErrorsMap["acc-2"] == nil {
		t.Fatal("expected per-account background error")
	}
}

func TestUpdateAccountsMsgPrunesDeleted(t *testing.T) {
	m := testModel(nil, management.Account{ID: "acc-1"})
	m.UsageData["acc-1"] = []quotaWindow{{}}
	m.UsageData["ghost"] = []quotaWindow{{}}
	m.ErrorsMap["ghost"] = errors.New("stale")

	next, _ := m.Update(AccountsMsg{Accounts: []management.Account{{ID: "acc-1"}}})
	updated := next.(Model)

	if _, ok := updated.UsageData["ghost"]; ok {
		t.Fatal("ghost usage data survived prune")
	}
	if _, ok := updated.ErrorsMap["ghost"]; ok {
		t.Fatal("ghost error survived prune")
	}
}

func TestUpdateQuitKey(t *testing.T) {
	m := testModel(nil, management.Account{ID: "acc-1"})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected quit cmd")
	}
	_ = next
}

func TestUpdateNavigationMovesActive(t *testing.T) {
	m := testModel(nil,
		management.Account{ID: "acc-1"},
		management.Account{ID: "acc-2"},
		management.Account{ID: "acc-3"},
	)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if next.(Model).activeAccountKey() != "acc-2" {
		t.Fatalf("active = %q, want acc-2", next.(Model).activeAccountKey())
	}

	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyLeft})
	if next.(Model).activeAccountKey() != "acc-1" {
		t.Fatalf("active = %q, want acc-1", next.(Model).activeAccountKey())
	}
}

func TestUpdateNoticeTimesOut(t *testing.T) {
	m := testModel(nil)
	m.Notice = "hello"
	m.noticeSeq = 7

	next, _ := m.Update(NoticeTimeoutMsg{Seq: 6})
	if next.(Model).Notice != "hello" {
		t.Fatal("stale timeout cleared notice")
	}

	next, _ = next.(Model).Update(NoticeTimeoutMsg{Seq: 7})
	if next.(Model).Notice != "" {
		t.Fatal("matching timeout did not clear notice")
	}
}

func TestUpdateFiltersNonCodexAccounts(t *testing.T) {
	m := testModel(nil, management.Account{ID: "codex:default", Provider: "codex"})

	next, _ := m.Update(AccountsMsg{Accounts: []management.Account{
		{ID: "antigravity:default", Provider: "antigravity"},
		{ID: "codex:default", Provider: "codex"},
		{ID: "codex:second", Provider: "codex"},
	}})
	updated := next.(Model)

	if len(updated.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2 codex accounts", len(updated.Accounts))
	}
	for _, account := range updated.Accounts {
		if account.Provider != "codex" {
			t.Fatalf("non-codex account %q survived filter", account.ID)
		}
	}
}

func TestAccountLabelPrefersEmail(t *testing.T) {
	if got := accountLabel(management.Account{ID: "98609d8a-85fb-4ff8-aee2-9344e68fbe3f", Email: "user@example.com"}); got != "user@example.com" {
		t.Fatalf("label = %q, want email", got)
	}
}

func TestAccountLabelFallsBackToShortID(t *testing.T) {
	if got := accountLabel(management.Account{ID: "98609d8a-85fb-4ff8-aee2-9344e68fbe3f"}); got != "98609d...be3f" {
		t.Fatalf("label = %q, want short id", got)
	}
	if got := accountLabel(management.Account{ID: "codex:default"}); got != "codex:...ault" {
		t.Fatalf("label = %q, want short id for long id", got)
	}
}
