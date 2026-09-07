package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) View() string {
	var s strings.Builder
	modal := m.currentOverlayModal()

	s.WriteString(m.renderHeader())
	s.WriteString("\n")

	if len(m.Accounts) > 0 {
		if !m.CompactMode {
			s.WriteString(m.renderAccountTabs())
			s.WriteString("\n\n")
		} else {
			s.WriteString("\n")
		}
	}

	if m.CompactMode {
		s.WriteString(m.renderCompactView())
	} else {
		if m.Loading {
			s.WriteString(m.renderWindowsLoadingSkeleton())
		} else if m.activeAccount() != nil {
			s.WriteString(m.renderWindowsView())
		} else {
			s.WriteString("\n")
		}
	}

	footer := HelpStyle.Render("\n" + m.renderFooter())
	s.WriteString(footer)

	content := s.String()
	containerStyle := lipgloss.NewStyle().Padding(1, 2)
	hAlign := lipgloss.Left
	vAlign := lipgloss.Top
	if m.Width > 0 {
		containerStyle = containerStyle.Width(m.Width)
		hAlign = lipgloss.Center
	}
	if m.Height > 0 {
		containerStyle = containerStyle.Height(m.Height)
		vAlign = lipgloss.Center
	}
	containerStyle = containerStyle.Align(hAlign, vAlign)

	baseView := containerStyle.Render(content)

	if modal != "" {
		body, footerArea := splitFooterArea(baseView, lipgloss.Height(footer))
		return joinFooterArea(overlayCenter(body, modal, m.Width, m.Height-lipgloss.Height(footer)), footerArea)
	}

	return baseView
}

func (m Model) preferredContentWidth() int {
	if m.Width <= 0 {
		return 0
	}
	if m.Width <= 12 {
		return m.Width
	}
	usable := m.Width - 4
	const maxContentWidth = 220
	if usable > maxContentWidth {
		return maxContentWidth
	}
	return usable
}

func (m Model) renderHeader() string {
	return TitleStyle.Render("Prism")
}

func (m Model) renderFooter() string {
	if m.CompactMode {
		return "↑↓ Move • Enter Menu • ? Help • q Quit"
	}
	return "←→ Move • Enter Menu • ? Help • q Quit"
}
