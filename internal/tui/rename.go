package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deLiseLINO/prism/internal/management"
)

const actionMenuRename = "rename"

// renameState is the rename-provider overlay. It first picks a provider
// from Providers, then edits the new id; Checked is the draft the daemon
// last answered for.
type renameState struct {
	Providers []string
	Cursor    int
	From      string
	Draft     string
	Checked   string
	Preview   *management.ProviderRenameResponse
	Conflicts []management.RenameIssue
	Busy      bool
}

// RenameProvidersMsg carries the provider ids the rename overlay offers.
type RenameProvidersMsg struct {
	IDs []string
	Err error
}

// RenameResultMsg is the daemon's answer to a rename or its dry run.
type RenameResultMsg struct {
	From     string
	Draft    string
	DryRun   bool
	Response management.ProviderRenameResponse
	Conflict *management.RenameConflictBody
	Err      error
}

func (c *HTTPClient) RenameProvider(ctx context.Context, id string, w management.ProviderRenameWrite) (management.ProviderRenameResponse, *management.RenameConflictBody, error) {
	var out management.ProviderRenameResponse
	body, err := json.Marshal(w)
	if err != nil {
		return out, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/providers/"+urlPathEscape(id)+"/rename", bytes.NewReader(body))
	if err != nil {
		return out, nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return out, nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		var conflict management.RenameConflictBody
		if json.Unmarshal(raw, &conflict) == nil && conflict.Error.Code == "rename_conflict" {
			return out, &conflict, nil
		}
		resp.Body = io.NopCloser(bytes.NewReader(raw))
	}
	if resp.StatusCode >= 400 {
		return out, nil, errorFromBody(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return out, nil, nil
}

func renameProvidersCmd(client Client) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		list, err := client.ProvidersList(ctx)
		if err != nil {
			return RenameProvidersMsg{Err: err}
		}
		ids := make([]string, 0, len(list.Providers))
		for _, p := range list.Providers {
			ids = append(ids, p.ID)
		}
		slices.Sort(ids)
		return RenameProvidersMsg{IDs: ids}
	}
}

// renameProviderCmd reads the current generation and sends one rename or
// dry run.
func renameProviderCmd(client Client, from, to string, dryRun bool) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		msg := RenameResultMsg{From: from, Draft: to, DryRun: dryRun}
		list, err := client.ProvidersList(ctx)
		if err != nil {
			msg.Err = err
			return msg
		}
		msg.Response, msg.Conflict, msg.Err = client.RenameProvider(ctx, from, management.ProviderRenameWrite{To: to, ExpectedGeneration: list.Generation, DryRun: dryRun})
		return msg
	}
}

func (m Model) beginRenameFlow() (tea.Model, tea.Cmd) {
	m.resetActionMenuState()
	m.resetHelpState()
	m.resetDeleteState()
	m.resetIntegrationsState()
	m.ShowInfo = false
	m.Err = nil
	m.Notice = ""
	m.Rename = &renameState{Busy: true}
	return m, renameProvidersCmd(m.api)
}

func validRenameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
}

func (m Model) handleRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := *m.Rename
	switch msg.Type {
	case tea.KeyCtrlC:
		m.Rename = nil
		return m, tea.Quit
	case tea.KeyEsc:
		m.Rename = nil
		return m, nil
	}
	if st.From == "" {
		switch msg.Type {
		case tea.KeyUp:
			if st.Cursor > 0 {
				st.Cursor--
			}
		case tea.KeyDown:
			if st.Cursor < len(st.Providers)-1 {
				st.Cursor++
			}
		case tea.KeyEnter:
			if st.Busy || len(st.Providers) == 0 {
				return m, nil
			}
			st.From = st.Providers[st.Cursor]
		}
		m.Rename = &st
		return m, nil
	}
	switch msg.Type {
	case tea.KeyBackspace:
		if st.Draft != "" {
			st.Draft = st.Draft[:len(st.Draft)-1]
		}
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			if validRenameRune(r) {
				st.Draft += string(r)
			}
		}
	case tea.KeyEnter:
		if st.Busy || st.Draft == "" {
			return m, nil
		}
		confirmed := st.Checked == st.Draft && st.Preview != nil
		st.Busy = true
		m.Rename = &st
		return m, renameProviderCmd(m.api, st.From, st.Draft, !confirmed)
	}
	m.Rename = &st
	return m, nil
}

