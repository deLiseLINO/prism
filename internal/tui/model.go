package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"

	"prism/internal/management"
)

type Model struct {
	api Client

	Accounts        []management.Account
	ActiveAccountIx int
	Loading         bool
	Err             error
	Notice          string
	noticeSeq       int

	UsageData          map[string][]quotaWindow
	LoadingMap         map[string]bool
	ErrorsMap          map[string]error
	lastRefresh        map[string]time.Time
	refreshScheduled   map[string]bool
	silentRefresh      map[string]bool
	statsLastRefresh   time.Time
	statsFetchInflight bool

	CompactMode     bool
	Width           int
	Height          int
	defaultProgress progress.Model
	shortProgress   progress.Model

	HelpVisible         bool
	ActionMenuVisible   bool
	ActionMenuCursor    int
	SettingsVisible     bool
	Settings            Settings
	settingsCursor      int
	settingsDraft       int
	settingsDraftActive bool
	ShowInfo            bool

	ProviderFilter        string
	PinnedAccounts        map[string]string
	ProviderSelectVisible bool
	ProviderSelectMode    providerSelectMode
	ProviderCursor        int
	AuthLoginVisible      bool
	AuthProvider          string
	AuthSession           string
	AuthLoginURL          string
	AuthBrowserFailed     bool
	AuthLoginStatus       string

	IntegrationsVisible bool
	Integrations        []integrationStatus
	IntegrationsCursor  int
	IntegrationConfirm  string
	StatsVisible        bool
	StatsRange          string
	StatsData           *management.StatsResponse
	StatsLoading        bool
	StatsScroll         int

	DeleteConfirm bool
	PinConfirm    bool

	compactBarAnimations map[string]compactBarAnimation
	tabWindowAnimations  map[string]tabWindowAnimation
	animationTicking     bool
}

var authProviders = []string{"codex", "antigravity"}

const defaultProviderFilter = "codex"

const defaultStatsRange = "24h"

