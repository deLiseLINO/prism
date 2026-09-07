package tui

import "github.com/charmbracelet/x/ansi"

func stringWidth(value string) int {
	return ansi.StringWidth(value)
}
