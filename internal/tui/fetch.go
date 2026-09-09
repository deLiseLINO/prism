package tui

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"prism/internal/config"
	"prism/internal/integrations"
	"prism/internal/management"
)

const maxConcurrentLoads = 3

func (m Model) fetchAccountCmd(accountKey string) tea.Cmd {
	if accountKey == "" || m.api == nil {
		return nil
	}
	client := m.api

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		quota, err := client.AccountQuota(ctx, accountKey)
		if err != nil {
			return ErrMsg{AccountKey: accountKey, Err: err}
		}
		return DataMsg{AccountKey: accountKey, Windows: quotaWindowsFromView(quota.Quota)}
	}
}

func (m Model) fetchNextCmd() tea.Cmd {
	if m.UsageData == nil {
		m.UsageData = make(map[string][]quotaWindow)
	}
	if m.LoadingMap == nil {
		m.LoadingMap = make(map[string]bool)
	}
	if m.ErrorsMap == nil {
		m.ErrorsMap = make(map[string]error)
	}

	currentlyLoading := 0
	for _, isLoading := range m.LoadingMap {
		if isLoading {
			currentlyLoading++
		}
	}
	if currentlyLoading >= maxConcurrentLoads {
		return nil
	}
	availableSlots := maxConcurrentLoads - currentlyLoading

	checkAccount := func(accountKey string) tea.Cmd {
		if accountKey == "" {
			return nil
		}
		if m.LoadingMap[accountKey] {
			return nil
		}
		_, hasData := m.UsageData[accountKey]
		_, hasErr := m.ErrorsMap[accountKey]

		scheduled := m.refreshScheduled[accountKey]
		if scheduled {
			delete(m.refreshScheduled, accountKey)
			m.LoadingMap[accountKey] = true
			m.silentRefresh[accountKey] = true
			return m.fetchAccountCmd(accountKey)
		}
		if !hasData && !hasErr {
			m.LoadingMap[accountKey] = true
			return m.fetchAccountCmd(accountKey)
		}
		return nil
	}

	cmds := make([]tea.Cmd, 0, availableSlots)
	orderedAccounts := m.Accounts
	if m.CompactMode {
		orderedAccounts = make([]management.Account, 0, len(m.Accounts))
		for _, idx := range m.compactVisualOrderIndices() {
			if idx < 0 || idx >= len(m.Accounts) {
				continue
			}
			orderedAccounts = append(orderedAccounts, m.Accounts[idx])
		}
	}

	for i := range orderedAccounts {
		if cmd := checkAccount(orderedAccounts[i].ID); cmd != nil {
			cmds = append(cmds, cmd)
			availableSlots--
			if availableSlots == 0 {
				return tea.Batch(cmds...)
			}
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func ReloadAccountsCmd(client Client, activeKey string) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		accounts, err := client.Accounts(ctx)
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to reload accounts: %w", err)}
		}
		return AccountsMsg{ActiveKey: activeKey, Accounts: accounts.Accounts}
	}
}

func reloadAccountsNoticeCmd(client Client, notice string) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		accounts, err := client.Accounts(ctx)
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to reload accounts: %w", err)}
		}
		return AccountsMsg{Accounts: accounts.Accounts, Notice: notice}
	}
}

func PauseAccountCmd(client Client, accountKey string, version uint64) tea.Cmd {
	if client == nil {
		return nil
	}
	return mutateAccountCmd(client, accountKey, version, client.PauseAccount)
}

func ResumeAccountCmd(client Client, accountKey string, version uint64) tea.Cmd {
	if client == nil || accountKey == "" {
		return nil
	}
	return mutateAccountCmd(client, accountKey, version, client.ResumeAccount)
}

func mutateAccountCmd(client Client, accountKey string, version uint64, mutate func(context.Context, string, uint64) (management.Account, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if _, err := mutate(ctx, accountKey, version); err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to update account: %w", err)}
		}

		accounts, err := client.Accounts(ctx)
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to reload accounts: %w", err)}
		}
		return AccountsMsg{ActiveKey: accountKey, Accounts: accounts.Accounts, Notice: "account updated"}
	}
}