func (m Model) applyRenameProviders(msg RenameProvidersMsg) (tea.Model, tea.Cmd) {
	if m.Rename == nil {
		return m, nil
	}
	if msg.Err != nil {
		m.Rename = nil
		m.Err = fmt.Errorf("list providers: %w", msg.Err)
		return m, nil
	}
	m.Rename = &renameState{Providers: msg.IDs}
	return m, nil
}

func (m Model) applyRenameResult(msg RenameResultMsg) (tea.Model, tea.Cmd) {
	if m.Rename == nil || m.Rename.From != msg.From {
		return m, nil
	}
	st := *m.Rename
	st.Busy = false
	if msg.Err != nil {
		m.Rename = nil
		m.Err = fmt.Errorf("rename %s: %w", msg.From, msg.Err)
		return m, nil
	}
	if !msg.DryRun && msg.Conflict == nil {
		m.Rename = nil
		m.Notice = fmt.Sprintf("renamed %s -> %s", msg.From, msg.Draft)
		return m, ReloadAccountsCmd(m.api, "")
	}
	st.Checked = msg.Draft
	st.Preview, st.Conflicts = nil, nil
	if msg.Conflict != nil {
		st.Conflicts = msg.Conflict.Conflicts
	} else {
		resp := msg.Response
		st.Preview = &resp
	}
	m.Rename = &st
	return m, nil
}

func (m Model) renderRenameModal() string {
	st := m.Rename
	if st.From == "" {
		lines := []string{InfoTitleStyle.Render("Rename provider"), ""}
		switch {
		case st.Busy:
			lines = append(lines, InfoValueStyle.Render("Loading providers..."))
		case len(st.Providers) == 0:
			lines = append(lines, InfoValueStyle.Render("No providers configured."))
		default:
			lines = append(lines, InfoValueStyle.Render("Select provider to rename:"), "")
			for i, id := range st.Providers {
				cursor := " "
				if i == st.Cursor {
					cursor = ">"
				}
				lines = append(lines, InfoValueStyle.Render(cursor+" "+id))
			}
		}
		lines = append(lines, "", ActionMenuHintStyle.Render("[↑/↓] Move   [enter] Select   [esc] Cancel"))
		return InfoBoxStyle.Copy().Width(modalWidthForLines(lines, 56)).Render(strings.Join(lines, "\n"))
	}
	lines := []string{fmt.Sprintf("New id for %s: %s_", st.From, st.Draft)}
	current := st.Checked == st.Draft && st.Draft != ""
	switch {
	case st.Busy:
		lines = append(lines, "", "Checking...")
	case current && len(st.Conflicts) > 0:
		lines = append(lines, "", "Blocked, nothing will change:")
		for _, c := range st.Conflicts {
			lines = append(lines, "  "+c.Detail)
		}
	case current && st.Preview != nil:
		p := st.Preview.Plan
		accounts := "none"
		if len(p.Accounts) > 0 {
			accounts = strings.Join(p.Accounts, ", ")
		}
		lines = append(lines, "",
			fmt.Sprintf("Config references: %d", p.ConfigRefs),
			"Accounts: "+accounts,
			fmt.Sprintf("Usage history: %d attempts", p.UsageAttempts))
		for _, w := range st.Preview.Warnings {
			lines = append(lines, "Note: "+w.Detail)
		}
	}
	hint := "[enter] Check   [esc] Cancel"
	if current && st.Preview != nil && !st.Busy {
		hint = "[enter] Rename   [esc] Cancel"
	}
	lines = append(lines, "", hint)
	return renderMessageModal("Rename provider", strings.Join(lines, "\n"), WarningStyle, m.Width)
}
