package integrations

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentClientInstancesPreserveOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := "{\n  \"theme\": \"dark\"\n}\n"
	writeFileOrDie(t, path, seed)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			module := NewClaude(ClaudeOptions{ConfigPath: path, Port: testPort + i, Models: []Model{{ID: "router/model"}}})
			if result := module.Apply(); !result.OK {
				t.Errorf("apply %d: %+v", i, result)
			}
			_ = module.Status()
		}(i)
	}
	wg.Wait()
	module := NewClaude(ClaudeOptions{ConfigPath: path, Port: testPort, Models: []Model{{ID: "router/model"}}})
	if result := module.Rollback(); !result.OK {
		t.Fatal(result)
	}
	if got := readFile(t, path); got != seed {
		t.Fatalf("concurrent original lost: %s", got)
	}
}

func TestCodexRefreshAndRollbackShareMutationOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	seed := "theme = \"dark\"\n"
	writeFileOrDie(t, path, seed)
	module := NewCodex(CodexOptions{ConfigPath: path, Port: testPort, Models: []Model{{ID: "router/model"}}})
	if result := module.Apply(); !result.OK {
		t.Fatal(result)
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if err := module.RefreshCatalog(); err != nil {
					t.Error(err)
				}
			} else {
				_ = module.Rollback()
			}
		}(i)
	}
	wg.Wait()
	if got := readFile(t, path); got != seed {
		t.Fatalf("refresh raced restoration: %s", got)
	}
	if (LocalIO{}).FileExists(CodexCatalogPath(path)) {
		t.Fatal("retired catalog recreated by refresh")
	}
}
