package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func normalizeKey(key string) string {
	ruToEn := map[rune]rune{
		'й': 'q', 'ц': 'w', 'у': 'e', 'к': 'r', 'е': 't', 'н': 'y', 'г': 'u', 'ш': 'i', 'щ': 'o', 'з': 'p', 'х': '[', 'ъ': ']',
		'ф': 'a', 'ы': 's', 'в': 'd', 'а': 'f', 'п': 'g', 'р': 'h', 'о': 'j', 'л': 'k', 'д': 'l', 'ж': ';', 'э': '\'',
		'я': 'z', 'ч': 'x', 'с': 'c', 'м': 'v', 'и': 'b', 'т': 'n', 'ь': 'm', 'б': ',', 'ю': '.',
		'Й': 'Q', 'Ц': 'W', 'У': 'E', 'К': 'R', 'Е': 'T', 'Н': 'Y', 'Г': 'U', 'Ш': 'I', 'Щ': 'O', 'З': 'P', 'Х': '{', 'Ъ': '}',
		'Ф': 'A', 'Ы': 'S', 'В': 'D', 'А': 'F', 'П': 'G', 'Р': 'H', 'О': 'J', 'Л': 'K', 'Д': 'L', 'Ж': ':', 'Э': '"',
		'Я': 'Z', 'Ч': 'X', 'С': 'C', 'М': 'V', 'И': 'B', 'Т': 'N', 'Ь': 'M', 'Б': '<', 'Ю': '>',
	}

	if len(key) > 0 {
		runes := []rune(key)
		if len(runes) == 1 {
			if en, ok := ruToEn[runes[0]]; ok {
				return string(en)
			}
		}
	}
	return key
}

func normalizeHelpKey(rawKey, normalizedKey string) string {
	switch rawKey {
	case "?", "/", ".":
		return "help"
	}
	switch normalizedKey {
	case "?", "/", ".":
		return "help"
	}
	return normalizedKey
}

func (m Model) handleHelpOverlay(keyStr string) (tea.Model, tea.Cmd) {
	switch keyStr {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "help":
		m.resetHelpState()
		return m, nil
	}
	return m, nil
}

func (m Model) handleAuthLogin(keyStr string) (tea.Model, tea.Cmd) {
	switch keyStr {
	case "q", "ctrl+c":
		m.resetAuthLoginState()
		return m, tea.Quit
	case "esc":
		m.resetAuthLoginState()
		return m, nil
	case "c":
		if strings.TrimSpace(m.AuthLoginURL) == "" {
			return m, nil
		}
		return m, CopyToClipboardCmd(m.AuthLoginURL)
	}
	return m, nil
}

func (m Model) handleProviderSelect(keyStr string) (tea.Model, tea.Cmd) {
	switch keyStr {
	case "q", "ctrl+c":
		m.resetProviderSelectState()
		return m, tea.Quit
	case "esc", "n":
		m.resetProviderSelectState()
		return m, nil
	case "up", "k":
		m.ProviderCursor = (m.ProviderCursor - 1 + len(authProviders)) % len(authProviders)
		return m, nil
	case "down", "j":
		m.ProviderCursor = (m.ProviderCursor + 1) % len(authProviders)
		return m, nil
	case "enter":
		if m.ProviderCursor < 0 || m.ProviderCursor >= len(authProviders) {
			return m, nil
		}
		provider := authProviders[m.ProviderCursor]
		mode := m.ProviderSelectMode
		m.resetProviderSelectState()
		if mode == providerSelectModeSwitch {
			return switchProvider(m, provider)
		}
		return m, StartAuthCmd(m.api, provider)
	}
	return m, nil
}

func (m Model) handleIntegrationsOverlay(keyStr string) (tea.Model, tea.Cmd) {
	if m.IntegrationConfirm != "" {
		switch keyStr {
		case "q", "ctrl+c":
			m.IntegrationConfirm = ""
			return m, tea.Quit
		case "esc":
			m.IntegrationConfirm = ""
			return m, nil
		case "enter":
			id := m.IntegrationConfirm
			m.IntegrationConfirm = ""
			return m, ApplyHostIntegrationCmd(m.api, m.activeHostID(), id)
		}
		return m, nil
	}

	switch keyStr {
	case "q", "ctrl+c":
		m.resetIntegrationsState()
		return m, tea.Quit
	case "esc", "o":
		m.resetIntegrationsState()
		return m, nil
	case "up", "k":
		if len(m.Integrations) > 0 {
			m.IntegrationsCursor = (m.IntegrationsCursor - 1 + len(m.Integrations)) % len(m.Integrations)
		}
		return m, nil
	case "down", "j":
		if len(m.Integrations) > 0 {
			m.IntegrationsCursor = (m.IntegrationsCursor + 1) % len(m.Integrations)
		}
		return m, nil
	case "H":
		m.cycleActiveHost()
		return m, FetchHostIntegrationsCmd(m.api, m.activeHostID())
	case "enter":
		if m.IntegrationsCursor < 0 || m.IntegrationsCursor >= len(m.Integrations) {
			return m, nil
		}
		selected := m.Integrations[m.IntegrationsCursor]
		m.IntegrationConfirm = selected.ID
		return m, nil
	}
	return m, nil
}

