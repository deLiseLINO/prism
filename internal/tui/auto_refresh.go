package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const autoRefreshTickInterval = time.Second

func autoRefreshTickCmd() tea.Cmd {
	return tea.Tick(autoRefreshTickInterval, func(now time.Time) tea.Msg {
		return AutoRefreshTickMsg{Now: now}
	})
}

const statsAutoRefreshInterval = 5 * time.Second

func (m Model) handleAutoRefreshTick(now time.Time) (tea.Model, tea.Cmd) {
	if m.lastRefresh == nil {
		m.lastRefresh = make(map[string]time.Time)
	}
	if m.refreshScheduled == nil {
		m.refreshScheduled = make(map[string]bool)
	}

	if !m.Settings.AutoRefreshEnabled {
		return m, autoRefreshTickCmd()
	}

	for i := range m.Accounts {
		accountKey := m.Accounts[i].ID
		if accountKey == "" {
			continue
		}
		if m.refreshScheduled[accountKey] {
			continue
		}
		if m.LoadingMap[accountKey] {
			continue
		}
		last, ok := m.lastRefresh[accountKey]
		if !ok {
			m.lastRefresh[accountKey] = now
			continue
		}
		if now.Sub(last) < m.autoRefreshInterval(accountKey) {
			continue
		}
		m.lastRefresh[accountKey] = now
		m.refreshScheduled[accountKey] = true
	}

	return m, tea.Batch(autoRefreshTickCmd(), m.fetchNextCmd(), m.statsAutoRefreshCmd(now))
}

func (m Model) statsAutoRefreshCmd(now time.Time) tea.Cmd {
	if !m.Settings.AutoRefreshEnabled || !m.StatsVisible || m.statsFetchInflight {
		return nil
	}
	if now.Sub(m.statsLastRefresh) < statsAutoRefreshInterval {
		return nil
	}
	m.statsLastRefresh = now
	m.statsFetchInflight = true
	return FetchStatsCmd(m.api, m.StatsRange)
}

func (m Model) autoRefreshInterval(accountKey string) time.Duration {
	if accountKey == m.activeAccountKey() {
		return time.Duration(m.Settings.ActiveIntervalSec) * time.Second
	}
	return time.Duration(m.Settings.BackgroundIntervalSec) * time.Second
}

func (m *Model) resetAutoRefreshTimers() {
	m.lastRefresh = make(map[string]time.Time)
}

func (m *Model) resetAutoRefreshTimer(accountKey string) {
	if accountKey == "" {
		return
	}
	if m.lastRefresh == nil {
		m.lastRefresh = make(map[string]time.Time)
	}
	delete(m.lastRefresh, accountKey)
}

func (m *Model) pruneAutoRefreshTimers() {
	if len(m.lastRefresh) == 0 && len(m.silentRefresh) == 0 {
		return
	}
	valid := make(map[string]struct{}, len(m.Accounts))
	for i := range m.Accounts {
		if m.Accounts[i].ID != "" {
			valid[m.Accounts[i].ID] = struct{}{}
		}
	}
	for key := range m.lastRefresh {
		if _, ok := valid[key]; !ok {
			delete(m.lastRefresh, key)
		}
	}
	for key := range m.silentRefresh {
		if _, ok := valid[key]; !ok {
			delete(m.silentRefresh, key)
		}
	}
}
