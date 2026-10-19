package integrations

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSafetyStoredStatesCannotPublishUnsupportedConfig(t *testing.T) {
	for _, field := range []string{"original", "written", "pending", "history"} {
		t.Run(field, func(t *testing.T) {
			path := tempFile(t, t.TempDir(), "models.json", `{"providers":{}}`)
			current := fileState{Present: true, Text: readFile(t, path)}
			invalid := fileState{Present: true, Text: `{"providers":false}`}
			f := journalFile{Path: path, Original: current, Written: current}
			j := &restorationJournal{Version: 1, Target: path, Files: []journalFile{f}}
			switch field {
			case "original":
				j.Files[0].Original = invalid
			case "written":
				j.Files[0].Written = invalid
				j.Files[0].Pending = &current
			case "pending":
				j.Files[0].Pending = &invalid
			case "history":
				f.Original = invalid
				j.History = [][]journalFile{{f}}
			}
			if err := publishJournal(LocalIO{}, j); err != nil {
				t.Fatal(err)
			}
			journalBefore := readFile(t, restorationPath(path))
			module := NewPi(PiOptions{ConfigPath: path, Port: testPort, Models: []Model{{ID: "router/model"}}})
			for _, result := range []ApplyResult{module.Apply(), module.Rollback()} {
				if result.OK || result.Conflict != ConflictJournal || !result.Retryable {
					t.Fatalf("unsupported persisted %s allowed publication: %+v", field, result)
				}
			}
			if readFile(t, path) != current.Text || readFile(t, restorationPath(path)) != journalBefore {
				t.Fatal("refusal changed current settings or recovery authority")
			}
		})
	}
}

func TestConfigSafetyJournalSchemaNeverDefaultsToDeletion(t *testing.T) {
	for _, mutation := range []string{"missing original", "null original", "missing written", "missing path", "missing present", "missing text", "null present", "null text", "null pending", "history missing original", "retired pending", "retired restoring"} {
		t.Run(mutation, func(t *testing.T) {
			path := tempFile(t, t.TempDir(), "models.json", `{"providers":{}}`)
			before := readFile(t, path)
			state := map[string]any{"present": true, "text": before}
			f := map[string]any{"path": path, "original": state, "written": state}
			j := map[string]any{"version": 1, "target": path, "retired": false, "restoring": false, "files": []any{f}}
			switch mutation {
			case "missing original":
				delete(f, "original")
			case "null original":
				f["original"] = nil
			case "missing written":
				delete(f, "written")
			case "missing path":
				delete(f, "path")
			case "missing present":
				delete(state, "present")
			case "missing text":
				delete(state, "text")
			case "null present":
				state["present"] = nil
			case "null text":
				state["text"] = nil
			case "null pending":
				f["pending"] = nil
			case "history missing original":
				j["history"] = []any{[]any{map[string]any{"path": path, "written": state}}}
			case "retired pending":
				j["retired"] = true
				f["pending"] = state
			case "retired restoring":
				j["retired"], j["restoring"] = true, true
			}
			raw, err := json.Marshal(j)
			if err != nil {
				t.Fatal(err)
			}
			writeFileOrDie(t, restorationPath(path), string(raw))
			module := NewPi(PiOptions{ConfigPath: path, Port: testPort})
			if result := module.Rollback(); result.OK || result.Conflict != ConflictJournal {
				t.Fatalf("invalid schema accepted: %+v", result)
			}
			if readFile(t, path) != before || readFile(t, restorationPath(path)) != string(raw) {
				t.Fatal("invalid schema changed current settings or journal")
			}
		})
	}
}

