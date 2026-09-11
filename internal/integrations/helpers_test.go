package integrations

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func tempFile(t *testing.T, dir string, name string, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const testPort = 8787

var userToml = "# user configuration\ntop_setting = \"keep\"\n\n[profile.default]\nmodel = \"gpt-5.2\"\n"

var userModelYaml = "theme: dark\nproviders:\n  openai:\n    baseUrl: https://api.openai.com/v1\n    api: openai-completions\n    apiKey: sk-user\n    models: []\n"

var grokTestModels = []Model{
	{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", ContextWindow: 400000},
	{ID: "gemini-3-pro", Name: "Gemini 3 Pro"},
}

var ompTestModels = []Model{
	{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex"},
	{ID: "gemini-3-pro", Name: "Gemini 3 Pro"},
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func replaceOne(s, old, new string) string { return strings.Replace(s, old, new, 1) }

func writeFileOrDie(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "prism-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func readText(t *testing.T, path string) string {
	t.Helper()
	return readFile(t, path)
}

func localFileExists(path string) bool {
	return LocalIO{}.FileExists(path)
}

func localReadTextIfExists(path string) (string, bool) {
	return LocalIO{}.ReadTextIfExists(path)
}
