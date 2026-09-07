package tui

import "github.com/charmbracelet/lipgloss"

var (
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginBottom(1)

	TabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Underline(true).
			Foreground(lipgloss.Color("255"))

	TabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240"))

	GroupHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("39")).
				MarginTop(1)

	LabelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	PercentStyle = lipgloss.NewStyle().
			Width(8).
			Align(lipgloss.Right).
			Foreground(lipgloss.Color("170"))

	ResetTimeStyle = lipgloss.NewStyle().
			Width(26).
			Align(lipgloss.Left).
			Foreground(lipgloss.Color("241")).
			MarginLeft(2)

	HelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			MarginTop(1)

	HelpKeyStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39"))

	HelpSectionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("252"))

	ActionMenuTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("39"))

	ActionMenuSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("255"))

	ActionMenuItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))

	ActionMenuHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244"))

	ErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	WarningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	NoticeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))

	InfoTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39"))

	InfoKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))

	InfoValueStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	InfoBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)

	SourceBadgeBracketStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240"))
	SourceCodexBadgeActiveStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("205"))
	SourceCodexBadgeMutedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("176"))

	CompactExhaustedHeaderStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color("251"))
	BarEmptyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("238"))
)
