package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deLiseLINO/prism/internal/management"
)

func pressKeys(t *testing.T, m Model, keys ...tea.KeyMsg) Model {
	t.Helper()
	for _, k := range keys {
		next, cmd := m.Update(k)
		m = next.(Model)
		if cmd != nil && m.Rename != nil && m.Rename.Busy {
			next, _ = m.Update(cmd())
			m = next.(Model)
		}
	}
	return m
}

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestRenameOverlayPreviewsThenRenames(t *testing.T) {
	account := management.Account{ID: "codex:default", Provider: "codex", State: "active"}
	client := newFakeClient(account)
	client.generation = 7
	client.providers = []management.Provider{{ID: "gw"}, {ID: "codex"}}
	client.renameReply = func(id string, w management.ProviderRenameWrite) (management.ProviderRenameResponse, *management.RenameConflictBody) {
		if w.To == "codex" {
			return management.ProviderRenameResponse{}, &management.RenameConflictBody{Conflicts: []management.RenameIssue{{Kind: "provider_exists", Detail: `provider "codex" already exists`}}}
		}
		return management.ProviderRenameResponse{Generation: 7, From: id, To: w.To, DryRun: w.DryRun, Plan: management.RenamePlan{ConfigRefs: 3, Accounts: []string{"gw:default"}, UsageAttempts: 5}}, nil
	}
	m := testModel(client, account)

	m = pressKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter}, runes("m"))
	if m.Rename == nil || len(m.Rename.Providers) != 2 {
		t.Fatalf("rename overlay = %+v", m.Rename)
	}
	if !strings.Contains(m.renderRenameModal(), "codex") {
		t.Fatal("picker does not list providers")
	}
	m = pressKeys(t, m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter})
	if m.Rename.From != "gw" {
		t.Fatalf("picked %q, want gw", m.Rename.From)
	}

	m = pressKeys(t, m, runes("codex"), tea.KeyMsg{Type: tea.KeyEnter})
	if view := m.renderRenameModal(); !strings.Contains(view, "already exists") || !strings.Contains(view, "[enter] Check") {
		t.Fatalf("conflict view:\n%s", view)
	}
	for range len("codex") {
		m = pressKeys(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = pressKeys(t, m, runes("ed/ge"), tea.KeyMsg{Type: tea.KeyEnter})
	if m.Rename.Draft != "edge" {
		t.Fatalf("draft = %q: characters outside the id grammar must be ignored", m.Rename.Draft)
	}
	if view := m.renderRenameModal(); !strings.Contains(view, "Usage history: 5 attempts") || !strings.Contains(view, "[enter] Rename") {
		t.Fatalf("preview view:\n%s", view)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.Rename != nil || m.Notice != "renamed gw -> edge" {
		t.Fatalf("after rename: overlay=%+v notice=%q", m.Rename, m.Notice)
	}
	if len(client.renames) != 3 || !client.renames[0].DryRun || !client.renames[1].DryRun || client.renames[2].DryRun || client.renames[2].ExpectedGeneration != 7 {
		t.Fatalf("requests = %+v", client.renames)
	}
}
