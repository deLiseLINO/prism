package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m Model) renderWindowsView() string {
	windows, ok := m.UsageData[m.activeAccountKey()]
	if !ok {
		return "No quota data.\n"
	}

	groups := groupQuotaWindows(windows)
	if len(groups) == 0 {
		return "No quota data.\n"
	}

	var s strings.Builder
	for gi, group := range groups {
		if gi > 0 {
			s.WriteString("\n")
		}
		if group.Title != "" {
			s.WriteString(m.renderGroupHeader(group.Title))
			s.WriteString("\n")
		}
		for wi, window := range group.Windows {
			if wi > 0 {
				s.WriteString("\n")
			}
			s.WriteString(m.renderWindowRow(window))
			s.WriteString("\n")
		}
	}
	return s.String()
}

type quotaWindowGroup struct {
	Title   string
	Windows []quotaWindow
}

func groupQuotaWindows(windows []quotaWindow) []quotaWindowGroup {
	var groups []quotaWindowGroup
	index := map[string]int{}
	for _, w := range windows {
		title := windowGroupTitle(w.Label)
		if title == "" && len(windows) > 0 {
			title = defaultGroupTitle
		}
		gi, ok := index[title]
		if !ok {
			gi = len(groups)
			index[title] = gi
			groups = append(groups, quotaWindowGroup{Title: title})
		}
		groups[gi].Windows = append(groups[gi].Windows, w)
	}
	for i := range groups {
		groups[i].Windows = sortWindowRows(groups[i].Windows)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return windowGroupOrder(groups[i].Title) < windowGroupOrder(groups[j].Title)
	})
	return groups
}

func windowGroupOrder(title string) int {
	switch title {
	case "Gemini Models":
		return 0
	case "Claude and GPT models":
		return 1
	}
	return 2
}

const defaultGroupTitle = ""

func windowGroupTitle(label string) string {
	switch {
	case strings.Contains(label, "Gemini"):
		return "Gemini Models"
	case strings.Contains(label, "Claude"):
		return "Claude and GPT models"
	}
	return defaultGroupTitle
}

func sortWindowRows(windows []quotaWindow) []quotaWindow {
	out := append([]quotaWindow(nil), windows...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if windowRowOrder(out[j]) < windowRowOrder(out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func windowRowOrder(w quotaWindow) int {
	if w.WindowSec == windowSecShort {
		return 0
	}
	if w.WindowSec == windowSecWeekly {
		return 1
	}
	return 2
}

func (m Model) renderGroupHeader(title string) string {
	_, barWidth, _, _ := m.windowRowLayout(0)
	rowWidth := m.windowRowDisplayWidth(0)
	leadOffset := m.windowLeadOffset(0)
	barStart := ansi.StringWidth(windowRowIndent) + 22 + 1
	start := barStart + (barWidth-ansi.StringWidth(title))/2
	if start < 0 {
		start = 0
	}
	if rowWidth > 0 && start > rowWidth {
		start = rowWidth
	}
	rightPad := 0
	if rowWidth > 0 {
		rightPad = rowWidth - start - ansi.StringWidth(title)
		if rightPad < 0 {
			rightPad = 0
		}
	}
	headerStyle := groupHeaderStyle(title).MarginTop(0)
	return strings.Repeat(" ", leadOffset+start) + headerStyle.Render(title) + strings.Repeat(" ", rightPad)
}

func groupHeaderStyle(title string) lipgloss.Style {
	if title == "Claude and GPT models" {
		return ClaudeGroupHeaderStyle
	}
	return GeminiGroupHeaderStyle
}

func (m Model) renderWindowsLoadingSkeleton() string {
	var s strings.Builder
	for _, title := range []string{"Gemini Models", "Claude and GPT models"} {
		s.WriteString(m.renderGroupHeader(title))
		s.WriteString("\n")
		s.WriteString(m.renderWindowStatusRow(quotaWindow{WindowSec: windowSecShort}, "Loading..."))
		s.WriteString("\n")
		s.WriteString(m.renderWindowStatusRow(quotaWindow{WindowSec: windowSecWeekly}, "Loading..."))
		s.WriteString("\n")
	}
	return s.String()
}

func windowRowLabel(window quotaWindow) string {
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
	name := truncateLabel(windowRowLabel(window), nameWidth)
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
	name := truncateLabel(windowRowLabel(window), nameWidth)
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
