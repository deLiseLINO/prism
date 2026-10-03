package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/deLiseLINO/prism/internal/buildinfo"
	"github.com/deLiseLINO/prism/internal/update"
)

const (
	updatePromptChoiceNow = iota
	updatePromptChoiceSkip
	updatePromptChoiceDismiss
	updatePromptChoiceCount

	updatePromptModalMinWidth = 72
	updatePromptModalMaxWidth = 104
	updatePromptViewportInset = 6
	updatePromptBorderWidth   = 2

	updateHintText       = "Update available • press U"
	headerUpdateHintGap  = 6
	updateHintMinPadding = 2
)

type pendingUpdate struct {
	Method  update.Method
	Version string
}

func (m Model) WithStartupUpdate(version string, method update.Method, stateDir string) Model {
	m.updateStateDir = stateDir
	if version = strings.TrimSpace(version); version != "" && update.SupportsAutoUpdate(method) {
		m.UpdatePromptVersion = version
		m.UpdatePromptMethod = method
		m.UpdatePromptVisible = true
		m.UpdatePromptCursor = updatePromptChoiceNow
	}
	return m
}

func (m Model) PendingUpdate() (update.Method, string, bool) {
	if m.pendingUpdate == nil {
		return update.MethodUnknown, "", false
	}
	return m.pendingUpdate.Method, m.pendingUpdate.Version, true
}

func (m Model) updateAvailable() bool {
	return strings.TrimSpace(m.UpdatePromptVersion) != "" && update.SupportsAutoUpdate(m.UpdatePromptMethod)
}

func (m *Model) openUpdatePrompt() bool {
	if !m.updateAvailable() {
		return false
	}
	m.resetHelpState()
	m.resetActionMenuState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.resetStatsState()
	m.UpdatePromptVisible = true
	m.UpdatePromptCursor = updatePromptChoiceNow
	m.ShowInfo = false
	m.Notice = ""
	m.Err = nil
	return true
}

func (m Model) applyUpdateAvailable(msg UpdateAvailableMsg) Model {
	version := strings.TrimSpace(msg.Version)
	if version == "" || !update.SupportsAutoUpdate(msg.Method) {
		return m
	}
	m.UpdatePromptMethod = msg.Method
	if m.UpdatePromptVersion == "" || update.IsNewer(version, m.UpdatePromptVersion) {
		m.UpdatePromptVersion = version
	}
	if !m.UpdatePromptVisible {
		m.UpdateAvailableHint = updateHintText
	}
	return m
}

func DismissUpdateVersionCmd(stateDir, version string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(stateDir) != "" {
			_ = update.DismissVersion(stateDir, version)
		}
		return nil
	}
}

func (m Model) handleUpdatePrompt(keyStr string) (tea.Model, tea.Cmd) {
	switch keyStr {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.UpdatePromptCursor = (m.UpdatePromptCursor - 1 + updatePromptChoiceCount) % updatePromptChoiceCount
		return m, nil
	case "down", "j":
		m.UpdatePromptCursor = (m.UpdatePromptCursor + 1) % updatePromptChoiceCount
		return m, nil
	case "1":
		m.UpdatePromptCursor = updatePromptChoiceNow
		return m.confirmUpdatePrompt()
	case "2", "esc":
		m.UpdatePromptCursor = updatePromptChoiceSkip
		return m.confirmUpdatePrompt()
	case "3":
		m.UpdatePromptCursor = updatePromptChoiceDismiss
		return m.confirmUpdatePrompt()
	case "enter":
		return m.confirmUpdatePrompt()
	}
	return m, nil
}

func (m Model) confirmUpdatePrompt() (tea.Model, tea.Cmd) {
	m.UpdatePromptVisible = false
	switch m.UpdatePromptCursor {
	case updatePromptChoiceNow:
		m.UpdateAvailableHint = ""
		m.pendingUpdate = &pendingUpdate{Method: m.UpdatePromptMethod, Version: m.UpdatePromptVersion}
		return m, tea.Quit
	case updatePromptChoiceDismiss:
		m.UpdateAvailableHint = ""
		return m, DismissUpdateVersionCmd(m.updateStateDir, m.UpdatePromptVersion)
	default:
		if m.updateAvailable() {
			m.UpdateAvailableHint = updateHintText
		}
		return m, nil
	}
}

