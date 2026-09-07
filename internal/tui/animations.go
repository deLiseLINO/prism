package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type compactBarAnimation struct {
	From      float64
	To        float64
	Current   float64
	StartedAt time.Time
	Duration  time.Duration
}

type tabWindowAnimation struct {
	From      float64
	To        float64
	Current   float64
	StartedAt time.Time
	Duration  time.Duration
}

const (
	animationFrameInterval       = 16 * time.Millisecond
	unifiedAnimationDuration     = 1000 * time.Millisecond
	compactLoadAnimationDuration = unifiedAnimationDuration
	tabLoadAnimationDuration     = unifiedAnimationDuration
	tabSwitchAnimationDuration   = unifiedAnimationDuration
)

func animationTickCmd() tea.Cmd {
	return tea.Tick(animationFrameInterval, func(now time.Time) tea.Msg {
		return AnimationFrameMsg{Now: now}
	})
}

func (m *Model) ensureAnimationTickCmd() tea.Cmd {
	if !m.hasActiveAnimations() {
		m.animationTicking = false
		return nil
	}
	if m.animationTicking {
		return nil
	}
	m.animationTicking = true
	return animationTickCmd()
}

func (m *Model) startCompactBarAnimation(accountKey string, prevWindow quotaWindow, hadPrevData bool, nextWindow quotaWindow, wasLoading bool) {
	if accountKey == "" {
		return
	}
	if !nextWindow.HasPercent {
		delete(m.compactBarAnimations, accountKey)
		return
	}

	target := clampRatio(nextWindow.LeftPercent / 100)
	from := 0.0
	if hadPrevData && prevWindow.HasPercent {
		from = clampRatio(prevWindow.LeftPercent / 100)
	}
	if !hadPrevData || wasLoading {
		from = 0
	}
	if from == target {
		delete(m.compactBarAnimations, accountKey)
		return
	}

	if m.compactBarAnimations == nil {
		m.compactBarAnimations = make(map[string]compactBarAnimation)
	}
	m.compactBarAnimations[accountKey] = compactBarAnimation{
		From:      from,
		To:        target,
		Current:   from,
		StartedAt: time.Now(),
		Duration:  compactLoadAnimationDuration,
	}
}

func (m *Model) advanceCompactBarAnimations(now time.Time) bool {
	if len(m.compactBarAnimations) == 0 {
		return false
	}
	for key, anim := range m.compactBarAnimations {
		if anim.Duration <= 0 {
			delete(m.compactBarAnimations, key)
			continue
		}
		elapsed := now.Sub(anim.StartedAt)
		if elapsed <= 0 {
			continue
		}
		progress := float64(elapsed) / float64(anim.Duration)
		if progress >= 1 {
			delete(m.compactBarAnimations, key)
			continue
		}
		if progress < 0 {
			progress = 0
		}
		eased := 1 - (1-progress)*(1-progress)
		anim.Current = anim.From + (anim.To-anim.From)*eased
		m.compactBarAnimations[key] = anim
	}
	return len(m.compactBarAnimations) > 0
}

func (m Model) compactBarRatio(accountKey string, fallback float64) float64 {
	anim, ok := m.compactBarAnimations[accountKey]
	if !ok {
		return fallback
	}
	return anim.Current
}

func (m *Model) pruneCompactBarAnimations() {
	if len(m.compactBarAnimations) == 0 {
		return
	}
	valid := make(map[string]struct{}, len(m.Accounts))
	for i := range m.Accounts {
		if m.Accounts[i].ID != "" {
			valid[m.Accounts[i].ID] = struct{}{}
		}
	}
	for key := range m.compactBarAnimations {
		if _, ok := valid[key]; !ok {
			delete(m.compactBarAnimations, key)
		}
	}
}

func (m *Model) clearCompactBarAnimations() {
	if len(m.compactBarAnimations) == 0 {
		m.animationTicking = false
		return
	}
	for key := range m.compactBarAnimations {
		delete(m.compactBarAnimations, key)
	}
	m.animationTicking = false
}

