package tui

import "prism/internal/management"

func (m Model) renderAccountBadge(account management.Account) string {
	if m.PinnedAccounts[account.Provider] != account.ID {
		return ""
	}
	return SourceBadgeBracketStyle.Render("[") + SourceCodexBadgeActiveStyle.Render("P") + SourceBadgeBracketStyle.Render("]")
}
