package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m Model) renderWindowsView() string {
	windows, ok := m.UsageData[m.activeAccountKey()]
	if !ok {
		return "No quota data.\n"
	}

	var s strings.Builder
	for i, window := range windows {
		if i > 0 {
			s.WriteString("\n")
		}
		s.WriteString(m.renderWindowHeader(window))
		s.WriteString("\n")
		s.WriteString(m.renderWindowRow(window))
		s.WriteString("\n")
	}
	return s.String()
}

func (m Model) renderWindowsLoadingSkeleton() string {
	windows := []quotaWindow{
		{Label: "5 hour usage limit", WindowSec: windowSecShort},
		{Label: "Weekly usage limit", WindowSec: windowSecWeekly},
	}
	var s strings.Builder
	for i, window := range windows {
		if i > 0 {
			s.WriteString("\n")
		}
		s.WriteString(m.renderWindowHeader(window))
		s.WriteString("\n")
		s.WriteString(m.renderWindowStatusRow(window, "Loading..."))
		s.WriteString("\n")
	}
	return s.String()
}

func (m Model) renderWindowHeader(window quotaWindow) string {
	header := windowHeader(window)
	nameWidth, barWidth, _, _ := m.windowRowLayout(window.WindowSec)
	rowWidth := m.windowRowDisplayWidth(window.WindowSec)
	leadOffset := m.windowLeadOffset(window.WindowSec)
	barStart := ansi.StringWidth(windowRowIndent) + nameWidth + 1
	start := barStart + (barWidth-ansi.StringWidth(header))/2
	if start < 0 {
		start = 0
	}
	if rowWidth > 0 && start > rowWidth {
		start = rowWidth
	}
	rightPad := 0
	if rowWidth > 0 {
		rightPad = rowWidth - start - ansi.StringWidth(header)
		if rightPad < 0 {
			rightPad = 0
		}
	}
	headerStyle := GroupHeaderStyle.Copy().MarginTop(0)
	return strings.Repeat(" ", leadOffset+start) + headerStyle.Render(header) + strings.Repeat(" ", rightPad)
}

func windowHeader(window quotaWindow) string {
	if window.WindowSec == windowSecShort {
		return "5 hour"
	}
	if window.WindowSec == windowSecWeekly {
		return "Weekly"
	}
	return window.Label
}

func (m Model) windowRowDisplayWidth(windowSec int64) int {
	nameWidth, barWidth, percentWidth, resetWidth := m.windowRowLayout(windowSec)
	const (
		gapsWidth       = 2
		resetMarginLeft = 2
	)
	return ansi.StringWidth(windowRowIndent) + nameWidth + barWidth + percentWidth + resetWidth + gapsWidth + resetMarginLeft
}

func (m Model) windowLeadOffset(windowSec int64) int {
	nameWidth, barWidth, _, _ := m.windowRowLayout(windowSec)
	rowWidth := m.windowRowDisplayWidth(windowSec)
	currentBarCenter := ansi.StringWidth(windowRowIndent) + nameWidth + 1 + (barWidth / 2)

	// lipgloss centers each rendered line; keep bar at the visual center by
	// making bar center match the center of this row line.
	offset := rowWidth - (2 * currentBarCenter)
	offset += 4
	if offset <= 0 {
		return 0
	}

	maxOffset := m.preferredContentWidth() - rowWidth
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	return offset
}

func (m Model) renderWindowRow(window quotaWindow) string {
	var s strings.Builder

	ratio := clampRatio(window.LeftPercent / 100)
	ratio = m.tabWindowRatio(m.activeAccountKey(), window, ratio)

	nameWidth, barWidth, percentWidth, resetWidth := m.windowRowLayout(window.WindowSec)
	leadOffset := m.windowLeadOffset(window.WindowSec)
	name := truncateLabel(window.Label, nameWidth)
	alignedName := padRight(name, nameWidth)
	percentText := fmt.Sprintf("%.0f%%", window.LeftPercent)
	if !window.HasPercent {
		percentText = "?"
	}
	if ansi.StringWidth(percentText) > percentWidth {
		percentText = truncateLabel(percentText, percentWidth)
	}
	resetText := truncateLabelFromLeft(formatResetText(window.ResetAt), resetWidth)

	s.WriteString(strings.Repeat(" ", leadOffset))
	s.WriteString(windowRowIndent)
	s.WriteString(LabelStyle.Render(alignedName))
	s.WriteString(" ")
	gradientStart, gradientEnd := barGradientForWindow(window.WindowSec)
	if window.HasPercent {
		s.WriteString(renderSmoothBar(barWidth, ratio, gradientStart, gradientEnd))
	} else {
		s.WriteString(renderSmoothBar(barWidth, 0, gradientStart, gradientEnd))
	}
	s.WriteString(" ")
	s.WriteString(PercentStyle.Copy().Width(percentWidth).Render(percentText))
	if resetWidth > 0 && strings.TrimSpace(resetText) != "" {
		s.WriteString(ResetTimeStyle.Copy().Width(resetWidth).Render(resetText))
	}

	return s.String()
}

