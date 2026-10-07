package integrations

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedTextWrittenConflictRetainsFilesAndJournal(t *testing.T) {
	for _, id := range []ID{Codex, Grok, Omp} {
		t.Run(string(id), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			seed := userToml
			if id == Omp {
				seed = userModelYaml
			}
			writeFileOrDie(t, path, seed)
			models := []Model{{ID: "router/model"}}
			var module Module
			switch id {
			case Codex:
				module = NewCodex(CodexOptions{ConfigPath: path, Port: testPort, Models: models})
			case Grok:
				module = NewGrok(GrokOptions{ConfigPath: path, Port: testPort, Models: models})
			case Omp:
				module = NewOmp(OmpOptions{ModelsPath: path, Port: testPort, Models: models})
			}
			if result := module.Apply(); !result.OK {
				t.Fatal(result)
			}
			applied := readFile(t, path)
			journal := readFile(t, restorationPath(path))
			edited := strings.Replace(applied, ProviderBaseUrl(testPort), "https://mock.invalid/v1", 1)
			if edited == applied {
				t.Fatal("fixture did not change managed endpoint")
			}
			writeFileOrDie(t, path, edited)
			for _, result := range []ApplyResult{module.Apply(), module.Rollback()} {
				if result.OK || !strings.Contains(result.Reason, "conflict") {
					t.Fatalf("written conflict accepted: %+v", result)
				}
			}
			if readFile(t, path) != edited || readFile(t, restorationPath(path)) != journal {
				t.Fatal("refusal changed current settings or original journal")
			}
			writeFileOrDie(t, path, applied)
			if result := module.Rollback(); !result.OK {
				t.Fatal(result)
			}
			if readFile(t, path) != seed {
				t.Fatal("historical original lost after conflict")
			}
		})
	}
}