func InitialModel(client Client, compactMode bool) Model {
	settings, err := LoadSettings()
	if err != nil {
		settings = DefaultSettings()
	}
	uiState, err := LoadUIState()
	if err != nil {
		uiState = UIState{}
	}

	compact := compactMode || uiState.CompactMode
	m := Model{
		api:                  client,
		Loading:              true,
		CompactMode:          compact,
		Settings:             settings,
		ProviderFilter:       normalizeProviderFilter(uiState.ProviderFilter),
		UsageData:            make(map[string][]quotaWindow),
		LoadingMap:           make(map[string]bool),
		ErrorsMap:            make(map[string]error),
		lastRefresh:          make(map[string]time.Time),
		refreshScheduled:     make(map[string]bool),
		silentRefresh:        make(map[string]bool),
		compactBarAnimations: make(map[string]compactBarAnimation),
		StatsRange:           normalizeStatsRange(uiState.StatsRange),
		tabWindowAnimations:  make(map[string]tabWindowAnimation),
		defaultProgress: progress.New(
			progress.WithDefaultGradient(),
			progress.WithoutPercentage(),
		),
		shortProgress: progress.New(
			progress.WithGradient(shortBarGradientStart, shortBarGradientEnd),
			progress.WithoutPercentage(),
		),
	}
	m.normalizeActiveAccountForView(uiState.ActiveAccountKey)
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.SetWindowTitle("Prism UI"),
		autoRefreshTickCmd(),
		ReloadAccountsCmd(m.api, ""),
		FetchPinnedAccountsCmd(m.api),
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		rawKey := msg.String()
		keyStr := normalizeHelpKey(rawKey, normalizeKey(rawKey))

		if m.SettingsVisible {
			return m.handleSettingsOverlay(keyStr)
		}
		if m.HelpVisible {
			return m.handleHelpOverlay(keyStr)
		}
		if m.AuthLoginVisible {
			return m.handleAuthLogin(keyStr)
		}
		if m.ProviderSelectVisible {
			return m.handleProviderSelect(keyStr)
		}
		if m.IntegrationsVisible {
			return m.handleIntegrationsOverlay(keyStr)
		}
		if m.StatsVisible {
			return m.handleStatsOverlay(keyStr)
		}
		if m.ActionMenuVisible {
			return m.handleActionMenu(keyStr)
		}
		if m.DeleteConfirm {
			return m.handleDeleteConfirm(keyStr)
		}
		if m.PinConfirm {
			return m.handlePinConfirm(keyStr)
		}

		switch keyStr {
		case "help":
			m.openHelpOverlay()
			return m, nil
		case ",":
			m.openSettingsOverlay()
			return m, nil
		case "s":
			return m.beginPinHotkey()
		case "enter":
			if m.Err != nil {
				m.Err = nil
				return m, nil
			}
			if m.activeAccount() == nil {
				return m, nil
			}
			m.openActionMenu()
			return m, nil
		case "x", "delete":
			return m.beginDeleteFlow()
		case "esc":
			if m.ShowInfo {
				m.ShowInfo = false
				return m, nil
			}
			if m.Err != nil {
				m.Err = nil
				return m, nil
			}
			if m.Notice != "" {
				m.Notice = ""
				return m, nil
			}
			return m, tea.Quit
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			return m.beginRefreshActive()
		case "R":
			return m.beginRefreshAll()
		case "u":
			return m.beginStatsFlow()
		case "i":
			m.resetHelpState()
			m.resetActionMenuState()
			m.ShowInfo = !m.ShowInfo
			m.resetDeleteState()
			m.resetIntegrationsState()
			m.resetStatsState()
			m.Notice = ""
			return m, nil
		case "v", "c":
			return m.toggleViewMode()
		case "n":
			return m.beginAddAccount()
		case "P":
			return m.beginProviderSwitch()
		case "o":
			return m.beginIntegrationsFlow()
		case "p":
			return m.beginPauseResumeHotkey()
		case "right", "l", "down", "j":
			if len(m.Accounts) > 1 {
				if m.CompactMode {
					m.moveActiveAccountCompact(1)
				} else {
					m.ActiveAccountIx = (m.ActiveAccountIx + 1) % len(m.Accounts)
				}
				m.syncActiveAccount()
				return m, tea.Batch(m.fetchNextCmd(), m.ensureAnimationTickCmd(), SaveUIStateSnapshotCmd(m.uiStateSnapshot()))
			}
		case "left", "h", "up", "k":
			if len(m.Accounts) > 1 {
				if m.CompactMode {
					m.moveActiveAccountCompact(-1)
				} else {
					m.ActiveAccountIx = (m.ActiveAccountIx - 1 + len(m.Accounts)) % len(m.Accounts)
				}
				m.syncActiveAccount()
				return m, tea.Batch(m.fetchNextCmd(), m.ensureAnimationTickCmd(), SaveUIStateSnapshotCmd(m.uiStateSnapshot()))
			}
		}

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height

		barWidth := msg.Width - 72
		if barWidth < 20 {
			barWidth = 20
		}
		if barWidth > 50 {
			barWidth = 50
		}
		m.defaultProgress.Width = barWidth
		m.shortProgress.Width = barWidth

	case AccountsMsg:
		m.ProviderFilter = normalizeProviderFilter(m.ProviderFilter)
		m.Accounts = filterProviderAccounts(msg.Accounts, m.ProviderFilter)
		m.ActiveAccountIx = 0
		m.UsageData = make(map[string][]quotaWindow)
		m.pruneAccountKeyedMaps()
		m.pruneCompactBarAnimations()
		m.pruneAutoRefreshTimers()
		m.clearTabWindowAnimations()
		m.resetDeleteState()
		m.resetIntegrationsState()
		m.resetStatsState()
		m.PinConfirm = false

		if len(m.Accounts) == 0 {
			m.Loading = false
			m.Err = fmt.Errorf("no accounts found; press n to add account")
			m.Notice = ""
			return m, nil
		}

		m.normalizeActiveAccountForView(msg.ActiveKey)
		m.Loading = true
		m.Err = nil
		m.Notice = msg.Notice

		if m.LoadingMap == nil {
			m.LoadingMap = make(map[string]bool)
		}
		activeFetch := tea.Cmd(nil)
		if account := m.activeAccount(); account != nil {
			m.LoadingMap[account.ID] = true
			activeFetch = m.fetchAccountCmd(account.ID)
		}

		cmds := []tea.Cmd{activeFetch, m.fetchNextCmd()}
		if msg.Notice != "" {
			m.noticeSeq++
			cmds = append(cmds, scheduleNoticeClearCmd(m.noticeSeq))
		}
		return m, tea.Batch(cmds...)

	case DataMsg:
		if msg.AccountKey == "" {
			return m, nil
		}
		prevWindows, hadPrevData := m.UsageData[msg.AccountKey]
		wasLoading := m.LoadingMap[msg.AccountKey]
		silent := m.silentRefresh[msg.AccountKey]
		delete(m.silentRefresh, msg.AccountKey)

		m.UsageData[msg.AccountKey] = msg.Windows
		m.LoadingMap[msg.AccountKey] = false
		delete(m.ErrorsMap, msg.AccountKey)

		if !silent {
			if m.CompactMode {
				m.startCompactBarAnimation(msg.AccountKey, primaryQuotaWindow(prevWindows), hadPrevData, primaryQuotaWindow(msg.Windows), wasLoading)
			} else {
				delete(m.compactBarAnimations, msg.AccountKey)
			}
			if msg.AccountKey == m.activeAccountKey() {
				m.startTabWindowAnimations(msg.AccountKey, prevWindows, hadPrevData, msg.Windows, wasLoading, tabLoadAnimationDuration)
			}
		}
		if msg.AccountKey == m.activeAccountKey() {
			m.Loading = false
			m.Err = nil
		}
		return m, tea.Batch(m.fetchNextCmd(), m.ensureAnimationTickCmd())

	case ErrMsg:
		if m.ErrorsMap == nil {
			m.ErrorsMap = make(map[string]error)
			m.LoadingMap = make(map[string]bool)
		}
		if msg.AccountKey != "" {
			m.ErrorsMap[msg.AccountKey] = msg.Err
			m.LoadingMap[msg.AccountKey] = false
			delete(m.silentRefresh, msg.AccountKey)
			delete(m.compactBarAnimations, msg.AccountKey)
			if msg.AccountKey == m.activeAccountKey() {
				m.clearTabWindowAnimations()
			}
			if msg.AccountKey != m.activeAccountKey() {
				return m, tea.Batch(m.fetchNextCmd(), m.ensureAnimationTickCmd())
			}
		}
		m.Loading = false
		m.Err = msg.Err
		m.Notice = ""
		return m, tea.Batch(m.fetchNextCmd(), m.ensureAnimationTickCmd())

	case NoticeMsg:
		m.Loading = false
		m.Err = nil
		m.Notice = msg.Text
		if msg.Text == "" {
			return m, nil
		}
		m.noticeSeq++
		return m, scheduleNoticeClearCmd(m.noticeSeq)

	case NoticeTimeoutMsg:
		if msg.Seq != m.noticeSeq {
			return m, nil
		}
		m.Notice = ""
		return m, nil

	case AuthStartedMsg:
		m.ProviderSelectVisible = false
		m.AuthLoginVisible = true
		m.AuthProvider = msg.Provider
		m.AuthSession = msg.Session
		m.AuthLoginURL = strings.TrimSpace(msg.AuthURL)
		m.AuthBrowserFailed = false
		m.AuthLoginStatus = ""
		m.Loading = false
		m.Err = nil
		m.Notice = ""
		return m, tea.Batch(PollAuthCmd(m.api, msg.Provider, msg.Session), OpenAuthURLCmd(m.AuthLoginURL))

	case AuthPendingMsg:
		if !m.AuthLoginVisible {
			return m, nil
		}
		return m, PollAuthCmd(m.api, m.AuthProvider, m.AuthSession)

	case AuthFinishedMsg:
		if !m.AuthLoginVisible {
			return m, nil
		}
		m.AuthLoginVisible = false
		m.AuthSession = ""
		m.AuthLoginURL = ""
		m.AuthBrowserFailed = false
		m.AuthLoginStatus = ""
		m.Loading = false
		if msg.Err != nil {
			m.Err = fmt.Errorf("login failed: %w", msg.Err)
			return m, nil
		}
		return m, reloadAccountsNoticeCmd(m.api, "account added: "+msg.Provider)

	case AuthBrowserOpenResultMsg:
		if !m.AuthLoginVisible {
			return m, nil
		}
		if msg.Err != nil {
			m.AuthBrowserFailed = true
			m.AuthLoginStatus = msg.Err.Error()
			return m, nil
		}
		m.AuthLoginStatus = strings.TrimSpace(msg.Text)
		return m, nil

	case AuthCopyResultMsg:
		if !m.AuthLoginVisible {
			return m, nil
		}
		if msg.Err != nil {
			m.AuthLoginStatus = msg.Err.Error()
			return m, nil
		}
		m.AuthLoginStatus = strings.TrimSpace(msg.Text)
		return m, nil

	case PinnedAccountsMsg:
		m.PinnedAccounts = msg.Pinned
		return m, nil

	case IntegrationsMsg:
		m.Integrations = msg.Integrations
		m.IntegrationsCursor = 0
		m.Loading = false
		m.Err = nil
		return m, nil
	case StatsMsg:
		m.statsFetchInflight = false
		if !m.StatsVisible {
			return m, nil
		}
		if msg.Range != m.StatsRange {
			return m, nil
		}
		stats := msg.Stats
		m.StatsData = &stats
		m.StatsLoading = false
		m.StatsScroll = 0
		return m, nil

	case StatsErrMsg:
		m.statsFetchInflight = false
		if !m.StatsVisible {
			return m, nil
		}
		m.resetStatsState()
		m.Err = msg.Err
		m.Notice = ""
		return m, nil

	case IntegrationApplyResultMsg:
		m.IntegrationConfirm = ""
		if msg.Err != nil {
			m.Err = msg.Err
			return m, nil
		}
		if !msg.OK {
			if strings.TrimSpace(msg.Reason) == "" {
				msg.Reason = "unknown reason"
			}
			m.Err = fmt.Errorf("apply failed: %s", msg.Reason)
			return m, FetchIntegrationsCmd(m.api)
		}
		m.Err = nil
		m.Notice = "integration applied: " + msg.ID
		m.noticeSeq++
		return m, tea.Batch(scheduleNoticeClearCmd(m.noticeSeq), FetchIntegrationsCmd(m.api))

	case AnimationFrameMsg:
		if !m.advanceAnimations(msg.Now) {
			m.animationTicking = false
			return m, nil
		}
		return m, animationTickCmd()

	case progress.FrameMsg:
		defaultModel, defaultCmd := m.defaultProgress.Update(msg)
		m.defaultProgress = defaultModel.(progress.Model)

		shortModel, shortCmd := m.shortProgress.Update(msg)
		m.shortProgress = shortModel.(progress.Model)

		return m, tea.Batch(defaultCmd, shortCmd)

	case AutoRefreshTickMsg:
		return m.handleAutoRefreshTick(msg.Now)
	}

	return m, nil
}

func filterProviderAccounts(accounts []management.Account, provider string) []management.Account {
	if len(accounts) == 0 {
		return accounts
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = defaultProviderFilter
	}
	out := make([]management.Account, 0, len(accounts))
	for _, a := range accounts {
		if strings.ToLower(a.Provider) == provider {
			out = append(out, a)
		}
	}
	return out
}

func normalizeProviderFilter(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, candidate := range authProviders {
		if provider == candidate {
			return provider
		}
	}
	return defaultProviderFilter
}

func (m Model) uiStateSnapshot() UIState {
	return UIState{
		CompactMode:      m.CompactMode,
		ActiveAccountKey: m.activeAccountKey(),
		ProviderFilter:   m.ProviderFilter,
		StatsRange:       m.StatsRange,
	}
}
