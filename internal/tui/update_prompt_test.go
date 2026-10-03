package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deLiseLINO/prism/internal/management"
	"github.com/deLiseLINO/prism/internal/update"
)

func promptModel(t *testing.T) (Model, string) {
	t.Helper()
	dir := t.TempDir()
	m := testModel(nil).WithStartupUpdate("9.9.9", update.MethodGo, dir)
	if !m.UpdatePromptVisible {
		t.Fatal("startup prompt not visible")
	}
	return m, dir
}

func press(m Model, msg tea.KeyMsg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func runeKey(r string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)} }

func TestStartupUpdateIgnoredForUnsupportedMethod(t *testing.T) {
	m := testModel(nil).WithStartupUpdate("9.9.9", update.MethodUnknown, t.TempDir())
	if m.UpdatePromptVisible || m.updateAvailable() {
		t.Fatal("unknown method must not prompt")
	}
}

func TestUpdatePromptUpdateNowKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{runeKey("1"), {Type: tea.KeyEnter}} {
		m, _ := promptModel(t)
		got, cmd := press(m, key)
		method, version, ok := got.PendingUpdate()
		if !ok || method != update.MethodGo || version != "9.9.9" {
			t.Fatalf("%v: pending = %v %q %v", key, method, version, ok)
		}
		if got.UpdatePromptVisible || got.UpdateAvailableHint != "" {
			t.Fatalf("%v: modal/hint state wrong", key)
		}
		if cmd == nil {
			t.Fatalf("%v: expected quit cmd", key)
		}
		if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
			t.Fatalf("%v: cmd is not quit", key)
		}
	}
}

func TestUpdatePromptSkipShowsHint(t *testing.T) {
	for _, key := range []tea.KeyMsg{runeKey("2"), {Type: tea.KeyEsc}} {
		m, dir := promptModel(t)
		got, cmd := press(m, key)
		if got.UpdatePromptVisible {
			t.Fatalf("%v: modal still visible", key)
		}
		if !strings.Contains(got.UpdateAvailableHint, "press U") {
			t.Fatalf("%v: hint = %q", key, got.UpdateAvailableHint)
		}
		if _, _, ok := got.PendingUpdate(); ok || cmd != nil {
			t.Fatalf("%v: skip must not queue upgrade", key)
		}
		if update.LoadState(dir).DismissedVersion != "" {
			t.Fatalf("%v: skip must not persist dismissal", key)
		}
	}
}

func TestUpdatePromptDismissPersists(t *testing.T) {
	m, dir := promptModel(t)
	got, cmd := press(m, runeKey("3"))
	if got.UpdatePromptVisible || got.UpdateAvailableHint != "" {
		t.Fatal("dismiss must hide modal and hint")
	}
	if cmd == nil {
		t.Fatal("expected dismiss cmd")
	}
	cmd()
	if state := update.LoadState(dir); state.DismissedVersion != "9.9.9" {
		t.Fatalf("state = %+v", state)
	}
	if _, _, ok := got.PendingUpdate(); ok {
		t.Fatal("dismiss must not queue upgrade")
	}
}

func TestUpdatePromptCursorEnter(t *testing.T) {
	m, _ := promptModel(t)
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.UpdatePromptCursor != updatePromptChoiceSkip {
		t.Fatalf("cursor = %d", m.UpdatePromptCursor)
	}
	got, _ := press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.UpdatePromptVisible || got.UpdateAvailableHint == "" {
		t.Fatal("enter on Skip must hide modal and show hint")
	}
}

func TestUpdatePromptSwallowsOtherKeys(t *testing.T) {
	m, _ := promptModel(t)
	got, cmd := press(m, runeKey("r"))
	if !got.UpdatePromptVisible || cmd != nil {
		t.Fatal("unrelated key must leave the modal untouched")
	}
}

func TestUpdateHotkeyReopensPrompt(t *testing.T) {
	m, _ := promptModel(t)
	m, _ = press(m, runeKey("2"))
	m, _ = press(m, runeKey("u"))
	if !m.UpdatePromptVisible || m.UpdatePromptCursor != updatePromptChoiceNow {
		t.Fatal("u must reopen the prompt")
	}
}

func TestUpdateHotkeyNoopWithoutUpdate(t *testing.T) {
	m := testModel(nil)
	m, _ = press(m, runeKey("u"))
	if m.UpdatePromptVisible {
		t.Fatal("no update known, prompt must stay closed")
	}
}

func TestUpdateAvailableMsgSetsHint(t *testing.T) {
	m := testModel(nil)
	next, _ := m.Update(UpdateAvailableMsg{Version: "1.2.0", Method: update.MethodBrew})
	got := next.(Model)
	if got.UpdatePromptVersion != "1.2.0" || !strings.Contains(got.UpdateAvailableHint, "press U") {
		t.Fatalf("got version %q hint %q", got.UpdatePromptVersion, got.UpdateAvailableHint)
	}
	next, _ = got.Update(UpdateAvailableMsg{Version: "1.1.0", Method: update.MethodBrew})
	if next.(Model).UpdatePromptVersion != "1.2.0" {
		t.Fatal("older version must not replace newer")
	}
	next, _ = m.Update(UpdateAvailableMsg{Version: "1.2.0", Method: update.MethodUnknown})
	if next.(Model).UpdatePromptVersion != "" {
		t.Fatal("unsupported method must be ignored")
	}
}

func TestUpdatePromptModalShowsCommand(t *testing.T) {
	m, _ := promptModel(t)
	out := m.renderUpdatePromptModal()
	for _, want := range []string{"9.9.9", "go install github.com/deLiseLINO/prism/cmd/prism@v9.9.9", "Skip until next version"} {
		if !strings.Contains(out, want) {
			t.Fatalf("modal missing %q:\n%s", want, out)
		}
	}
}

func TestSettingsUpdateCheckToggle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	m := testModel(nil)
	if !m.Settings.CheckForUpdateOnStartup {
		t.Fatal("update check must default to enabled")
	}
	m.openSettingsOverlay()
	m.settingsCursor = settingsRowUpdateCheck
	got, _ := press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.Settings.CheckForUpdateOnStartup {
		t.Fatal("enter must toggle update check off")
	}
	if !got.Settings.AutoRefreshEnabled {
		t.Fatal("auto-refresh must be untouched")
	}
}

func TestSettingsUpdateCheckPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	settings := DefaultSettings()
	settings.CheckForUpdateOnStartup = false
	if err := SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CheckForUpdateOnStartup {
		t.Fatal("setting was not persisted")
	}
}

func TestViewShowsUpdateHintAfterSkip(t *testing.T) {
	account := management.Account{ID: "acc-1", Provider: "codex"}
	m := testModel(newFakeClient(account), account)
	m.Width, m.Height = 140, 40
	m = m.WithStartupUpdate("9.9.9", update.MethodGo, t.TempDir())
	if strings.Contains(m.View(), "press U") {
		t.Fatal("hint must not show while the prompt is open")
	}
	m, _ = press(m, runeKey("2"))
	if !strings.Contains(m.View(), "Update available • press U") {
		t.Fatalf("hint missing from view:\n%s", m.View())
	}
}