func DeleteAccountCmd(client Client, accountKey string) tea.Cmd {
	if client == nil || accountKey == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := client.DeleteAccount(ctx, accountKey); err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to delete account: %w", err)}
		}

		accounts, err := client.Accounts(ctx)
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to reload accounts: %w", err)}
		}
		return AccountsMsg{Accounts: accounts.Accounts, Notice: "account deleted"}
	}
}

func StartAuthCmd(client Client, provider string) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		start, err := client.AuthStart(ctx, provider)
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("login failed: %w", err)}
		}
		return AuthStartedMsg{Provider: provider, Session: start.Session, AuthURL: start.URL}
	}
}

func PollAuthCmd(client Client, provider, session string) tea.Cmd {
	if client == nil {
		return nil
	}
	return tea.Tick(300*time.Millisecond, func(_ time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		status, err := client.AuthStatus(ctx, provider, session)
		if err != nil {
			return AuthFinishedMsg{Provider: provider, Err: err}
		}
		switch status.State {
		case "complete", "authorized":
			return AuthFinishedMsg{Provider: provider}
		case "failed", "unauthorized":
			return AuthFinishedMsg{Provider: provider, Err: fmt.Errorf("authorization failed")}
		default:
			return AuthPendingMsg{}
		}
	})
}

func OpenAuthURLCmd(authURL string) tea.Cmd {
	authURL = strings.TrimSpace(authURL)
	if authURL == "" {
		return nil
	}
	return func() tea.Msg {
		if err := openBrowser(authURL); err != nil {
			return AuthBrowserOpenResultMsg{Err: fmt.Errorf("failed to open browser: %w", err)}
		}
		return AuthBrowserOpenResultMsg{Text: "Opened authorization URL in browser."}
	}
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return execCommand("open", url)
	case "windows":
		return execCommand("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if err := execCommand("xdg-open", url); err == nil {
			return nil
		}
		return fmt.Errorf("xdg-open failed")
	}
}

func CopyToClipboardCmd(text string) tea.Cmd {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return func() tea.Msg {
		var name string
		var args []string
		switch runtime.GOOS {
		case "darwin":
			name = "pbcopy"
		case "windows":
			name = "cmd"
			args = []string{"/c", "clip"}
		default:
			if _, err := lookPath("wl-copy"); err == nil {
				name = "wl-copy"
			} else if _, err := lookPath("xclip"); err == nil {
				name = "xclip"
				args = []string{"-selection", "clipboard"}
			} else if _, err := lookPath("xsel"); err == nil {
				name = "xsel"
				args = []string{"--clipboard", "--input"}
			} else {
				return AuthCopyResultMsg{Err: fmt.Errorf("no clipboard command found")}
			}
		}
		if err := runWithStdin(name, args, text); err != nil {
			return AuthCopyResultMsg{Err: fmt.Errorf("failed to copy URL: %w", err)}
		}
		return AuthCopyResultMsg{Text: "Copied URL to clipboard."}
	}
}

func FetchIntegrationsCmd(client Client) tea.Cmd {
	return fetchHostIntegrationsCmd(client, managementHostLocal)
}

func FetchHostIntegrationsCmd(client Client, host string) tea.Cmd {
	return fetchHostIntegrationsCmd(client, host)
}

func fetchHostIntegrationsCmd(client Client, host string) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		var (
			items []integrations.Status
			err   error
		)
		if host == "" || host == managementHostLocal {
			items, err = client.IntegrationsList(ctx)
		} else {
			items, err = client.HostIntegrationsList(ctx, host)
		}
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to load integrations: %w", err)}
		}
		return IntegrationsMsg{Host: host, Integrations: integrationStatusesFromList(items)}
	}
}

func FetchHostsCmd(client Client) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		hosts, err := client.HostsList(ctx)
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to load hosts: %w", err)}
		}
		return HostsMsg{Hosts: hostsFromList(hosts.Hosts)}
	}
}

