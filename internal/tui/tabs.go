package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"prism/internal/management"
)

func (m Model) renderAccountTabs() string {
	accounts := m.Accounts
	activeIndex := m.ActiveAccountIx
	width := m.Width

	if len(accounts) == 0 {
		return ""
	}

	maxVisible := 3
	switch {
	case width < 60:
		maxVisible = 1
	case width < 78:
		maxVisible = 2
	}

	baseLabelLimit := m.tabLabelLimit()
	for maxVisible >= 1 {
		start, end := tabVisibleRange(len(accounts), activeIndex, maxVisible)
		for labelLimit := baseLabelLimit; labelLimit >= 6; labelLimit-- {
			tabs := m.renderTabsRange(accounts, start, end, activeIndex, labelLimit)
			rendered := strings.Join(tabs, " • ")
			if m.tabsFitsWidth(rendered) || (maxVisible == 1 && labelLimit == 6) {
				return rendered
			}
		}
		maxVisible--
	}

	return ""
}

func tabVisibleRange(total, activeIndex, maxVisible int) (start, end int) {
	start = 0
	end = total
	if total <= maxVisible {
		return start, end
	}

	half := maxVisible / 2
	start = activeIndex - half
	if start < 0 {
		start = 0
	}
	end = start + maxVisible
	if end > total {
		end = total
		start = end - maxVisible
	}
	return start, end
}

func (m Model) renderTabsRange(accounts []management.Account, start, end, activeIndex, labelLimit int) []string {
	tabs := make([]string, 0, (end-start)+2)
	if start > 0 {
		tabs = append(tabs, TabInactiveStyle.Render("..."))
	}

	for i := start; i < end; i++ {
		account := accounts[i]
		label := accountLabel(account)
		badge := renderProviderBadge(account.Provider, i == activeIndex)
		badgeWidth := ansi.StringWidth(badge)
		if badgeWidth > 0 {
			limit := labelLimit - (badgeWidth + 1)
			if limit < 4 {
				limit = 4
			}
			label = truncateLabel(label, limit)
		} else {
			label = truncateLabel(label, labelLimit)
		}
		labelText := TabInactiveStyle.Render(label)
		if i == activeIndex {
			labelText = TabActiveStyle.Render(label)
		}

		if badgeWidth > 0 {
			tabs = append(tabs, badge+" "+labelText)
			continue
		}
		tabs = append(tabs, labelText)
	}

	if end < len(accounts) {
		tabs = append(tabs, TabInactiveStyle.Render("..."))
	}

	return tabs
}

func (m Model) tabsFitsWidth(rendered string) bool {
	if m.Width <= 0 {
		return true
	}
	available := m.preferredContentWidth() - 2
	if available < 12 {
		available = m.Width
	}
	return ansi.StringWidth(rendered) <= available
}

func (m Model) tabLabelLimit() int {
	width := m.Width
	switch {
	case width >= 180:
		return 28
	case width >= 150:
		return 24
	case width >= 120:
		return 20
	case width >= 100:
		return 16
	case width >= 80:
		return 12
	default:
		return 8
	}
}

func truncateLabel(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func accountLabel(account management.Account) string {
	if email := strings.TrimSpace(account.Email); email != "" {
		return email
	}
	return shortAccountID(account.ID)
}

func shortAccountID(accountID string) string {
	trimmed := strings.TrimSpace(accountID)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 12 {
		return trimmed
	}
	return trimmed[:6] + "..." + trimmed[len(trimmed)-4:]
}

func truncateLabelFromLeft(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return "…" + string(runes[len(runes)-(limit-1):])
}
