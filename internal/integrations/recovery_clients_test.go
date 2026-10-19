package integrations

import (
	"path/filepath"
	"strings"
	"testing"
)

func recoveryClient(id ID, path string, port int, io FileIO) Module {
	models := []Model{{ID: "router/model"}}
	switch id {
	case Codex:
		return NewCodex(CodexOptions{ConfigPath: path, Port: port, Models: models, IO: io})
	case Grok:
		return NewGrok(GrokOptions{ConfigPath: path, Port: port, Models: models, IO: io})
	case Omp:
		return NewOmp(OmpOptions{ModelsPath: path, Port: port, Models: models, IO: io})
	case Claude:
		return NewClaude(ClaudeOptions{ConfigPath: path, Port: port, Models: models, IO: io})
	case Pi:
		return NewPi(PiOptions{ConfigPath: path, Port: port, Models: models, IO: io})
	case Opencode:
		return NewOpencode(OpencodeOptions{ConfigPath: path, Port: port, Models: models, IO: io})
	case Hermes:
		return NewHermes(HermesOptions{ConfigPath: path, Port: port, Models: models, IO: io})
	default:
		panic("unsupported client")
	}
}

func recoveryFixture(t *testing.T, id ID) (Module, string) {
	t.Helper()
	name, seed := "config.toml", userToml
	switch id {
	case Omp:
		name, seed = "models.yml", userModelYaml
	case Claude:
		name, seed = "settings.json", "{\n  \"env\": {\"USER_SETTING\": \"keep\"},\n  \"theme\": \"dark\"\n}\n"
	case Pi:
		name, seed = "models.json", "{\n  \"theme\": \"dark\"\n}\n"
	case Opencode:
		name, seed = "opencode.jsonc", "{\n  // user setting\n  \"theme\": \"dark\"\n}\n"
	case Hermes:
		name, seed = "config.yaml", userModelYaml
	}
	path := tempFile(t, t.TempDir(), name, seed)
	module := recoveryClient(id, path, testPort, nil)
	if result := module.Apply(); !result.OK {
		t.Fatal(result)
	}
	return module, path
}

func TestEveryClientReportsJournalConflictBeforeApply(t *testing.T) {
	for _, id := range IDs {
		t.Run(string(id), func(t *testing.T) {
			module, path := recoveryFixture(t, id)
			writeFileOrDie(t, restorationPath(path), "{broken")
			before := readFile(t, path)
			status := module.Status()
			if status.Conflict != ConflictJournal || !strings.Contains(status.Detail, "journal") {
				t.Fatalf("status did not expose recovery problem: %+v", status)
			}
			for _, result := range []ApplyResult{module.Apply(), module.Rollback()} {
				if result.OK || !result.Retryable || result.Conflict != ConflictJournal {
					t.Fatalf("conflict did not offer confirmed recovery: %+v", result)
				}
			}
			if readFile(t, path) != before || readFile(t, restorationPath(path)) != "{broken" {
				t.Fatal("status or refusal changed settings or journal")
			}
		})
	}
}

func TestEveryClientReconnectUsesCurrentBaseline(t *testing.T) {
	for _, id := range IDs {
		t.Run(string(id), func(t *testing.T) {
			_, path := recoveryFixture(t, id)
			baseline := readFile(t, path)
			writeFileOrDie(t, restorationPath(path), "{broken")
			module := recoveryClient(id, path, testPort+1, nil)
			forced, ok := module.(ForcedModule)
			if !ok {
				t.Fatal("confirmed reconnect unavailable")
			}
			result := forced.ApplyForced()
			if !result.OK || result.Backup == "" {
				t.Fatalf("reconnect: %+v", result)
			}
			if readFile(t, result.Backup) != "{broken" {
				t.Fatal("old journal not retained")
			}
			if status := module.Status(); status.Conflict != "" {
				t.Fatalf("conflict after reconnect: %+v", status)
			}
			// Restart must keep the new baseline, including side files.
			restarted := recoveryClient(id, path, testPort+1, nil)
			if result := restarted.Rollback(); !result.OK {
				t.Fatal(result)
			}
			if readFile(t, path) != baseline {
				t.Fatal("rollback reused historical originals instead of the pre-reconnect settings")
			}
			if result := restarted.Apply(); !result.OK {
				t.Fatal(result)
			}
			if result := restarted.Rollback(); !result.OK {
				t.Fatal(result)
			}
			if readFile(t, path) != baseline {
				t.Fatal("retired recovery baseline was lost on the next apply")
			}
		})
	}
}

func TestClaudeHistoricalModelConflictCanReconnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	before := `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787","ANTHROPIC_AUTH_TOKEN":"prism-loopback","ANTHROPIC_MODEL":"router/model","USER_SETTING":"keep"},"theme":"dark"}`
	writeFileOrDie(t, path, before)
	legacy := filepath.Join(filepath.Dir(path), ".prism-claude-env.json")
	writeFileOrDie(t, legacy, `{}`)
	module := NewClaude(ClaudeOptions{ConfigPath: path, Port: testPort, Models: []Model{{ID: "router/model"}}})
	if status := module.Status(); status.Conflict != ConflictJournal {
		t.Fatalf("historical conflict hidden: %+v", status)
	}
	if result := module.Apply(); result.OK || result.Conflict != ConflictJournal {
		t.Fatal(result)
	}
	if result := module.ApplyForced(); !result.OK || result.Backup == "" {
		t.Fatal(result)
	}
	if result := module.Rollback(); !result.OK {
		t.Fatal(result)
	}
	if readFile(t, path) != before {
		t.Fatal("reconnect lost current historical settings")
	}
}
