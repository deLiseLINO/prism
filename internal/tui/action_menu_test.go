package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"prism/internal/management"
)

func TestActionMenuSectionsIncludePauseResume(t *testing.T) {
	m := testModel(nil, management.Account{ID: "acc-1", State: "active"})
	items := m.actionMenuItems()

	foundPause := false
	for _, item := range items {
		if item.ID == actionMenuPause {
			foundPause = true
		}
		if item.ID == actionMenuResume {
			t.Fatal("active account offered resume")
		}
	}
	if !foundPause {
		t.Fatal("active account missing pause")
	}
}

func TestActionMenuSectionsPausedAccount(t *testing.T) {
	m := testModel(nil, management.Account{ID: "acc-1", State: "paused"})
	items := m.actionMenuItems()

	foundResume := false
	for _, item := range items {
		if item.ID == actionMenuResume {
			foundResume = true
		}
		if item.ID == actionMenuPause {
			t.Fatal("paused account offered pause")
		}
	}
	if !foundResume {
		t.Fatal("paused account missing resume")
	}
}

func TestActionMenuNavigation(t *testing.T) {
	m := testModel(nil, management.Account{ID: "acc-1"})
	m.openActionMenu()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated := next.(Model)
	if updated.ActionMenuCursor != 1 {
		t.Fatalf("cursor = %d, want 1", updated.ActionMenuCursor)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	if next.(Model).ActionMenuCursor != 0 {
		t.Fatalf("cursor = %d, want 0", next.(Model).ActionMenuCursor)
	}
}

func TestActionMenuShortcutJumps(t *testing.T) {
	m := testModel(nil, management.Account{ID: "acc-1"})
	m.openActionMenu()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	updated := next.(Model)
	if !updated.DeleteConfirm {
		t.Fatal("delete shortcut did not open confirm")
	}
}

func TestActionMenuEscCloses(t *testing.T) {
	m := testModel(nil, management.Account{ID: "acc-1"})
	m.openActionMenu()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(Model)
	if updated.ActionMenuVisible {
		t.Fatal("menu still visible after esc")
	}
}

func TestActionMenuPinItemOnlyForCodex(t *testing.T) {
	m := testModel(nil, management.Account{ID: "codex:main", Provider: "codex", State: "active"})
	items := m.actionMenuItems()

	foundPin := false
	for _, item := range items {
		if item.ID == actionMenuPin {
			foundPin = true
		}
	}
	if !foundPin {
		t.Fatal("codex account missing pin item")
	}

	m = testModel(nil, management.Account{ID: "ag:main", Provider: "antigravity", State: "active"})
	for _, item := range m.actionMenuItems() {
		if item.ID == actionMenuPin {
			t.Fatal("non-codex account offered pin")
		}
	}
}

func TestPinFlowConfirmsAndWritesProvider(t *testing.T) {
	client := newFakeClient(management.Account{ID: "codex:default", Provider: "codex", State: "active"})
	client.providers = []management.Provider{{ID: "codex", Wire: "responses", Models: []string{"gpt-5"}}}
	m := testModel(client, management.Account{ID: "codex:default", Provider: "codex", State: "active"})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	updated := next.(Model)
	if !updated.PinConfirm {
		t.Fatal("pin hotkey did not open confirm")
	}

	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(Model)
	if updated.PinConfirm {
		t.Fatal("pin confirm still visible")
	}
	if cmd == nil {
		t.Fatal("pin confirm did not schedule write")
	}
	msg := cmd()
	accountsMsg, ok := msg.(AccountsMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if accountsMsg.Notice != "codex pinned to codex:default" {
		t.Fatalf("notice = %q", accountsMsg.Notice)
	}
	if client.replacedID != "codex" {
		t.Fatalf("replacedID = %q", client.replacedID)
	}
	if client.replacedBody.Pool == nil || client.replacedBody.Pool.PinnedAccount != "codex:default" {
		t.Fatalf("pool = %+v", client.replacedBody.Pool)
	}
	if client.replacedBody.ExpectedGeneration != client.generation {
		t.Fatalf("expectedGeneration = %d", client.replacedBody.ExpectedGeneration)
	}
	if len(client.replacedBody.Models) != 1 || client.replacedBody.Models[0] != "gpt-5" {
		t.Fatalf("models = %+v", client.replacedBody.Models)
	}
	if client.replacedBody.Credential != "" {
		t.Fatal("credential must not be copied")
	}
}

func TestPinFlowAutoClearsPin(t *testing.T) {
	client := newFakeClient(management.Account{ID: "codex:default", Provider: "codex", State: "active"})
	client.providers = []management.Provider{{ID: "codex", Wire: "responses"}}
	m := testModel(client, management.Account{ID: "codex:default", Provider: "codex", State: "active"})
	m.beginPinFlow()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	updated := next.(Model)
	if updated.PinConfirm {
		t.Fatal("pin confirm still visible")
	}
	if cmd == nil {
		t.Fatal("auto did not schedule write")
	}
	msg := cmd()
	accountsMsg, ok := msg.(AccountsMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if accountsMsg.Notice != "codex pin cleared" {
		t.Fatalf("notice = %q", accountsMsg.Notice)
	}
	if client.replacedBody.Pool == nil || client.replacedBody.Pool.PinnedAccount != "" {
		t.Fatalf("pool = %+v", client.replacedBody.Pool)
	}
}

func TestPinFlowSkipsNonCodexAccount(t *testing.T) {
	m := testModel(nil, management.Account{ID: "ag:default", Provider: "antigravity", State: "active"})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	updated := next.(Model)
	if updated.PinConfirm {
		t.Fatal("non-codex account opened pin confirm")
	}
}

func TestSettingsHotkeyIsComma(t *testing.T) {
	m := testModel(nil, management.Account{ID: "codex:default", Provider: "codex"})
	m.beginPinFlow()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{','}})
	updated := next.(Model)
	if !updated.SettingsVisible {
		t.Fatal("comma did not open settings")
	}
	if updated.PinConfirm {
		t.Fatal("comma opened pin confirm")
	}
}
