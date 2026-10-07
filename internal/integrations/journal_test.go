package integrations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientStateSurvivesChangesAndRestart(t *testing.T) {
	for _, id := range IDs {
		t.Run(string(id), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config")
			seed := "theme: dark\nproviders:\n  prism:\n    apiKey: user-key\n    baseUrl: https://mock.invalid/v1\n    models: []\n"
			if id == Codex || id == Grok {
				seed = "theme = \"dark\"\n"
			}
			if id == Claude {
				seed = "{\n  \"theme\": \"dark\",\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"https://mock.invalid\",\n    \"ANTHROPIC_MODEL\": \"user-model\"\n  }\n}\n"
			}
			if id == Pi || id == Opencode {
				seed = "{\n  \"theme\": \"dark\",\n  \"providers\": {\n    \"prism\": {\n      \"baseUrl\": \"https://mock.invalid/v1\"\n    }\n  }\n}\n"
			}
			writeFileOrDie(t, path, seed)
			models := []Model{{ID: "router/first", Name: "First", ContextWindow: 8000}}
			makeModule := func(port int) Module {
				switch id {
				case Codex:
					return NewCodex(CodexOptions{ConfigPath: path, Port: port, Models: models})
				case Grok:
					return NewGrok(GrokOptions{ConfigPath: path, Port: port, Models: models})
				case Omp:
					return NewOmp(OmpOptions{ModelsPath: path, Port: port, Models: models})
				case Hermes:
					return NewHermes(HermesOptions{ConfigPath: path, Port: port, Models: models})
				case Claude:
					return NewClaude(ClaudeOptions{ConfigPath: path, Port: port, Models: models})
				case Pi:
					return NewPi(PiOptions{ConfigPath: path, Port: port, Models: models})
				default:
					return NewOpencode(OpencodeOptions{ConfigPath: path, Port: port, Models: models})
				}
			}
			module := makeModule(testPort)
			apply := func(m Module) ApplyResult {
				if forced, ok := m.(ForcedModule); ok {
					return forced.ApplyForced()
				}
				return m.Apply()
			}
			if result := apply(module); !result.OK {
				t.Fatalf("initial %+v", result)
			}
			first := readFile(t, path)
			if result := apply(module); !result.OK {
				t.Fatalf("repeat %+v", result)
			}
			if got := readFile(t, path); got != first {
				t.Fatal("repeated apply changed config")
			}
			models = []Model{{ID: "router/second", Name: "Second", ContextWindow: 16000}}
			module = makeModule(testPort + 1)
			if result := apply(module); !result.OK {
				t.Fatalf("restart change %+v", result)
			}
			if result := module.Rollback(); !result.OK {
				t.Fatalf("rollback %+v", result)
			}
			if got := readFile(t, path); got != seed {
				t.Fatalf("original lost\n%s", got)
			}
		})
	}
}

func TestClaudePreservesAbsentOriginalAndRefusesWrittenConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	module := NewClaude(ClaudeOptions{ConfigPath: path, Port: testPort, Models: []Model{{ID: "router/model"}}})
	if r := module.Apply(); !r.OK {
		t.Fatal(r)
	}
	applied := readFile(t, path)
	edited := strings.Replace(applied, "claude-router--model", "user-edited", 1)
	writeFileOrDie(t, path, edited)
	if r := module.Rollback(); r.OK {
		t.Fatal("edited written value restored")
	}
	if readFile(t, path) != edited {
		t.Fatal("refusal mutated settings")
	}
	writeFileOrDie(t, path, applied)
	module = NewClaude(ClaudeOptions{ConfigPath: path, Port: testPort + 1, Models: []Model{{ID: "router/next"}}})
	if r := module.Apply(); !r.OK {
		t.Fatal(r)
	}
	if r := module.Rollback(); !r.OK {
		t.Fatal(r)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("absent original recreated: %v", err)
	}
	j, err := loadJournal(LocalIO{}, path)
	if err != nil || j == nil || !j.Retired {
		t.Fatalf("retirement receipt %v %v", j, err)
	}
}