func (m Model) renderWindowStatusRow(window quotaWindow, status string) string {
	var s strings.Builder
	nameWidth, barWidth, percentWidth, resetWidth := m.windowRowLayout(window.WindowSec)
	leadOffset := m.windowLeadOffset(window.WindowSec)
	name := truncateLabel(window.Label, nameWidth)
	alignedName := padRight(name, nameWidth)
	status = truncateLabelStrict(status, resetWidth)
	gradientStart, gradientEnd := barGradientForWindow(window.WindowSec)

	s.WriteString(strings.Repeat(" ", leadOffset))
	s.WriteString(windowRowIndent)
	s.WriteString(LabelStyle.Render(alignedName))
	s.WriteString(" ")
	s.WriteString(renderSmoothBar(barWidth, 0, gradientStart, gradientEnd))
	s.WriteString(" ")
	s.WriteString(PercentStyle.Copy().Width(percentWidth).Render("..."))
	if resetWidth > 0 && strings.TrimSpace(status) != "" {
		s.WriteString(ResetTimeStyle.Copy().Width(resetWidth).Render(status))
	}
	return s.String()
}

func (m Model) windowRowLayout(windowSec int64) (nameWidth, barWidth, percentWidth, resetWidth int) {
	nameWidth = 22
	barWidth = m.barWidthForWindow(windowSec)
	percentWidth = 5
	resetWidth = 26

	if m.Width <= 0 {
		return
	}

	const (
		minNameWidth      = 6
		minNameSoftWidth  = 8
		minBarWidth       = 8
		minBarSoftWidth   = 10
		minPercentWidth   = 4
		minResetWidth     = 0
		minResetSoftWidth = 8
		gapsWidth         = 2
		resetMarginLeft   = 2
	)

	available := m.preferredContentWidth() - ansi.StringWidth(windowRowIndent)
	if available <= 0 {
		return
	}
	// Keep a small horizontal reserve on narrow widths so leadOffset can
	// still compensate and keep the bar/header near visual center.
	switch contentWidth := m.preferredContentWidth(); {
	case contentWidth <= 104 && available > 24:
		available -= 8
	case contentWidth <= 120 && available > 24:
		available -= 4
	}

	used := nameWidth + barWidth + percentWidth + resetWidth + gapsWidth + resetMarginLeft
	shortage := used - available
	if shortage <= 0 {
		return
	}

	reduce := func(current, minimum int) int {
		if shortage <= 0 {
			return current
		}
		canReduce := current - minimum
		if canReduce <= 0 {
			return current
		}
		if canReduce > shortage {
			canReduce = shortage
		}
		shortage -= canReduce
		return current - canReduce
	}

	reduceBalanced := func(left, leftMin, right, rightMin int) (int, int) {
		for shortage > 0 {
			progressed := false
			if left > leftMin {
				left--
				shortage--
				progressed = true
			}
			if shortage > 0 && right > rightMin {
				right--
				shortage--
				progressed = true
			}
			if !progressed {
				break
			}
		}
		return left, right
	}

	nameWidth, resetWidth = reduceBalanced(nameWidth, minNameSoftWidth, resetWidth, minResetSoftWidth)
	barWidth = reduce(barWidth, minBarSoftWidth)
	percentWidth = reduce(percentWidth, minPercentWidth)
	nameWidth, resetWidth = reduceBalanced(nameWidth, minNameWidth, resetWidth, minResetWidth)
	barWidth = reduce(barWidth, minBarWidth)
	return
}

func padRight(value string, width int) string {
	if width <= 0 {
		return value
	}
	current := ansi.StringWidth(value)
	if current >= width {
		return value
	}
	return value + strings.Repeat(" ", width-current)
}

func truncateLabelStrict(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	return truncateLabel(value, limit)
}
