package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"prism/internal/management"
)

var statsRanges = []string{"1h", "24h", "7d", "30d", "all"}

const (
	statsBarCapacity   = 20
	statsNamePad       = 14
	statsModelPad      = 30
	statsModalMinWidth = 72
	statsChromeLines   = 8
	statsTopModels     = 10
)

var (
	statsRangeActiveStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	statsRangeInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	statsBarFilledStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	statsBarFailedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	statsDimStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func statsRangeIndex(keyStr string) int {
	for i, r := range statsRanges {
		if keyStr == r {
			return i
		}
	}
	if len(keyStr) == 1 && keyStr[0] >= '1' && keyStr[0] <= '5' {
		return int(keyStr[0] - '1')
	}
	return -1
}

func statsRangePrev(statsRange string) string {
	for i, r := range statsRanges {
		if r == statsRange {
			return statsRanges[(i-1+len(statsRanges))%len(statsRanges)]
		}
	}
	return statsRanges[0]
}

func statsRangeNext(statsRange string) string {
	for i, r := range statsRanges {
		if r == statsRange {
			return statsRanges[(i+1)%len(statsRanges)]
		}
	}
	return statsRanges[0]
}

func (m Model) maxStatsScroll() int {
	available := m.Height - statsChromeLines
	if available <= 0 {
		return 0
	}
	if overflow := statsModalLines(m) - available; overflow > 0 {
		return overflow
	}
	return 0
}

func statsModalLines(m Model) int {
	return len(statsModalContentLines(m))
}

func (m Model) renderStatsModal() string {
	lines := statsModalContentLines(m)
	width := modalWidthForLines(lines, statsModalMinWidth)
	if maxScroll := m.maxStatsScroll(); maxScroll > 0 && m.StatsScroll > 0 {
		scroll := m.StatsScroll
		if scroll > maxScroll {
			scroll = maxScroll
		}
		visible := m.Height - statsChromeLines
		if scroll+visible > len(lines) {
			visible = len(lines) - scroll
		}
		if visible > 0 {
			lines = lines[scroll : scroll+visible]
		}
	}
	return InfoBoxStyle.Copy().Width(width).Render(strings.Join(lines, "\n"))
}

func statsModalContentLines(m Model) []string {
	lines := []string{
		InfoTitleStyle.Render("Stats"),
		statsDimStyle.Render("range: " + m.StatsRange),
		"",
		m.renderStatsRangeRow(),
		"",
	}

	if m.StatsLoading {
		lines = append(lines, statsDimStyle.Render("Loading stats…"))
	} else if m.StatsData == nil || statsEmpty(*m.StatsData) {
		lines = append(lines, statsDimStyle.Render("No stats yet."))
	} else {
		lines = append(lines, m.renderStatsOverview()...)
		lines = append(lines, m.renderStatsProviders()...)
		lines = append(lines, m.renderStatsModels()...)
	}

	lines = append(lines, "", ActionMenuHintStyle.Render("[←/→] Range   [r] Refresh   [u/esc] Close"))
	return lines
}

func statsEmpty(stats management.StatsResponse) bool {
	return stats.Overview.Requests == 0 && len(stats.Providers) == 0 && len(stats.Models) == 0
}

func (m Model) renderStatsRangeRow() string {
	parts := make([]string, 0, len(statsRanges))
	for i, r := range statsRanges {
		if r == m.StatsRange {
			parts = append(parts, statsRangeActiveStyle.Render(fmt.Sprintf("[%d:%s]", i+1, r)))
		} else {
			parts = append(parts, statsRangeInactiveStyle.Render(fmt.Sprintf("[%d:%s]", i+1, r)))
		}
	}
	return strings.Join(parts, " ")
}

func (m Model) renderStatsOverview() []string {
	overview := m.StatsData.Overview
	return []string{
		HelpSectionStyle.Render("Overview"),
		statsKV("requests:", fmt.Sprintf("%d", overview.Requests)),
		statsKV("completed:", fmt.Sprintf("%d", overview.Completed)),
		statsKV("failed:", fmt.Sprintf("%d", overview.Failed)),
		statsKV("tokens in:", fmt.Sprintf("%d", overview.InputTokens)),
		statsKV("tokens out:", fmt.Sprintf("%d", overview.OutputTokens)),
		statsKV("tokens cached:", fmt.Sprintf("%d", overview.CachedTokens)),
		statsKV("tokens total:", fmt.Sprintf("%d", overview.TotalTokens)),
		statsKV("measured:", measuredText(overview)),
	}
}

func statsKV(key, value string) string {
	return fmt.Sprintf("%s %s", InfoKeyStyle.Render(fmt.Sprintf("%-14s", key)), InfoValueStyle.Render(value))
}

func measuredText(overview management.StatsOverview) string {
	if overview.Requests <= 0 {
		return fmt.Sprintf("%d/%d (0%%)", overview.Measured, overview.Requests)
	}
	share := int64(math.Round(float64(overview.Measured) / float64(overview.Requests) * 100))
	return fmt.Sprintf("%d/%d (%d%%)", overview.Measured, overview.Requests, share)
}

func (m Model) renderStatsProviders() []string {
	providers := m.StatsData.Providers
	lines := []string{"", HelpSectionStyle.Render("Providers")}
	if len(providers) == 0 {
		return append(lines, statsDimStyle.Render("No provider stats."))
	}
	var maxTotal int64
	for _, p := range providers {
		if p.TotalTokens > maxTotal {
			maxTotal = p.TotalTokens
		}
	}
	for _, p := range providers {
		lines = append(lines, statsRow(p.Provider, statsNamePad, p.StatsOverview, p.TotalTokens, maxTotal))
	}
	return lines
}

func (m Model) renderStatsModels() []string {
	models := m.StatsData.Models
	lines := []string{"", HelpSectionStyle.Render("Models")}
	if len(models) == 0 {
		return append(lines, statsDimStyle.Render("No model stats."))
	}
	top := models
	if len(top) > statsTopModels {
		top = top[:statsTopModels]
	}
	var maxTotal int64
	for _, mdl := range top {
		if mdl.TotalTokens > maxTotal {
			maxTotal = mdl.TotalTokens
		}
	}
	for _, mdl := range top {
		name := mdl.Model
		if mdl.Provider != "" {
			name = fmt.Sprintf("%s %s", mdl.Model, statsDimStyle.Render(mdl.Provider))
		}
		lines = append(lines, statsRow(name, statsModelPad, mdl.StatsOverview, mdl.TotalTokens, maxTotal))
	}
	return lines
}

func statsRow(name string, namePad int, overview management.StatsOverview, total, maxTotal int64) string {
	counts := fmt.Sprintf("req %d · ok %d · fail %d · tot %d", overview.Requests, overview.Completed, overview.Failed, total)
	return fmt.Sprintf("%s %s %s",
		InfoValueStyle.Render(fmt.Sprintf("%-*s", namePad, name)),
		miniBar(total, maxTotal, overview.Failed == 0),
		statsDimStyle.Render(counts),
	)
}

func miniBar(total, maxTotal int64, healthy bool) string {
	if maxTotal <= 0 || total <= 0 {
		return BarEmptyStyle.Render(strings.Repeat("·", statsBarCapacity))
	}
	filled := int(math.Round(float64(statsBarCapacity) * float64(total) / float64(maxTotal)))
	if filled < 5 {
		filled = 5
	}
	if filled > statsBarCapacity {
		filled = statsBarCapacity
	}
	filledStyle := statsBarFilledStyle
	if !healthy {
		filledStyle = statsBarFailedStyle
	}
	return filledStyle.Render(strings.Repeat("█", filled)) + BarEmptyStyle.Render(strings.Repeat("░", statsBarCapacity-filled))
}