func TestConfigSafetyValidPendingPreservesAbsentAndEmptyOriginal(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("original-present=%v", present), func(t *testing.T) {
			path := tempFile(t, t.TempDir(), "models.json", `{}`)
			pending := fileState{Present: true, Text: `{"providers":{"prism":{"baseUrl":"https://mock.invalid/v1"}}}`}
			j := &restorationJournal{Version: 1, Target: path, Files: []journalFile{{
				Path: path, Original: fileState{Present: present},
				Written: fileState{Present: true, Text: `{}`}, Pending: &pending,
			}}}
			if err := publishJournal(LocalIO{}, j); err != nil {
				t.Fatal(err)
			}
			result := NewPi(PiOptions{ConfigPath: path, Port: testPort}).Rollback()
			if !result.OK {
				t.Fatalf("valid v1 completion refused: %+v", result)
			}
			got, exists, err := (LocalIO{}).ReadText(path)
			if err != nil || exists != present || got != "" {
				t.Fatalf("absent and empty original confused: present=%v text=%q err=%v", exists, got, err)
			}
			loaded, err := loadJournal(LocalIO{}, path)
			if err != nil || loaded == nil || !loaded.Retired || loaded.Restoring || loaded.Files[0].Pending != nil {
				t.Fatalf("valid v1 retirement could not reload: %+v, %v", loaded, err)
			}
		})
	}
}

func TestConfigSafetyUsesSupportedFormatGuards(t *testing.T) {
	cases := []struct {
		name, good, bad string
	}{
		{"config", "theme = \"dark\"\n", "model_provider = 42\n"},
		{"config.toml", "openai_base_url = \"https://mock.invalid/v1\"\n", "openai_base_url = 42\n"},
		{"config.toml", "model_catalog_json = \"foreign.json\"\n", "model_catalog_json = \"a\"\nmodel_catalog_json = \"b\"\n"},
		{"config.toml", "model_provider = \"foreign\"\n", "model_provider = \"a\"\nmodel_provider = \"b\"\n"},
		{"config.toml", "openai_base_url = \"https://mock.invalid\"\n", "openai_base_url = \"a\"\nopenai_base_url = \"b\"\n"},
		{"config.toml", RenderFencedBlock(CodexFence, "# managed"), CodexFence.Begin},
		{"config.toml", RenderFencedBlock(GrokFence, "# managed"), GrokFence.End},
		{"settings.json", `{"env":{"ANTHROPIC_MODEL":"foreign"}}`, `{"env":{"ANTHROPIC_MODEL":null}}`},
		{"settings.json", `{"env":{"USER_SETTING":42}}`, `{"env":false}`},
		{"models.json", `{"providers":{"prism":{}}}`, `{"providers":{"prism":false}}`},
		{"opencode.jsonc", "{ // comment\n\"provider\":{},\"providers\":{},}\n", `{"provider":false}`},
		{"opencode.json", `{"provider":{}}`, `{"providers":{"prism":[]}}`},
		{"models.yml", "providers: {}\n", "providers: [bad]\n"},
		{"models.yaml", "providers:\n  prism:\n    models: []\n", "providers:\n  prism: {}\n"},
		{"config.yaml", "theme: dark\n", "providers:\n  prism:\n  prism:\n"},
		{"gateway-models.json", `{}`, ""},
		{"prism-catalog.json", `{"models":[]}`, `[]`},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d", tc.name, i), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			if err := inspectConfig(path, tc.good); err != nil {
				t.Fatalf("supported user config refused: %v", err)
			}
			if err := inspectConfig(path, tc.bad); err == nil {
				t.Fatal("unsupported structure accepted")
			}
		})
	}
}

func TestConfigSafetyCurrentBlocksEvenWithoutUsableJournal(t *testing.T) {
	for _, journal := range []string{"", "{broken"} {
		t.Run(fmt.Sprintf("journal=%q", journal), func(t *testing.T) {
			path := tempFile(t, t.TempDir(), "models.json", `{"providers":false}`)
			if journal != "" {
				writeFileOrDie(t, restorationPath(path), journal)
			}
			called := false
			transform := func(text string) ConfigTransform {
				called = true
				return nextTransform(`{"providers":{}}`, true)
			}
			_, err := applyRestorationUnlocked(LocalIO{}, path, transform, nil, false, nil)
			var conflict *journalConflictError
			if err == nil || errors.As(err, &conflict) || called {
				t.Fatalf("unsafe current did not block before transform: %v, called=%v", err, called)
			}
			_, err = recoverRestorationUnlocked(LocalIO{}, path, nil, nil, transform, nil, false)
			if err == nil || errors.As(err, &conflict) || called {
				t.Fatalf("unsafe current allowed recovery planning: %v, called=%v", err, called)
			}
			if readFile(t, path) != `{"providers":false}` {
				t.Fatal("unsafe current settings changed")
			}
			if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
				t.Fatalf("unsafe current created backup: %v", err)
			}
		})
	}
}