func (m Model) handleActionMenu(keyStr string) (tea.Model, tea.Cmd) {
	items := m.actionMenuItems()
	if len(items) == 0 {
		m.resetActionMenuState()
		return m, nil
	}

	switch keyStr {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.resetActionMenuState()
		return m, nil
	case "up", "k":
		m.ActionMenuCursor = (m.ActionMenuCursor - 1 + len(items)) % len(items)
		return m, nil
	case "down", "j":
		m.ActionMenuCursor = (m.ActionMenuCursor + 1) % len(items)
		return m, nil
	case "enter":
		return m.confirmActionMenu()
	}

	if len(keyStr) == 1 && keyStr[0] >= '1' && keyStr[0] <= '9' {
		index := int(keyStr[0] - '1')
		if index >= 0 && index < len(items) {
			m.ActionMenuCursor = index
			return m.confirmActionMenu()
		}
	}

	for index, item := range items {
		if keyStr == item.Shortcut {
			m.ActionMenuCursor = index
			return m.confirmActionMenu()
		}
	}
	return m, nil
}

func (m Model) handleDeleteConfirm(keyStr string) (tea.Model, tea.Cmd) {
	switch keyStr {
	case "q", "ctrl+c":
		m.resetDeleteState()
		return m, tea.Quit
	case "esc":
		m.resetDeleteState()
		return m, nil
	case "enter":
		account := m.activeAccount()
		if account == nil {
			m.resetDeleteState()
			return m, nil
		}
		accountKey := account.ID
		m.Loading = true
		m.Err = nil
		m.Notice = ""
		m.ShowInfo = false
		m.resetDeleteState()
		return m, DeleteAccountCmd(m.api, accountKey)
	}
	return m, nil
}

func (m Model) handlePinConfirm(keyStr string) (tea.Model, tea.Cmd) {
	switch keyStr {
	case "q", "ctrl+c":
		m.PinConfirm = false
		return m, tea.Quit
	case "esc":
		m.PinConfirm = false
		return m, nil
	case ",":
		m.PinConfirm = false
		m.openSettingsOverlay()
		return m, nil
	case "enter":
		account := m.activeAccount()
		if account == nil {
			m.PinConfirm = false
			return m, nil
		}
		accountKey := account.ID
		providerID := account.Provider
		m.Loading = true
		m.Err = nil
		m.Notice = ""
		m.ShowInfo = false
		m.PinConfirm = false
		return m, PinProviderAccountCmd(m.api, providerID, accountKey)
	case "a":
		account := m.activeAccount()
		if account == nil {
			m.PinConfirm = false
			return m, nil
		}
		m.Loading = true
		m.Err = nil
		m.Notice = ""
		m.ShowInfo = false
		m.PinConfirm = false
		return m, PinProviderAccountCmd(m.api, account.Provider, "")
	}
	return m, nil
}

func (m Model) handleStatsOverlay(keyStr string) (tea.Model, tea.Cmd) {
	switch keyStr {
	case "q", "ctrl+c":
		m.resetStatsState()
		return m, tea.Quit
	case "esc", "u":
		m.resetStatsState()
		return m, nil
	case "r":
		m.StatsLoading = true
		return m, FetchStatsCmd(m.api, m.StatsRange)
	case "left", "h":
		return m.switchStatsRange(statsRangePrev(m.StatsRange))
	case "right", "l":
		return m.switchStatsRange(statsRangeNext(m.StatsRange))
	case "up", "k":
		if m.StatsScroll > 0 {
			m.StatsScroll--
		}
		return m, nil
	case "down", "j":
		if m.StatsScroll < m.maxStatsScroll() {
			m.StatsScroll++
		}
		return m, nil
	}
	if idx := statsRangeIndex(keyStr); idx >= 0 {
		return m.switchStatsRange(statsRanges[idx])
	}
	return m, nil
}

func (m Model) switchStatsRange(statsRange string) (tea.Model, tea.Cmd) {
	if statsRange == m.StatsRange {
		return m, nil
	}
	m.StatsRange = statsRange
	m.StatsData = nil
	m.StatsScroll = 0
	m.StatsLoading = true
	return m, FetchStatsCmd(m.api, m.StatsRange)
}
