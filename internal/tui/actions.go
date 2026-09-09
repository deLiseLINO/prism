package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	actionMenuRefresh      = "refresh"
	actionMenuPause        = "pause"
	actionMenuResume       = "resume"
	actionMenuPin          = "pin"
	actionMenuInfo         = "info"
	actionMenuDelete       = "delete"
	actionMenuRefreshAll   = "refresh_all"
	actionMenuAdd          = "add"
	actionMenuIntegrations = "integrations"
	actionMenuView         = "view"
	actionMenuProvider     = "provider"
	actionMenuSettings     = "settings"
	actionMenuHelp         = "help"
)

func (m Model) confirmActionMenu() (tea.Model, tea.Cmd) {
	items := m.actionMenuItems()
	if len(items) == 0 {
		m.resetActionMenuState()
		return m, nil
	}
	if m.ActionMenuCursor < 0 || m.ActionMenuCursor >= len(items) {
		m.ActionMenuCursor = 0
	}

	selected := items[m.ActionMenuCursor]
	m.resetActionMenuState()

	switch selected.ID {
	case actionMenuRefresh:
		return m.beginRefreshActive()
	case actionMenuPause:
		return m.beginPauseResume(clientPause)
	case actionMenuResume:
		return m.beginPauseResume(clientResume)
	case actionMenuPin:
		m.beginPinFlow()
		return m, nil
	case actionMenuInfo:
		m.ShowInfo = true
		m.Notice = ""
		m.Err = nil
		return m, nil
	case actionMenuDelete:
		return m.beginDeleteFlow()
	case actionMenuRefreshAll:
		return m.beginRefreshAll()
	case actionMenuAdd:
		return m.beginAddAccount()
	case actionMenuIntegrations:
		return m.beginIntegrationsFlow()
	case actionMenuView:
		return m.toggleViewMode()
	case actionMenuProvider:
		return m.beginProviderSwitch()
	case actionMenuSettings:
		m.openSettingsOverlay()
		return m, nil
	case actionMenuHelp:
		m.openHelpOverlay()
		return m, nil
	default:
		return m, nil
	}
}

func (m *Model) beginPinFlow() {
	account := m.activeAccount()
	if account == nil {
		return
	}
	m.resetHelpState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.ShowInfo = false
	m.Err = nil
	m.Notice = ""
	m.PinConfirm = true
}

func (m Model) beginPinHotkey() (tea.Model, tea.Cmd) {
	account := m.activeAccount()
	if account == nil {
		return m, nil
	}
	m.beginPinFlow()
	return m, nil
}

func (m *Model) openHelpOverlay() {
	m.resetActionMenuState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.ShowInfo = false
	m.Notice = ""
	m.Err = nil
	m.HelpVisible = true
}

func (m *Model) resetHelpState() {
	m.HelpVisible = false
}

func (m *Model) openActionMenu() {
	m.resetHelpState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.ShowInfo = false
	m.Notice = ""
	m.Err = nil
	m.ActionMenuVisible = true
	m.ActionMenuCursor = 0
}

func (m *Model) resetActionMenuState() {
	m.ActionMenuVisible = false
	m.ActionMenuCursor = 0
}

type pauseResume int

const (
	clientPause pauseResume = iota
	clientResume
)

func (m Model) beginPauseResume(action pauseResume) (tea.Model, tea.Cmd) {
	account := m.activeAccount()
	if account == nil {
		return m, nil
	}
	accountKey := account.ID
	version := account.Version

	m.Loading = true
	m.Err = nil
	m.resetHelpState()
	m.resetActionMenuState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.ShowInfo = false
	m.Notice = ""

	if action == clientPause {
		return m, PauseAccountCmd(m.api, accountKey, version)
	}
	return m, ResumeAccountCmd(m.api, accountKey, version)
}

func (m Model) beginPauseResumeHotkey() (tea.Model, tea.Cmd) {
	account := m.activeAccount()
	if account == nil {
		return m, nil
	}
	if strings.ToLower(account.State) == "paused" {
		return m.beginPauseResume(clientResume)
	}
	return m.beginPauseResume(clientPause)
}

func (m Model) beginDeleteFlow() (tea.Model, tea.Cmd) {
	if len(m.Accounts) == 0 {
		return m, nil
	}
	account := m.activeAccount()
	if account == nil {
		return m, nil
	}

	m.resetActionMenuState()
	m.resetHelpState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.ShowInfo = false
	m.Err = nil
	m.Notice = ""
	m.DeleteConfirm = true
	return m, nil
}

