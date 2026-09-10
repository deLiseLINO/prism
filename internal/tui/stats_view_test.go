package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"prism/internal/management"
)

func statsTestResponse() management.StatsResponse {
	return management.StatsResponse{
		Range: "24h",
		Overview: management.StatsOverview{
			Requests:     10,
			Completed:    8,
			Failed:       2,
			InputTokens:  1500,
			OutputTokens: 500,
			CachedTokens: 200,
			TotalTokens:  2000,
			Measured:     5,
		},
		Providers: []management.StatsProvider{
			{Provider: "codex", StatsOverview: management.StatsOverview{Requests: 6, Completed: 6, TotalTokens: 1500}},
			{Provider: "antigravity", StatsOverview: management.StatsOverview{Requests: 4, Completed: 2, Failed: 2, TotalTokens: 500}},
		},
		Models: []management.StatsModel{
			{Model: "gpt-5", Provider: "codex", StatsOverview: management.StatsOverview{Requests: 6, Completed: 6, TotalTokens: 1500}},
			{Model: "gemini-3-pro", Provider: "antigravity", StatsOverview: management.StatsOverview{Requests: 4, Completed: 2, Failed: 2, TotalTokens: 500}},
		},
	}
}

func TestStatsHotkeyOpensOverlay(t *testing.T) {
	client := newFakeClient(management.Account{ID: "acc-1"})
	m := testModel(client, management.Account{ID: "acc-1"})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	updated := next.(Model)
	if !updated.StatsVisible {
		t.Fatal("stats overlay did not open")
	}
	if !updated.StatsLoading {
		t.Fatal("stats overlay not marked loading")
	}
	msgs := batchMsgs(cmd)
	if len(msgs) != 1 {
		t.Fatalf("cmds = %v, want single fetch", msgs)
	}
	if _, ok := msgs[0].(StatsMsg); !ok {
		t.Fatalf("cmd msg = %T, want StatsMsg", msgs[0])
	}
	if len(client.statsRange) != 1 || client.statsRange[0] != "24h" {
		t.Fatalf("stats range = %v, want [24h]", client.statsRange)
	}
}

func TestStatsMsgStoresDataAndRenders(t *testing.T) {
	client := newFakeClient(management.Account{ID: "acc-1"})
	client.stats = statsTestResponse()
	m := testModel(client, management.Account{ID: "acc-1"})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	next, _ = next.(Model).Update(batchMsgs(cmd)[0])
	updated := next.(Model)

	if updated.StatsData == nil {
		t.Fatal("stats data not stored")
	}
	if updated.StatsLoading {
		t.Fatal("stats still loading after msg")
	}

	view := updated.View()
	for _, want := range []string{"Stats", "codex", "antigravity", "gpt-5", "tot 1500", "tot 500", "5/10 (50%)", "[←/→] Range"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q", want)
		}
	}
}

func TestStatsOverlayCloseKeys(t *testing.T) {
	client := newFakeClient(management.Account{ID: "acc-1"})
	m := testModel(client, management.Account{ID: "acc-1"})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(Model).StatsVisible {
		t.Fatal("esc did not close stats overlay")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	updated := next.(Model)
	if updated.StatsVisible {
		t.Fatal("u did not close stats overlay")
	}
	if updated.StatsData != nil {
		t.Fatal("stats data not reset on close")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if next.(Model).StatsVisible {
		t.Fatal("q did not close stats overlay")
	}
}

func TestStatsRangeSwitchRefetches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := newFakeClient(management.Account{ID: "acc-1"})
	m := testModel(client, management.Account{ID: "acc-1"})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	batchMsgs(cmd)
	next, cmd = next.(Model).Update(tea.KeyMsg{Type: tea.KeyRight})
	batchMsgs(cmd)
	updated := next.(Model)
	if updated.StatsRange != "7d" {
		t.Fatalf("range = %q, want 7d", updated.StatsRange)
	}

	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	batchMsgs(cmd)
	if next.(Model).StatsRange != "1h" {
		t.Fatalf("range = %q, want 1h", next.(Model).StatsRange)
	}
	want := []string{"24h", "7d", "1h"}
	for i, r := range want {
		if client.statsRange[i] != r {
			t.Fatalf("fetch %d = %q, want %q", i, client.statsRange[i], r)
		}
	}

	state, err := LoadUIState()
	if err != nil {
		t.Fatal(err)
	}
	if state.StatsRange != "1h" {
		t.Fatalf("persisted range = %q, want 1h", state.StatsRange)
	}

	restored := InitialModel(client, false)
	if restored.StatsRange != "1h" {
		t.Fatalf("restored range = %q, want 1h", restored.StatsRange)
	}
}

func TestUIStateStatsRangeNormalizes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := SaveUIState(UIState{StatsRange: "bogus"}); err != nil {
		t.Fatal(err)
	}
	state, err := LoadUIState()
	if err != nil {
		t.Fatal(err)
	}
	if state.StatsRange != "24h" {
		t.Fatalf("range = %q, want fallback 24h", state.StatsRange)
	}

	if err := SaveUIState(UIState{StatsRange: "30d"}); err != nil {
		t.Fatal(err)
	}
	state, err = LoadUIState()
	if err != nil {
		t.Fatal(err)
	}
	if state.StatsRange != "30d" {
		t.Fatalf("range = %q, want 30d", state.StatsRange)
	}
}

