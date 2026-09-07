package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSettingsOverlayToggle(t *testing.T) {
	m := testModel(nil)
	if !m.Settings.AutoRefreshEnabled {
		t.Fatal("expected auto-refresh enabled by default")
	}

	m.openSettingsOverlay()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(Model)

	if updated.Settings.AutoRefreshEnabled {
		t.Fatal("toggle did not disable auto-refresh")
	}
}

func TestSettingsOverlayStepInterval(t *testing.T) {
	m := testModel(nil)
	m.openSettingsOverlay()
	m.moveSettingsCursor(1)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	updated := next.(Model)

	if updated.Settings.ActiveIntervalSec != m.Settings.ActiveIntervalSec+activeIntervalStepSec {
		t.Fatalf("active interval = %d", updated.Settings.ActiveIntervalSec)
	}
}

func TestSettingsOverlayTypingClamps(t *testing.T) {
	m := testModel(nil)
	m.Settings.ActiveIntervalSec = 30
	m.openSettingsOverlay()
	m.settingsCursor = settingsRowActiveInterval

	m.typeSettingsDigit(9)
	m.typeSettingsDigit(9)
	m.typeSettingsDigit(9)

	if m.Settings.ActiveIntervalSec != ActiveIntervalMaxSec {
		t.Fatalf("active interval = %d, want clamp to %d", m.Settings.ActiveIntervalSec, ActiveIntervalMaxSec)
	}
}

func TestFormatIntervalLabel(t *testing.T) {
	cases := []struct {
		seconds int
		want    string
	}{
		{45, "45s"},
		{120, "2m"},
		{3600, "1h"},
		{5400, "1h 30m"},
	}
	for _, tc := range cases {
		if got := formatIntervalLabel(tc.seconds); got != tc.want {
			t.Fatalf("formatIntervalLabel(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}
