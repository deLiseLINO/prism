package account

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"prism/internal/quota"
)

func writeMeta(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func validFile() string {
	return `{
  "version": 1,
  "accounts": [
    {
      "id": "codex:acc-1",
      "provider": "codex",
      "priority": 2,
      "credGen": 3,
      "state": "active",
      "email": "u@e.co",
      "quota": {"used": 10, "limit": 100, "source": "header"},
      "createdAt": "2023-01-01T00:00:00Z",
      "updatedAt": "2023-01-02T00:00:00Z"
    },
    {
      "id": "codex:acc-2",
      "provider": "codex",
      "priority": 1,
      "credGen": 1,
      "state": "paused",
      "createdAt": "2023-01-01T00:00:00Z",
      "updatedAt": "2023-01-01T00:00:00Z"
    }
  ]
}`
}

func TestRepositoryLoadParsesAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	writeMeta(t, path, validFile())
	repo := OpenMeta(path)
	accounts, err := repo.Load("codex")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts = %d", len(accounts))
	}
	a := accounts[0]
	if a.ID != "codex:acc-1" || a.Priority != 2 || a.CredGen != 3 || a.State != Active {
		t.Fatalf("account = %+v", a)
	}
	if a.Quota.Used != 10 || a.Quota.Limit == nil || *a.Quota.Limit != 100 || a.Quota.Source != quota.SourceHeader {
		t.Fatalf("quota = %+v", a.Quota)
	}
	if accounts[1].State != Paused {
		t.Fatalf("state = %v", accounts[1].State)
	}
}

func TestRepositoryLoadRejectsInvalidFiles(t *testing.T) {
	cases := map[string]string{
		"bad json":      `{`,
		"wrong version": `{"version": 2, "accounts": []}`,
		"wrong provider": `{"version": 1, "accounts": [{
			"id": "a", "provider": "antigravity", "credGen": 1,
			"state": "active", "createdAt": "2023-01-01T00:00:00Z", "updatedAt": "2023-01-01T00:00:00Z"}]}`,
		"zero credgen": `{"version": 1, "accounts": [{
			"id": "a", "provider": "codex", "credGen": 0,
			"state": "active", "createdAt": "2023-01-01T00:00:00Z", "updatedAt": "2023-01-01T00:00:00Z"}]}`,
		"bad state": `{"version": 1, "accounts": [{
			"id": "a", "provider": "codex", "credGen": 1,
			"state": "zombie", "createdAt": "2023-01-01T00:00:00Z", "updatedAt": "2023-01-01T00:00:00Z"}]}`,
		"duplicate id": `{"version": 1, "accounts": [
			{"id": "a", "provider": "codex", "credGen": 1, "state": "active", "createdAt": "2023-01-01T00:00:00Z", "updatedAt": "2023-01-01T00:00:00Z"},
			{"id": "a", "provider": "codex", "credGen": 2, "state": "active", "createdAt": "2023-01-01T00:00:00Z", "updatedAt": "2023-01-01T00:00:00Z"}]}`,
	}
	for name, content := range cases {
		path := filepath.Join(t.TempDir(), "accounts.json")
		writeMeta(t, path, content)
		repo := OpenMeta(path)
		if _, err := repo.Load("codex"); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestRepositoryMissingFileLoadsEmpty(t *testing.T) {
	repo := OpenMeta(filepath.Join(t.TempDir(), "none.json"))
	accounts, err := repo.Load("codex")
	if err != nil || len(accounts) != 0 {
		t.Fatalf("accounts = %v err = %v", accounts, err)
	}
}

func TestEnsureCreatesRowAndPersistsEmail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	repo := OpenMeta(path)
	if err := repo.Ensure("codex", "codex:acc-1", 1, "dev@example.com"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	reloaded := OpenMeta(path)
	accounts, err := reloaded.Load("codex")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != "codex:acc-1" || accounts[0].CredGen != 1 {
		t.Fatalf("accounts = %+v", accounts)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "dev@example.com") {
		t.Fatalf("email missing from metadata: %s", raw)
	}
	if !strings.Contains(string(raw), "active") {
		t.Fatalf("state missing: %s", raw)
	}
}

func TestEnsureAdvancesGenerationWithoutDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	writeMeta(t, path, validFile())
	repo := OpenMeta(path)
	if err := repo.Ensure("codex", "codex:acc-1", 4, "new@e.co"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	reloaded := OpenMeta(path)
	accounts, err := reloaded.Load("codex")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("rows = %d, want 2", len(accounts))
	}
	a := accounts[0]
	if a.CredGen != 4 || a.Priority != 2 || a.State != Active || a.Quota.Used != 10 {
		t.Fatalf("updated row = %+v", a)
	}
	if accounts[1].CredGen != 1 {
		t.Fatalf("other row touched: %+v", accounts[1])
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "new@e.co") || strings.Contains(string(raw), "u@e.co") {
		t.Fatalf("email not updated: %s", raw)
	}
}

func TestEnsureRejectsZeroGeneration(t *testing.T) {
	repo := OpenMeta(filepath.Join(t.TempDir(), "accounts.json"))
	if err := repo.Ensure("codex", "codex:acc-1", 0, "e@e.co"); err == nil {
		t.Fatal("expected zero generation error")
	}
}

func TestCurrentGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	writeMeta(t, path, validFile())
	repo := OpenMeta(path)
	gen, err := repo.CurrentGeneration("codex", "codex:acc-2")
	if err != nil || gen != 1 {
		t.Fatalf("gen = %d err = %v", gen, err)
	}
	gen, err = repo.CurrentGeneration("codex", "codex:missing")
	if err != nil || gen != 0 {
		t.Fatalf("missing gen = %d err = %v", gen, err)
	}
}

func TestRepositoryWritesAreAtomicAndRestartSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	repo := OpenMeta(path)
	for i := 0; i < 3; i++ {
		if err := repo.Ensure("codex", "codex:acc-1", CredentialGeneration(i+1), "e@e.co"); err != nil {
			t.Fatalf("ensure: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".accounts-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	reloaded := OpenMeta(path)
	accounts, err := reloaded.Load("codex")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(accounts) != 1 || accounts[0].CredGen != 3 {
		t.Fatalf("accounts = %+v", accounts)
	}
}

func TestMetaWritePreservesForeignProviderFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	writeMeta(t, path, `{"version":1,"accounts":[]}`)
	repo := OpenMeta(path)
	if err := repo.Ensure("antigravity", "antigravity:g-1", 1, "a@e.co"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := repo.Load("codex"); err == nil {
		t.Fatal("cross-provider load must fail")
	}
}