func TestStatsRefreshHotkeyRefetches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := newFakeClient(management.Account{ID: "acc-1"})
	m := testModel(client, management.Account{ID: "acc-1"})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	batchMsgs(cmd)
	next, cmd = next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	batchMsgs(cmd)
	if len(client.statsRange) != 2 {
		t.Fatalf("fetched ranges = %v, want 2 fetches", client.statsRange)
	}
}

func TestStatsAutoRefreshPollsWhileVisible(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := newFakeClient(management.Account{ID: "acc-1"})
	m := testModel(client, management.Account{ID: "acc-1"})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	var openMsg StatsMsg
	for _, msg := range batchMsgs(cmd) {
		if typed, ok := msg.(StatsMsg); ok {
			openMsg = typed
		}
	}
	next, _ = next.(Model).Update(openMsg)
	updated := next.(Model)
	fetches := len(client.statsRange)

	early := updated.statsLastRefresh.Add(2 * time.Second)
	if updated.statsAutoRefreshCmd(early) != nil {
		t.Fatal("poll scheduled before interval elapsed")
	}

	due := updated.statsLastRefresh.Add(statsAutoRefreshInterval)
	poll := updated.statsAutoRefreshCmd(due)
	if poll == nil {
		t.Fatal("poll not scheduled at interval")
	}
	if _, ok := poll().(StatsMsg); !ok {
		t.Fatal("poll cmd did not fetch stats")
	}
	if len(client.statsRange) != fetches+1 {
		t.Fatalf("fetched ranges = %v, want one poll fetch", client.statsRange)
	}

	closed, _ := updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if closed.(Model).statsAutoRefreshCmd(due.Add(statsAutoRefreshInterval)) != nil {
		t.Fatal("poll scheduled with stats overlay closed")
	}
}

func TestStatsAutoRefreshDisabledSkipsPoll(t *testing.T) {
	client := newFakeClient(management.Account{ID: "acc-1"})
	m := testModel(client, management.Account{ID: "acc-1"})
	m.Settings.AutoRefreshEnabled = false
	m.StatsVisible = true
	m.statsLastRefresh = time.Now().Add(-time.Hour)

	if m.statsAutoRefreshCmd(time.Now()) != nil {
		t.Fatal("poll scheduled with auto-refresh disabled")
	}
}

func TestStatsScrollClamps(t *testing.T) {
	client := newFakeClient(management.Account{ID: "acc-1"})
	m := testModel(client, management.Account{ID: "acc-1"})
	m.StatsVisible = true
	m.StatsData = &management.StatsResponse{
		Overview: management.StatsOverview{Requests: 1, Measured: 1},
		Providers: func() []management.StatsProvider {
			out := make([]management.StatsProvider, 0, 30)
			for i := 0; i < 30; i++ {
				out = append(out, management.StatsProvider{Provider: "p", StatsOverview: management.StatsOverview{Requests: 1, Completed: 1, TotalTokens: int64(i + 1)}})
			}
			return out
		}(),
	}
	m.Height = 20

	for i := 0; i < 100; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(Model)
	}
	maxScroll := m.maxStatsScroll()
	if maxScroll <= 0 {
		t.Fatalf("maxScroll = %d, want positive with overflow", maxScroll)
	}
	if m.StatsScroll != maxScroll {
		t.Fatalf("scroll = %d, want clamp at %d", m.StatsScroll, maxScroll)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if next.(Model).StatsScroll != maxScroll-1 {
		t.Fatalf("scroll = %d, want %d", next.(Model).StatsScroll, maxScroll-1)
	}
}

func TestStatsEmptyStateRenders(t *testing.T) {
	client := newFakeClient(management.Account{ID: "acc-1"})
	m := testModel(client, management.Account{ID: "acc-1"})
	m.StatsVisible = true
	m.StatsRange = "24h"

	view := m.View()
	if !strings.Contains(view, "No stats yet.") {
		t.Fatalf("view missing empty state, got:\n%s", view)
	}
	if !strings.Contains(view, "[←/→] Range") {
		t.Fatalf("view missing footer hint")
	}
}

func TestStatsErrMsgClosesOverlay(t *testing.T) {
	client := newFakeClient(management.Account{ID: "acc-1"})
	client.statsErr = errors.New("boom")
	m := testModel(client, management.Account{ID: "acc-1"})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	msgs := batchMsgs(cmd)
	if len(msgs) != 1 {
		t.Fatalf("cmds = %v, want single fetch", msgs)
	}
	if _, ok := msgs[0].(StatsErrMsg); !ok {
		t.Fatalf("cmd msg = %T, want StatsErrMsg", msgs[0])
	}

	next, _ = next.(Model).Update(msgs[0])
	updated := next.(Model)
	if updated.StatsVisible {
		t.Fatal("stats overlay still visible after error")
	}
	if updated.Err == nil {
		t.Fatal("expected error set")
	}
}
