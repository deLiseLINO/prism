package tui

import "strings"

func renderProviderBadge(provider string, isActive bool) string {
	if strings.ToLower(strings.TrimSpace(provider)) != "codex" {
		return ""
	}
	style := SourceCodexBadgeMutedStyle
	if isActive {
		style = SourceCodexBadgeActiveStyle
	}
	return SourceBadgeBracketStyle.Render("[") + style.Render("C") + SourceBadgeBracketStyle.Render("]")
}
