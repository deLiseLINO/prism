package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	messageModalMinWidth = 64
	messageModalMaxWidth = 104
	messageModalInset    = 6
)

func (m Model) currentOverlayModal() string {
	if m.SettingsVisible {
		return m.renderSettingsModal()
	}
	if m.HelpVisible {
		return m.renderHelpModal()
	}
	if m.ProviderSelectVisible {
		return m.renderProviderSelectModal()
	}
	if m.AuthLoginVisible {
		return m.renderAuthLoginModal()
	}
	if m.IntegrationsVisible {
		if m.IntegrationConfirm != "" {
			return m.renderIntegrationConfirmModal()
		}
		return m.renderIntegrationsModal()
	}
	if m.ActionMenuVisible {
		return m.renderActionMenuModal()
	}
	if m.StatsVisible {
		return m.renderStatsModal()
	}
	if m.ShowInfo {
		return m.renderInfoModal()
	}
	if m.DeleteConfirm {
		return m.renderDeleteConfirmModal()
	}
	if m.PinConfirm {
		return m.renderPinConfirmModal()
	}
	if m.Err != nil {
		return m.renderErrorModal()
	}
	if m.Notice != "" {
		return renderMessageModal("Notice", m.Notice, NoticeStyle, m.Width)
	}
	if m.activeAccount() == nil {
		return renderMessageModal("No accounts", "No accounts loaded.\nPress n to add account.", WarningStyle, m.Width)
	}
	return ""
}

