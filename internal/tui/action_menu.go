package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type actionMenuItem struct {
	ID       string
	Label    string
	Shortcut string
}

type actionMenuSection struct {
	Title string
	Items []actionMenuItem
}

func (m Model) actionMenuSections() []actionMenuSection {
	currentItems := []actionMenuItem{
		{ID: actionMenuRefresh, Label: "Refresh quota", Shortcut: "r"},
	}
	if account := m.activeAccount(); account != nil && strings.ToLower(account.State) == "paused" {
		currentItems = append(currentItems, actionMenuItem{ID: actionMenuResume, Label: "Resume account", Shortcut: "p"})
	} else {
		currentItems = append(currentItems, actionMenuItem{ID: actionMenuPause, Label: "Pause account", Shortcut: "p"})
	}
	account := m.activeAccount()
	if account != nil {
		currentItems = append(currentItems,
			actionMenuItem{ID: actionMenuPin, Label: "Pin account (switch)", Shortcut: "s"},
			actionMenuItem{ID: actionMenuInfo, Label: "Account details", Shortcut: "i"},
			actionMenuItem{ID: actionMenuDelete, Label: "Delete account", Shortcut: "x"},
		)
	}

	return []actionMenuSection{
		{
			Title: "Current account",
			Items: currentItems,
		},
		{
			Title: "Global actions",
			Items: []actionMenuItem{
				{ID: actionMenuRefreshAll, Label: "Refresh all", Shortcut: "R"},
				{ID: actionMenuAdd, Label: "Add account", Shortcut: "n"},
				{ID: actionMenuIntegrations, Label: "Integrations", Shortcut: "o"},
				{ID: actionMenuProvider, Label: "Switch provider", Shortcut: "P"},
				{ID: actionMenuView, Label: "Switch view", Shortcut: "v"},
				{ID: actionMenuSettings, Label: "Settings", Shortcut: ","},
				{ID: actionMenuHelp, Label: "Help", Shortcut: "?"},
			},
		},
	}
}

func (m Model) actionMenuItems() []actionMenuItem {
	sections := m.actionMenuSections()
	total := 0
	for _, section := range sections {
		total += len(section.Items)
	}
	items := make([]actionMenuItem, 0, total)
	for _, section := range sections {
		items = append(items, section.Items...)
	}
	return items
}

func actionMenuLabelWidth(sections []actionMenuSection) int {
	width := 0
	for _, section := range sections {
		for _, item := range section.Items {
			if w := ansi.StringWidth(item.Label); w > width {
				width = w
			}
		}
	}
	return width
}

func actionMenuModalWidth(lines []string) int {
	width := 56
	for _, line := range lines {
		if w := ansi.StringWidth(line) + 2; w > width {
			width = w
		}
	}
	return width
}