func (m Model) renderUpdatePromptModal() string {
	command := update.CommandString(m.UpdatePromptMethod, m.UpdatePromptVersion)
	lines := []string{
		UpdateHintStyle.Render("Update available"),
		InfoValueStyle.Render(fmt.Sprintf("%s -> %s", buildinfo.Version, m.UpdatePromptVersion)),
		"",
		InfoValueStyle.Render("Release notes: " + update.ReleaseNotesURL(m.UpdatePromptVersion)),
		"",
		renderUpdatePromptOption(1, "Update now", command, m.UpdatePromptCursor == updatePromptChoiceNow),
		renderUpdatePromptOption(2, "Skip", "", m.UpdatePromptCursor == updatePromptChoiceSkip),
		renderUpdatePromptOption(3, "Skip until next version", "", m.UpdatePromptCursor == updatePromptChoiceDismiss),
		"",
		ActionMenuHintStyle.Render("[↑/↓] Move   [enter] Select   [esc] Skip"),
	}
	return InfoBoxStyle.Copy().Width(m.updatePromptModalWidth(lines)).Render(strings.Join(lines, "\n"))
}

func renderUpdatePromptOption(index int, label, command string, selected bool) string {
	cursor := " "
	if selected {
		cursor = ">"
	}
	if strings.TrimSpace(command) != "" {
		label = fmt.Sprintf("%s (runs `%s`)", label, command)
	}
	return InfoValueStyle.Render(fmt.Sprintf("%s %d. %s", cursor, index, label))
}

func (m Model) updatePromptModalWidth(lines []string) int {
	target := modalWidthForLines(lines, updatePromptModalMinWidth)
	if target > updatePromptModalMaxWidth {
		target = updatePromptModalMaxWidth
	}
	if m.Width <= 0 {
		return target
	}
	maxAllowed := m.Width - updatePromptViewportInset - updatePromptBorderWidth
	if maxAllowed <= 0 {
		return updatePromptModalMinWidth
	}
	if target > maxAllowed {
		return maxAllowed
	}
	return target
}

func (m Model) overlayUpdateHint(base string) string {
	hint := strings.TrimSpace(m.UpdateAvailableHint)
	if hint == "" {
		return base
	}
	lines := strings.Split(base, "\n")
	canvasWidth := 0
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > canvasWidth {
			canvasWidth = width
		}
	}
	titleIdx := firstNonEmptyLine(lines)
	if canvasWidth == 0 || titleIdx < 0 {
		return base
	}

	hintRendered := UpdateHintStyle.Render(hint)
	hintWidth := ansi.StringWidth(hintRendered)
	if hintWidth+updateHintMinPadding > canvasWidth {
		return base
	}

	for _, idx := range []int{titleIdx, titleIdx + 1} {
		if idx >= len(lines) {
			continue
		}
		rightEdge := lineRightEdge(lines[idx])
		startX := canvasWidth - hintWidth
		if idx == titleIdx {
			startX = rightEdge + headerUpdateHintGap
		}
		if startX+hintWidth > canvasWidth || startX < rightEdge+updateHintMinPadding {
			continue
		}
		line := padANSI(lines[idx], canvasWidth)
		left := ansi.Cut(line, 0, startX)
		right := ansi.Cut(line, startX+hintWidth, canvasWidth)
		lines[idx] = left + hintRendered + right
		return strings.Join(lines, "\n")
	}
	return base
}

func firstNonEmptyLine(lines []string) int {
	for i, line := range lines {
		if strings.TrimSpace(ansi.Strip(line)) != "" {
			return i
		}
	}
	return -1
}

func lineRightEdge(line string) int {
	plain := ansi.Strip(line)
	return ansi.StringWidth(strings.TrimRight(plain, " "))
}