func TestConfigSafetyLaterAcceptedReadErrorWinsOverConflict(t *testing.T) {
	path := tempFile(t, t.TempDir(), "models.json", `{"providers":{"prism":{"baseUrl":"https://mock.invalid/current"}}}`)
	cache := filepath.Join(filepath.Dir(path), "cache", "gateway-models.json")
	current := fileState{Present: true, Text: readFile(t, path)}
	original := fileState{Present: true, Text: `{"providers":{"prism":{"baseUrl":"https://mock.invalid/original"}}}`}
	written := fileState{Present: true, Text: `{"providers":{"prism":{"baseUrl":"https://mock.invalid/written"}}}`}
	j := &restorationJournal{Version: 1, Target: path, Files: []journalFile{
		{Path: path, Original: original, Written: written},
		{Path: cache, Original: fileState{}, Written: fileState{}},
	}}
	if err := publishJournal(LocalIO{}, j); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, restorationPath(path))
	io := &configSafetyFaultIO{readPath: cache}
	module := NewPi(PiOptions{ConfigPath: path, Port: testPort, Models: []Model{{ID: "router/model"}}, IO: io})
	status := module.Status()
	if !status.Blocked || status.Conflict != "" || !strings.Contains(status.Detail, "inventory read failed") {
		t.Fatalf("later I/O failure was hidden: %+v", status)
	}
	result := module.ApplyForced()
	if result.OK || result.Retryable || result.Backup != "" || !strings.Contains(result.Reason, "inventory read failed") {
		t.Fatalf("read failure allowed recovery: %+v", result)
	}
	if io.backups != 0 || readFile(t, path) != current.Text || readFile(t, restorationPath(path)) != before {
		t.Fatal("later unreadable owned path allowed backup or mutation")
	}
}

type configSafetyFaultIO struct {
	LocalIO
	readPath       string
	readAfterWrite bool
	corruptBackup  bool
	failBackup     int
	backups        int
}

func (io *configSafetyFaultIO) ReadText(path string) (string, bool, error) {
	if path == io.readPath && (!io.readAfterWrite || io.backups > 0) {
		return "", false, fmt.Errorf("inventory read failed at %s", path)
	}
	return io.LocalIO.ReadText(path)
}

func (io *configSafetyFaultIO) CreateBackup(path, text string) error {
	io.backups++
	if io.backups == io.failBackup {
		return fmt.Errorf("backup publication failed")
	}
	if err := io.LocalIO.CreateBackup(path, text); err != nil {
		return err
	}
	if io.corruptBackup {
		return os.WriteFile(path, []byte("incomplete backup"), 0600)
	}
	return nil
}

func TestConfigSafetyBackupReceiptContainsOnlyVerifiedFiles(t *testing.T) {
	for _, fault := range []string{"first publication", "second publication", "first readback", "mismatched readback"} {
		t.Run(fault, func(t *testing.T) {
			path := tempFile(t, t.TempDir(), "models.json", `{"providers":{}}`)
			journal := restorationPath(path)
			writeFileOrDie(t, journal, "{broken")
			io := &configSafetyFaultIO{}
			want := 0
			switch fault {
			case "first publication":
				io.failBackup = 1
			case "second publication":
				io.failBackup, want = 2, 1
			case "first readback":
				io.readPath, io.readAfterWrite = journal+".bak", true
			case "mismatched readback":
				io.corruptBackup = true
			}
			result := NewPi(PiOptions{ConfigPath: path, Port: testPort, Models: []Model{{ID: "router/model"}}, IO: io}).ApplyForced()
			if result.OK || len(result.Backups) != want || want == 0 && result.Backup != "" {
				t.Fatalf("unverified backup in receipt: %+v", result)
			}
			for _, saved := range result.Backups {
				if readFile(t, saved) != "{broken" {
					t.Fatal("verified metadata backup missing from receipt")
				}
			}
			if readFile(t, path) != `{"providers":{}}` || readFile(t, journal) != "{broken" {
				t.Fatal("failed preservation changed active files")
			}
		})
	}
}