func (m *Model) resetDeleteState() {
	m.DeleteConfirm = false
}

func (m *Model) resetIntegrationsState() {
	m.IntegrationsVisible = false
	m.Integrations = nil
	m.IntegrationsCursor = 0
	m.IntegrationConfirm = ""
}

func (m *Model) resetAuthLoginState() {
	m.AuthLoginVisible = false
	m.AuthSession = ""
	m.AuthLoginURL = ""
	m.AuthBrowserFailed = false
	m.AuthLoginStatus = ""
}

func (m *Model) resetProviderSelectState() {
	m.ProviderSelectVisible = false
	m.ProviderSelectMode = providerSelectModeAuth
	m.ProviderCursor = 0
}

func (m Model) beginRefreshActive() (tea.Model, tea.Cmd) {
	if m.activeAccount() == nil {
		return m, nil
	}
	m.Loading = true
	m.Err = nil
	m.resetHelpState()
	m.resetActionMenuState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.Notice = ""

	if m.LoadingMap == nil {
		m.LoadingMap = make(map[string]bool)
	}
	delete(m.UsageData, m.activeAccountKey())
	delete(m.ErrorsMap, m.activeAccountKey())
	delete(m.compactBarAnimations, m.activeAccountKey())
	m.clearTabWindowAnimations()
	m.resetAutoRefreshTimer(m.activeAccountKey())
	return m, m.fetchNextCmd()
}

func (m Model) beginRefreshAll() (tea.Model, tea.Cmd) {
	m.Loading = true
	m.Err = nil
	m.resetHelpState()
	m.resetActionMenuState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.Notice = ""

	m.UsageData = make(map[string][]quotaWindow)
	m.ErrorsMap = make(map[string]error)
	m.LoadingMap = make(map[string]bool)
	m.compactBarAnimations = make(map[string]compactBarAnimation)
	m.tabWindowAnimations = make(map[string]tabWindowAnimation)
	m.animationTicking = false
	m.resetAutoRefreshTimers()

	return m, m.fetchNextCmd()
}

func (m Model) toggleViewMode() (tea.Model, tea.Cmd) {
	m.CompactMode = !m.CompactMode
	if m.CompactMode {
		m.clearTabWindowAnimations()
	} else {
		m.clearCompactBarAnimations()
	}
	m.resetHelpState()
	m.resetActionMenuState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.Notice = ""
	return m, tea.Batch(m.fetchNextCmd(), m.ensureAnimationTickCmd(), SaveUIStateSnapshotCmd(m.uiStateSnapshot()))
}

func (m Model) beginAddAccount() (tea.Model, tea.Cmd) {
	if m.AuthLoginVisible || m.ProviderSelectVisible {
		return m, nil
	}
	m.Loading = false
	m.Err = nil
	m.resetHelpState()
	m.resetActionMenuState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.ShowInfo = false
	m.Notice = ""
	m.ProviderSelectVisible = true
	m.ProviderSelectMode = providerSelectModeAuth
	m.ProviderCursor = 0
	return m, nil
}

func (m *Model) beginProviderSwitch() (tea.Model, tea.Cmd) {
	if m.AuthLoginVisible || m.ProviderSelectVisible {
		return m, nil
	}
	m.resetHelpState()
	m.resetActionMenuState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.ShowInfo = false
	m.Err = nil
	m.Notice = ""
	m.ProviderCursor = providerCursor(m.ProviderFilter)
	m.ProviderSelectVisible = true
	m.ProviderSelectMode = providerSelectModeSwitch
	return m, nil
}

func switchProvider(m Model, provider string) (tea.Model, tea.Cmd) {
	if provider == m.ProviderFilter {
		return m, nil
	}
	m.ProviderFilter = provider
	return m, tea.Batch(
		ReloadAccountsCmd(m.api, m.activeAccountKey()),
		SaveUIStateSnapshotCmd(m.uiStateSnapshot()),
	)
}

func providerCursor(provider string) int {
	for i, candidate := range authProviders {
		if candidate == provider {
			return i
		}
	}
	return 0
}

func (m Model) beginIntegrationsFlow() (tea.Model, tea.Cmd) {
	m.resetHelpState()
	m.resetActionMenuState()
	m.resetDeleteState()
	m.ShowInfo = false
	m.Err = nil
	m.Notice = ""
	m.IntegrationsVisible = true
	m.IntegrationsCursor = 0
	m.IntegrationConfirm = ""
	return m, FetchIntegrationsCmd(m.api)
}