type failedPublicationIO struct {
	LocalIO
	failPath   string
	failRemove string
	once       bool
}

func (f *failedPublicationIO) StageWrite(path, text string) error {
	if path == f.failPath {
		return fmt.Errorf("injected stage failure")
	}
	return f.LocalIO.StageWrite(path, text)
}
func (f *failedPublicationIO) RemoveDurable(path string) error {
	if path == f.failRemove && !f.once {
		f.once = true
		if err := f.LocalIO.Remove(path); err != nil {
			return err
		}
		return fmt.Errorf("injected directory sync failure")
	}
	return f.LocalIO.RemoveDurable(path)
}

func TestRetirementRetriesAndReplacementRetainsReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	io := &failedPublicationIO{}
	module := NewPi(PiOptions{ConfigPath: path, Port: testPort, Models: []Model{{ID: "router/model"}}, IO: io})
	if r := module.Apply(); !r.OK {
		t.Fatal(r)
	}
	io.failRemove = path
	if r := module.Rollback(); r.OK {
		t.Fatal("failed durable removal reported success")
	}
	if r := module.Rollback(); !r.OK {
		t.Fatal("retirement retry", r)
	}
	receipt := readFile(t, restorationPath(path))
	io.failPath = restorationPath(path)
	if r := module.Apply(); r.OK {
		t.Fatal("failed replacement journal reported success")
	}
	if readFile(t, restorationPath(path)) != receipt {
		t.Fatal("previous receipt retired before replacement")
	}
}

func TestAllClientPreflightReadFailureLeavesDiskUntouched(t *testing.T) {
	for _, id := range IDs {
		t.Run(string(id), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "unreadable")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			models := []Model{{ID: "router/model"}}
			var module Module
			switch id {
			case Codex:
				module = NewCodex(CodexOptions{ConfigPath: path, Port: testPort, Models: models})
			case Grok:
				module = NewGrok(GrokOptions{ConfigPath: path, Port: testPort, Models: models})
			case Omp:
				module = NewOmp(OmpOptions{ModelsPath: path, Port: testPort, Models: models})
			case Hermes:
				module = NewHermes(HermesOptions{ConfigPath: path, Port: testPort, Models: models})
			case Claude:
				module = NewClaude(ClaudeOptions{ConfigPath: path, Port: testPort, Models: models})
			case Pi:
				module = NewPi(PiOptions{ConfigPath: path, Port: testPort, Models: models})
			default:
				module = NewOpencode(OpencodeOptions{ConfigPath: path, Port: testPort, Models: models})
			}
			if result := module.Apply(); result.OK {
				t.Fatal("unreadable treated as absent")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "unreadable" {
				t.Fatalf("preflight mutated disk: %v", entries)
			}
		})
	}
}

func TestJSONCSettingsPreserveUnmanagedBytesAndGenerations(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	seed := "{\n  // keep this comment\n  \"theme\": \"dark\",\n  \"provider\": {},\n}\n"
	tempFile(t, filepath.Dir(path), filepath.Base(path), seed)
	module := NewOpencode(OpencodeOptions{Home: home, Port: testPort, Models: []Model{{ID: "router/model", ReasoningEfforts: []string{"high"}, DefaultReasoningEffort: "high"}}})
	if r := module.Apply(); !r.OK {
		t.Fatal(r)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "// keep this comment") {
		t.Fatal("comment lost")
	}
	for _, container := range []string{"provider", "providers"} {
		read := ReadJSONBlockLeaf(got, container, "prism", "config", "baseURL")
		if read.Kind != jsonLeafPresent || read.Endpoint == nil || *read.Endpoint != ProviderBaseUrl(testPort) {
			t.Fatalf("generation %s: %v", container, read)
		}
	}
	if (LocalIO{}).FileExists(strings.TrimSuffix(path, "c")) {
		t.Fatal("second config created")
	}
	if r := module.Rollback(); !r.OK {
		t.Fatal(r)
	}
	if readFile(t, path) != seed {
		t.Fatal("JSONC original lost")
	}
}
