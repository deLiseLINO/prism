package integrations

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexRefreshRequiresExactManagedBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFileOrDie(t, path, "theme = \"dark\"\n")
	models := []Model{{ID: "router/first"}}
	module := NewCodex(CodexOptions{ConfigPath: path, Port: testPort, ModelsSource: func() []Model { return models }})
	if r := module.Apply(); !r.OK {
		t.Fatal(r)
	}
	original := readFile(t, path)
	catalogPath := CodexCatalogPath(path)
	catalog := readFile(t, catalogPath)
	models = []Model{{ID: "router/second"}}
	for _, config := range []string{
		strings.Replace(original, prismCatalogMarker+"\n", "", 1),
		strings.Replace(original, catalogPath, filepath.Join(filepath.Dir(path), "foreign.json"), 1),
		strings.Replace(original, CodexFence.End, "", 1),
	} {
		writeFileOrDie(t, path, config)
		if module.ManagedBinding() {
			t.Fatal("foreign or damaged binding accepted")
		}
		if err := module.RefreshCatalog(); err != nil {
			t.Fatal(err)
		}
		if readFile(t, catalogPath) != catalog {
			t.Fatal("unsafe refresh overwrote catalog")
		}
	}
	writeFileOrDie(t, path, original)
	if !module.ManagedBinding() {
		t.Fatal("exact managed binding rejected")
	}
	if err := module.RefreshCatalog(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, catalogPath), "router/second") {
		t.Fatal("managed refresh missing model")
	}
	if r := module.Rollback(); !r.OK {
		t.Fatal(r)
	}
	if readFile(t, path) != "theme = \"dark\"\n" {
		t.Fatal("refresh changed restoration original")
	}
}