func ApplyIntegrationCmd(client Client, id string) tea.Cmd {
	return ApplyHostIntegrationCmd(client, managementHostLocal, id)
}

func ApplyHostIntegrationCmd(client Client, host, id string) tea.Cmd {
	if client == nil || id == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		var (
			result integrations.ApplyResult
			err    error
		)
		if host == "" || host == managementHostLocal {
			result, err = client.IntegrationApply(ctx, id)
		} else {
			result, err = client.HostIntegrationApply(ctx, host, id)
		}
		if err != nil {
			return IntegrationApplyResultMsg{ID: id, OK: false, Err: fmt.Errorf("failed to apply integration: %w", err)}
		}
		return IntegrationApplyResultMsg{ID: id, OK: result.OK, Reason: result.Reason}
	}
}

func FetchPinnedAccountsCmd(client Client) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		list, err := client.ProvidersList(ctx)
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to load pinned accounts: %w", err)}
		}
		pinned := make(map[string]string)
		for _, provider := range list.Providers {
			if provider.Pool != nil && provider.Pool.PinnedAccount != "" {
				pinned[provider.ID] = provider.Pool.PinnedAccount
			}
		}
		return PinnedAccountsMsg{Pinned: pinned}
	}
}

func PinProviderAccountCmd(client Client, providerID, accountKey string) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		list, err := client.ProvidersList(ctx)
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to load providers: %w", err)}
		}

		providerID = strings.ToLower(strings.TrimSpace(providerID))
		var provider management.Provider
		found := false
		for _, candidate := range list.Providers {
			if strings.EqualFold(candidate.ID, providerID) {
				provider = candidate
				found = true
				break
			}
		}
		if !found {
			return ErrMsg{Err: fmt.Errorf("provider %s not found", providerID)}
		}

		pool := defaultProviderPool()
		if provider.Pool != nil {
			pool = *provider.Pool
		}
		pool.PinnedAccount = accountKey

		write := providerWriteFrom(provider)
		write.Pool = &pool
		write.ExpectedGeneration = list.Generation

		if _, err := client.ProvidersReplace(ctx, provider.ID, write); err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to update provider: %w", err)}
		}

		accounts, err := client.Accounts(ctx)
		if err != nil {
			return ErrMsg{Err: fmt.Errorf("failed to reload accounts: %w", err)}
		}
		notice := providerID + " pin cleared"
		if accountKey != "" {
			notice = providerID + " pinned to " + accountKey
		}
		return tea.Batch(
			func() tea.Msg {
				return AccountsMsg{ActiveKey: accountKey, Accounts: accounts.Accounts, Notice: notice}
			},
			FetchPinnedAccountsCmd(client),
		)()
	}
}

func defaultProviderPool() config.PoolSettings {
	return config.PoolSettings{
		Strategy:        config.PoolQuota,
		Affinity:        config.AffinitySticky,
		MaxFailovers:    3,
		CooldownDefault: 300_000_000_000,
		CooldownMax:     900_000_000_000,
		ProbeEvery:      60_000_000_000,
	}
}

func providerWriteFrom(provider management.Provider) management.ProviderWrite {
	write := management.ProviderWrite{
		ID:             provider.ID,
		Wire:           provider.Wire,
		Models:         provider.Models,
		DisabledModels: provider.DisabledModels,
		Enabled:        provider.Enabled,
	}
	if provider.BaseURL != "" {
		write.BaseURL = &provider.BaseURL
	}
	if provider.DefaultModel != "" {
		write.DefaultModel = &provider.DefaultModel
	}
	return write
}

func scheduleNoticeClearCmd(seq int) tea.Cmd {
	return tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg {
		return NoticeTimeoutMsg{Seq: seq}
	})
}

func SaveUIStateSnapshotCmd(state UIState) tea.Cmd {
	return func() tea.Msg {
		_ = SaveUIState(state)
		return nil
	}
}

func SaveSettingsCmd(settings Settings) tea.Cmd {
	return func() tea.Msg {
		_ = SaveSettings(settings)
		return nil
	}
}
