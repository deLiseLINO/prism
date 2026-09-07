package tui

import (
	"strings"

	"prism/internal/management"
)

func (m Model) activeAccount() *management.Account {
	if len(m.Accounts) == 0 {
		return nil
	}
	if m.ActiveAccountIx < 0 || m.ActiveAccountIx >= len(m.Accounts) {
		return nil
	}
	return &m.Accounts[m.ActiveAccountIx]
}

func (m Model) activeAccountKey() string {
	account := m.activeAccount()
	if account == nil {
		return ""
	}
	return account.ID
}

func (m Model) findAccountByID(accountKey string) *management.Account {
	if accountKey == "" {
		return nil
	}
	for i := range m.Accounts {
		if m.Accounts[i].ID == accountKey {
			return &m.Accounts[i]
		}
	}
	return nil
}

func (m Model) compactVisualOrderIndices() []int {
	if len(m.Accounts) == 0 {
		return nil
	}

	normal := make([]int, 0, len(m.Accounts))
	exhausted := make([]int, 0, len(m.Accounts))
	for i := range m.Accounts {
		if m.isCompactAccountExhausted(m.Accounts[i].ID) {
			exhausted = append(exhausted, i)
		} else {
			normal = append(normal, i)
		}
	}
	return append(normal, exhausted...)
}

func (m *Model) moveActiveAccountCompact(delta int) {
	order := m.compactVisualOrderIndices()
	if len(order) == 0 {
		return
	}

	pos := -1
	for i, idx := range order {
		if idx == m.ActiveAccountIx {
			pos = i
			break
		}
	}
	if pos == -1 {
		m.ActiveAccountIx = order[0]
		return
	}

	next := (pos + delta) % len(order)
	if next < 0 {
		next += len(order)
	}
	m.ActiveAccountIx = order[next]
}

func (m *Model) syncActiveAccount() {
	m.Loading = true
	m.Err = nil
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.Notice = ""
	m.clearTabWindowAnimations()

	if acc := m.activeAccount(); acc != nil {
		if windows, ok := m.UsageData[acc.ID]; ok {
			m.Loading = false
			m.Err = m.ErrorsMap[acc.ID]
			if !m.CompactMode {
				m.startTabWindowAnimationsFromZero(acc.ID, windows, tabSwitchAnimationDuration)
			}
			return
		}
	}
}

func (m *Model) normalizeActiveAccountForView(activeKey string) {
	activeKey = strings.TrimSpace(activeKey)
	if len(m.Accounts) == 0 {
		m.ActiveAccountIx = 0
		return
	}

	if activeKey != "" {
		for i := range m.Accounts {
			if m.Accounts[i].ID == activeKey {
				m.ActiveAccountIx = i
				return
			}
		}
	}

	if m.CompactMode {
		if order := m.compactVisualOrderIndices(); len(order) > 0 {
			m.ActiveAccountIx = order[0]
			return
		}
	}

	m.ActiveAccountIx = 0
}