func (m *Model) clearTabWindowAnimations() {
	if len(m.tabWindowAnimations) == 0 {
		m.animationTicking = false
		return
	}
	for key := range m.tabWindowAnimations {
		delete(m.tabWindowAnimations, key)
	}
	m.animationTicking = false
}

func tabWindowKey(accountKey string, window quotaWindow) string {
	identity := window.Label
	if window.WindowSec != 0 {
		identity = fmt.Sprintf("%d", window.WindowSec)
	}
	return accountKey + "|" + identity
}

func (m *Model) startTabWindowAnimations(accountKey string, prevWindows []quotaWindow, hadPrevData bool, nextWindows []quotaWindow, wasLoading bool, duration time.Duration) {
	if accountKey == "" {
		return
	}
	m.clearTabWindowAnimations()
	prevByKey := make(map[string]quotaWindow, len(prevWindows))
	for _, w := range prevWindows {
		prevByKey[tabWindowKey(accountKey, w)] = w
	}
	for _, nextWindow := range nextWindows {
		if !nextWindow.HasPercent {
			continue
		}
		target := clampRatio(nextWindow.LeftPercent / 100)
		from := 0.0
		if prev, ok := prevByKey[tabWindowKey(accountKey, nextWindow)]; ok && hadPrevData && !wasLoading && prev.HasPercent {
			from = clampRatio(prev.LeftPercent / 100)
		}
		if from == target {
			continue
		}
		if m.tabWindowAnimations == nil {
			m.tabWindowAnimations = make(map[string]tabWindowAnimation)
		}
		m.tabWindowAnimations[tabWindowKey(accountKey, nextWindow)] = tabWindowAnimation{
			From:      from,
			To:        target,
			Current:   from,
			StartedAt: time.Now(),
			Duration:  duration,
		}
	}
}

func (m *Model) startTabWindowAnimationsFromZero(accountKey string, nextWindows []quotaWindow, duration time.Duration) {
	if accountKey == "" {
		return
	}
	m.clearTabWindowAnimations()
	for _, nextWindow := range nextWindows {
		if !nextWindow.HasPercent {
			continue
		}
		target := clampRatio(nextWindow.LeftPercent / 100)
		if target == 0 {
			continue
		}
		if m.tabWindowAnimations == nil {
			m.tabWindowAnimations = make(map[string]tabWindowAnimation)
		}
		m.tabWindowAnimations[tabWindowKey(accountKey, nextWindow)] = tabWindowAnimation{
			From:      0,
			To:        target,
			Current:  0,
			StartedAt: time.Now(),
			Duration:  duration,
		}
	}
}

func (m *Model) advanceTabWindowAnimations(now time.Time) bool {
	if len(m.tabWindowAnimations) == 0 {
		return false
	}
	for key, anim := range m.tabWindowAnimations {
		if anim.Duration <= 0 {
			delete(m.tabWindowAnimations, key)
			continue
		}
		elapsed := now.Sub(anim.StartedAt)
		if elapsed <= 0 {
			continue
		}
		progress := float64(elapsed) / float64(anim.Duration)
		if progress >= 1 {
			delete(m.tabWindowAnimations, key)
			continue
		}
		if progress < 0 {
			progress = 0
		}
		eased := 1 - (1-progress)*(1-progress)
		anim.Current = anim.From + (anim.To-anim.From)*eased
		m.tabWindowAnimations[key] = anim
	}
	return len(m.tabWindowAnimations) > 0
}

func (m *Model) advanceAnimations(now time.Time) bool {
	if m.CompactMode {
		return m.advanceCompactBarAnimations(now)
	}
	return m.advanceTabWindowAnimations(now)
}

func (m *Model) hasActiveAnimations() bool {
	if m.CompactMode {
		return len(m.compactBarAnimations) > 0
	}
	return len(m.tabWindowAnimations) > 0
}

func (m Model) tabWindowRatio(accountKey string, window quotaWindow, fallback float64) float64 {
	if accountKey == "" || len(m.tabWindowAnimations) == 0 {
		return fallback
	}
	anim, ok := m.tabWindowAnimations[tabWindowKey(accountKey, window)]
	if !ok {
		return fallback
	}
	return anim.Current
}