func (m Model) renderErrorModal() string {
	message := strings.TrimSpace(errText(m.Err))
	if message == "" {
		message = "Unknown error"
	}
	hint := "[enter/esc] Close"
	width := messageModalWidth("Error", message+"\n"+hint, m.Width)
	bodyWidth := width - 2
	if bodyWidth < 1 {
		bodyWidth = 1
	}
	wrappedMessage := lipgloss.NewStyle().Width(bodyWidth).Render(message)
	content := strings.Join([]string{
		ErrorStyle.Render("Error"),
		InfoValueStyle.Render(wrappedMessage),
		ActionMenuHintStyle.Render(hint),
	}, "\n\n")
	return InfoBoxStyle.Copy().Width(width).Render(content)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (m Model) renderDeleteConfirmModal() string {
	account := m.activeAccount()
	accountID := "n/a"
	if account != nil {
		accountID = account.ID
	}
	message := fmt.Sprintf("Delete account %s?\nThis removes it from the prism pool.\n[enter] Confirm   [esc] Cancel", accountID)
	return renderMessageModal("Delete account", message, WarningStyle, m.Width)
}

func (m Model) renderPinConfirmModal() string {
	accountID := "n/a"
	provider := m.ProviderFilter
	if account := m.activeAccount(); account != nil {
		accountID = account.ID
		provider = account.Provider
	}
	message := fmt.Sprintf("Pin %s to %s?\n[enter] Pinned   [a] Auto (clear pin)   [esc] Cancel", provider, accountID)
	return renderMessageModal("Pin account", message, WarningStyle, m.Width)
}

func (m Model) renderInfoModal() string {
	account := m.activeAccount()

	accountID := "n/a"
	email := "n/a"
	provider := "n/a"
	state := "n/a"
	priority := "n/a"
	inFlight := "n/a"
	cooldown := "n/a"
	if account != nil {
		accountID = account.ID
		provider = account.Provider
		state = account.State
		if account.Email != "" {
			email = account.Email
		}
		priority = fmt.Sprintf("%d", account.Priority)
		inFlight = fmt.Sprintf("%d", account.InFlight)
		if account.CooldownUntil != nil {
			cooldown = formatResetText(*account.CooldownUntil)
		}
	}

	windows, hasWindow := m.UsageData[m.activeAccountKey()]
	window := quotaWindow{}
	window.Used = -1
	window.Limit = nil
	if hasWindow {
		window = primaryQuotaWindow(windows)
	}
	used := "n/a"
	limit := "n/a"
	reset := "n/a"
	if hasWindow && window.HasPercent {
		if window.Used >= 0 {
			used = fmt.Sprintf("%d", window.Used)
		}
		if window.Limit != nil {
			limit = fmt.Sprintf("%d", *window.Limit)
		}
		if !window.ResetAt.IsZero() {
			reset = formatResetText(window.ResetAt)
		}
	}

	lines := []string{
		InfoTitleStyle.Render("Additional info"),
		fmt.Sprintf("%s %s", InfoKeyStyle.Render("email:"), InfoValueStyle.Render(email)),
		fmt.Sprintf("%s %s", InfoKeyStyle.Render("account_id:"), InfoValueStyle.Render(accountID)),
		fmt.Sprintf("%s %s", InfoKeyStyle.Render("provider:"), InfoValueStyle.Render(provider)),
		fmt.Sprintf("%s %s", InfoKeyStyle.Render("state:"), InfoValueStyle.Render(state)),
		fmt.Sprintf("%s %s", InfoKeyStyle.Render("priority:"), InfoValueStyle.Render(priority)),
		fmt.Sprintf("%s %s", InfoKeyStyle.Render("in_flight:"), InfoValueStyle.Render(inFlight)),
		fmt.Sprintf("%s %s", InfoKeyStyle.Render("cooldown_until:"), InfoValueStyle.Render(cooldown)),
		fmt.Sprintf("%s %s", InfoKeyStyle.Render("used:"), InfoValueStyle.Render(used)),
		fmt.Sprintf("%s %s", InfoKeyStyle.Render("limit:"), InfoValueStyle.Render(limit)),
		fmt.Sprintf("%s %s", InfoKeyStyle.Render("reset:"), InfoValueStyle.Render(reset)),
	}

	content := strings.Join(lines, "\n")
	return InfoBoxStyle.Copy().Width(60).Render(content)
}

func (m Model) renderHelpModal() string {
	primaryMove := "←/→"
	if m.CompactMode {
		primaryMove = "↑/↓"
	}

	lines := []string{
		InfoTitleStyle.Render("Keyboard help"),
		"",
		HelpSectionStyle.Render("Account actions"),
		renderHelpLine("Enter", "Open account menu"),
		renderHelpLine("r", "Refresh active account"),
		renderHelpLine("R", "Refresh all accounts"),
		renderHelpLine("p", "Pause or resume account"),
		renderHelpLine("s", "Pin account provider"),
		renderHelpLine("n", "Add account"),
		renderHelpLine("x", "Delete account"),
		renderHelpLine("i", "Account info"),
		"",
		HelpSectionStyle.Render("Other"),
		renderHelpLine(primaryMove, "Move between accounts"),
		renderHelpLine("v / c", "Toggle view mode"),
		renderHelpLine("P", "Switch provider"),
		renderHelpLine("o", "Integrations"),
		renderHelpLine("u", "Usage stats"),
		renderHelpLine(",", "Settings"),
		renderHelpLine("?", "Open or close this help"),
		renderHelpLine("q", "Quit"),
		"",
	}
	return InfoBoxStyle.Copy().Width(modalWidthForLines(lines, 56)).Render(strings.Join(lines, "\n"))
}

func modalWidthForLines(lines []string, minimum int) int {
	width := minimum
	for _, line := range lines {
		if candidate := ansi.StringWidth(line) + 2; candidate > width {
			width = candidate
		}
	}
	return width
}

func renderHelpLine(key, description string) string {
	return fmt.Sprintf("%s %s", HelpKeyStyle.Render(fmt.Sprintf("%-10s", key)), InfoValueStyle.Render(description))
}

func (m Model) renderActionMenuModal() string {
	sections := m.actionMenuSections()
	lines := []string{
		ActionMenuTitleStyle.Render("Account actions"),
	}

	if account := m.activeAccount(); account != nil {
		lines = append(lines, InfoValueStyle.Render(truncateLabel(account.ID, 44)))
	}
	lines = append(lines, "")

	labelWidth := actionMenuLabelWidth(sections)
	index := 0
	for sectionIx, section := range sections {
		if strings.TrimSpace(section.Title) != "" {
			lines = append(lines, HelpSectionStyle.Render(section.Title))
		}
		for _, item := range section.Items {
			cursor := " "
			style := ActionMenuItemStyle
			if index == m.ActionMenuCursor {
				cursor = ">"
				style = ActionMenuSelectedStyle
			}
			line := fmt.Sprintf("%s %d. %-*s %s", cursor, index+1, labelWidth, item.Label, item.Shortcut)
			lines = append(lines, style.Render(line))
			index++
		}
		if sectionIx < len(sections)-1 {
			lines = append(lines, "")
		}
	}

	lines = append(lines, "")
	lines = append(lines, ActionMenuHintStyle.Render("[↑/↓] Move   [enter] Select   [esc] Close"))

	return InfoBoxStyle.Copy().Width(actionMenuModalWidth(lines)).Render(strings.Join(lines, "\n"))
}

func (m Model) renderProviderSelectModal() string {
	title := "Add account"
	prompt := "Select provider to authorize:"
	if m.ProviderSelectMode == providerSelectModeSwitch {
		title = "Switch provider"
		prompt = "Select provider to show:"
	}
	lines := []string{
		InfoTitleStyle.Render(title),
		"",
		InfoValueStyle.Render(prompt),
		"",
	}
	for i, provider := range authProviders {
		cursor := " "
		if i == m.ProviderCursor {
			cursor = ">"
		}
		lines = append(lines, InfoValueStyle.Render(fmt.Sprintf("%s %d. %s", cursor, i+1, provider)))
	}
	lines = append(lines, "")
	lines = append(lines, ActionMenuHintStyle.Render("[↑/↓] Move   [enter] Select   [esc] Cancel"))
	return InfoBoxStyle.Copy().Width(modalWidthForLines(lines, 56)).Render(strings.Join(lines, "\n"))
}

func (m Model) renderAuthLoginModal() string {
	lines := []string{
		InfoTitleStyle.Render("Connect account"),
		"",
		InfoValueStyle.Render("Complete authorization in your browser. This window will close automatically after login."),
		"",
		InfoValueStyle.Render("If your browser did not open, open this URL manually:"),
	}

	bodyWidth := 78
	if m.Width > 0 && m.Width < 96 {
		bodyWidth = m.Width - 18
	}
	if bodyWidth < 24 {
		bodyWidth = 24
	}
	renderedURL := lipgloss.NewStyle().
		Width(bodyWidth).
		Foreground(lipgloss.Color("39")).
		Render(displayAuthURL(m.AuthLoginURL))
	lines = append(lines, renderedURL)
	lines = append(lines, "")
	if m.AuthBrowserFailed {
		lines = append(lines, NoticeStyle.Render("Browser did not open automatically."))
		lines = append(lines, "")
	}
	lines = append(lines, InfoValueStyle.Render("Waiting for authorization..."))
	if strings.TrimSpace(m.AuthLoginStatus) != "" {
		lines = append(lines, NoticeStyle.Render(m.AuthLoginStatus))
	}
	lines = append(lines, "")
	lines = append(lines, ActionMenuHintStyle.Render("[c] Copy   [esc] Cancel"))

	width := 84
	for _, line := range lines {
		if w := lipgloss.Width(line) + 2; w > width {
			width = w
		}
	}
	if width > 96 {
		width = 96
	}
	return InfoBoxStyle.Copy().Width(width).Render(strings.Join(lines, "\n"))
}

func (m Model) renderIntegrationsModal() string {
	lines := []string{
		InfoTitleStyle.Render("Integrations"),
		"",
	}
	if len(m.Integrations) == 0 {
		lines = append(lines, InfoValueStyle.Render("No integrations available."))
	} else {
		for i, item := range m.Integrations {
			cursor := " "
			if i == m.IntegrationsCursor {
				cursor = ">"
			}
			state := "not installed"
			if item.Installed {
				state = "installed"
				if item.Managed {
					state = "managed"
				}
				if item.Drift {
					state += " (drift)"
				}
			}
			line := fmt.Sprintf("%s %d. %-*s %s", cursor, i+1, 12, item.ID, state)
			lines = append(lines, InfoValueStyle.Render(line))
		}
	}
	lines = append(lines, "")
	lines = append(lines, ActionMenuHintStyle.Render("[↑/↓] Move   [enter] Apply   [o/esc] Close"))
	return InfoBoxStyle.Copy().Width(modalWidthForLines(lines, 56)).Render(strings.Join(lines, "\n"))
}

func (m Model) renderIntegrationConfirmModal() string {
	message := fmt.Sprintf("Apply prism integration to %s?\n[enter] Confirm   [esc] Cancel", m.IntegrationConfirm)
	return renderMessageModal("Apply integration", message, WarningStyle, m.Width)
}

func displayAuthURL(value string) string {
	replacer := strings.NewReplacer(
		"://", ":\u200b//",
		"/", "/\u200b",
		"?", "?\u200b",
		"&", "&\u200b",
		"=", "=\u200b",
	)
	return replacer.Replace(strings.TrimSpace(value))
}
