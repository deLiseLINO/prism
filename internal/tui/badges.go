package tui

import "prism/internal/management"

func (m Model) renderAccountBadge(account management.Account) string {
	selected := m.PinnedAccounts[account.Provider]
	if selected != account.ID {
		if selected != "" {
			return ""
		}
		if m.providerAccountCount(account.Provider) != 1 {
			return ""
		}
	}
	return SourceBadgeBracketStyle.Render("[") + SourceCodexBadgeActiveStyle.Render("*") + SourceBadgeBracketStyle.Render("]")
}

func (m Model) providerAccountCount(provider string) int {
	count := 0
	for _, account := range m.Accounts {
		if account.Provider == provider {
			count++
		}
	}
	return count
}
