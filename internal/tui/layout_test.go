package tui

import (
	"strings"
	"testing"
)

func lipglossWidth(value string) int {
	return stringWidth(value)
}

func TestMessageModalWidthClamps(t *testing.T) {
	if got := messageModalWidth("Error", "boom", 200); got != messageModalMinWidth {
		t.Fatalf("width = %d, want %d", got, messageModalMinWidth)
	}
	if got := messageModalWidth("Error", "boom", 40); got != 32 {
		t.Fatalf("narrow width = %d, want 32", got)
	}
}

func TestSplitJoinFooterArea(t *testing.T) {
	view := "a\nb\nc"
	body, footer := splitFooterArea(view, 1)
	if body != "a\nb" || footer != "c" {
		t.Fatalf("body = %q footer = %q", body, footer)
	}
	if got := joinFooterArea(body, footer); got != view {
		t.Fatalf("joined = %q", got)
	}
}

func TestOverlayCenterContainsModal(t *testing.T) {
	base := strings.Repeat("x\n", 10)
	modal := renderMessageModal("Title", "body", InfoTitleStyle, 80)
	combined := overlayCenter(base, modal, 80, 12)
	if !strings.Contains(combined, "Title") {
		t.Fatal("modal title missing from overlay")
	}
}
