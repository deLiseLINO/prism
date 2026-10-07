package integrations

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeRollbackKeepsUnrelatedJSONCEditBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := "{\n  // original comment\n  \"theme\": \"dark\"\n}\n"
	writeFileOrDie(t, path, seed)
	module := NewClaude(ClaudeOptions{ConfigPath: path, Port: testPort, Models: []Model{{ID: "router/model"}}})
	if r := module.Apply(); !r.OK {
		t.Fatal(r)
	}
	edited := strings.Replace(readFile(t, path), "  // original comment", "  // user changed this comment", 1)
	edited = strings.Replace(edited, `"theme": "dark"`, `"theme": "light"`, 1)
	writeFileOrDie(t, path, edited)
	if r := module.Rollback(); !r.OK {
		t.Fatal(r)
	}
	want := strings.Replace(strings.Replace(seed, "  // original comment", "  // user changed this comment", 1), `"theme": "dark"`, `"theme": "light"`, 1)
	if got := readFile(t, path); got != want {
		t.Fatalf("unrelated bytes lost\nGOT %s\nWANT %s", got, want)
	}
}

func TestHistoricalClaudeJournalSurvivesPortAndModelChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := "{\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"https://mock.invalid\"\n  }\n}\n"
	applied := claudeTransform(testPort, []Model{{ID: "router/old"}}, true)(original)
	writeFileOrDie(t, path, applied.Next)
	journalPath := filepath.Join(filepath.Dir(path), ".prism-claude-env.json")
	journal := `{"ANTHROPIC_BASE_URL":"https://mock.invalid"}`
	writeFileOrDie(t, journalPath, journal)
	module := NewClaude(ClaudeOptions{ConfigPath: path, Port: testPort + 1, Models: []Model{{ID: "router/new"}}})
	cachePath := module.cachePath()
	tempFile(t, filepath.Dir(cachePath), filepath.Base(cachePath), RenderClaudeGatewayCache(ClaudeBaseURL(testPort), []Model{{ID: "router/old"}}, 1))
	if r := module.Apply(); !r.OK {
		t.Fatal(r)
	}
	if r := module.Rollback(); !r.OK {
		t.Fatal(r)
	}
	read := ReadJSONScalarKeys(readFile(t, path), "env", "ANTHROPIC_BASE_URL", "settings.json", []string{"ANTHROPIC_BASE_URL"})
	if read.Endpoint == nil || *read.Endpoint != "https://mock.invalid" {
		t.Fatalf("historical original %v", read)
	}
	if readFile(t, journalPath) != journal {
		t.Fatal("historical journal replaced")
	}
}
