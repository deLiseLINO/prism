package integrations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recoveryFaultIO struct {
	LocalIO
	failBackup int
	backups    int
	failCommit bool
	mutatePath string
	failRead   string
	failRemove string
}

func (io *recoveryFaultIO) CreateBackup(path, content string) error {
	io.backups++
	if io.failBackup == io.backups {
		return fmt.Errorf("backup publication failed")
	}
	if err := io.LocalIO.CreateBackup(path, content); err != nil {
		return err
	}
	if io.mutatePath != "" {
		if err := os.WriteFile(io.mutatePath, []byte("{newer}"), 0600); err != nil {
			return err
		}
		io.mutatePath = ""
	}
	return nil
}

func (io *recoveryFaultIO) ReadText(path string) (string, bool, error) {
	if path == io.failRead {
		return "", false, fmt.Errorf("file read failed")
	}
	return io.LocalIO.ReadText(path)
}

func (io *recoveryFaultIO) RemoveDurable(path string) error {
	if path == io.failRemove {
		return fmt.Errorf("legacy cleanup failed")
	}
	return io.LocalIO.RemoveDurable(path)
}

func (io *recoveryFaultIO) CommitStaged(path string) error {
	if io.failCommit && strings.Contains(path, "prism-journal.json") {
		return fmt.Errorf("journal publication failed")
	}
	return io.LocalIO.CommitStaged(path)
}

func TestClaudeUnreadableLegacyMetadataBlocksJournalRecovery(t *testing.T) {
	for _, name := range []string{"env", "cache"} {
		t.Run(name, func(t *testing.T) {
			_, path := recoveryFixture(t, Claude)
			before := readFile(t, path)
			writeFileOrDie(t, restorationPath(path), `{broken}`)
			legacy := filepath.Join(filepath.Dir(path), ".prism-claude-env.json")
			if name == "cache" {
				legacy = filepath.Join(filepath.Dir(path), "cache", ".prism-gateway-models.json")
			}
			io := &recoveryFaultIO{failRead: legacy}
			module := recoveryClient(Claude, path, testPort+1, io)
			if status := module.Status(); !status.Blocked || status.Conflict != "" {
				t.Fatalf("legacy read error hidden by journal conflict: %+v", status)
			}
			for _, result := range []ApplyResult{module.Apply(), module.Rollback(), module.(ForcedModule).ApplyForced()} {
				if result.OK || result.Retryable || result.Conflict != "" || len(result.Backups) != 0 || !strings.Contains(result.Reason, "file read failed") {
					t.Fatal(result)
				}
			}
			if readFile(t, path) != before || readFile(t, restorationPath(path)) != `{broken}` {
				t.Fatal("unreadable legacy metadata allowed writes")
			}
		})
	}
}

func TestUnreadableJournalIsBlockedWithoutBackup(t *testing.T) {
	_, path := recoveryFixture(t, Pi)
	before := readFile(t, path)
	io := &recoveryFaultIO{failRead: restorationPath(path)}
	module := recoveryClient(Pi, path, testPort+1, io)
	if status := module.Status(); !status.Blocked || status.Conflict != "" {
		t.Fatalf("read error not blocked: %+v", status)
	}
	result := module.(ForcedModule).ApplyForced()
	if result.OK || result.Retryable || len(result.Backups) != 0 {
		t.Fatal(result)
	}
	if readFile(t, path) != before {
		t.Fatal("unreadable journal allowed writes")
	}
}

func TestClaudeLegacyCleanupFailureKeepsCurrentBaseline(t *testing.T) {
	_, path := recoveryFixture(t, Claude)
	before := readFile(t, path)
	legacy := filepath.Join(filepath.Dir(path), ".prism-claude-env.json")
	writeFileOrDie(t, legacy, `{broken}`)
	writeFileOrDie(t, restorationPath(path), `{broken}`)
	io := &recoveryFaultIO{failRemove: legacy}
	module := recoveryClient(Claude, path, testPort+1, io).(ForcedModule)
	result := module.ApplyForced()
	if result.OK || result.Backup == "" || !strings.Contains(result.Reason, "legacy cleanup failed") {
		t.Fatal(result)
	}
	if readFile(t, legacy) != `{broken}` || readFile(t, path) != before {
		t.Fatal("cleanup failure changed settings")
	}
	restarted := recoveryClient(Claude, path, testPort+1, nil)
	if result := restarted.Apply(); !result.OK {
		t.Fatal(result)
	}
	if result := restarted.Rollback(); !result.OK {
		t.Fatal(result)
	}
	if readFile(t, path) != before {
		t.Fatal("leftover legacy data replaced the current baseline")
	}
}

func TestRecoveryBackupFailureLeavesEveryActiveFile(t *testing.T) {
	for _, failure := range []int{1, 2} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			_, path := recoveryFixture(t, Claude)
			legacy := filepath.Join(filepath.Dir(path), ".prism-claude-env.json")
			writeFileOrDie(t, legacy, `{old}`)
			writeFileOrDie(t, restorationPath(path), `{broken}`)
			config := readFile(t, path)
			cachePath := filepath.Join(filepath.Dir(path), "cache", "gateway-models.json")
			cache := readFile(t, cachePath)
			io := &recoveryFaultIO{failBackup: failure}
			module := recoveryClient(Claude, path, testPort+1, io).(ForcedModule)
			result := module.ApplyForced()
			if result.OK || !strings.Contains(result.Reason, "backup publication failed") {
				t.Fatal(result)
			}
			if readFile(t, path) != config || readFile(t, cachePath) != cache || readFile(t, legacy) != `{old}` || readFile(t, restorationPath(path)) != `{broken}` {
				t.Fatal("failed backup displaced active files")
			}
		})
	}
}

func TestRecoveryJournalPublicationFailureRetainsSnapshots(t *testing.T) {
	_, path := recoveryFixture(t, Pi)
	before := readFile(t, path)
	writeFileOrDie(t, restorationPath(path), `{broken}`)
	io := &recoveryFaultIO{failCommit: true}
	result := recoveryClient(Pi, path, testPort+1, io).(ForcedModule).ApplyForced()
	if result.OK || result.Backup == "" || len(result.Backups) < 2 {
		t.Fatal(result)
	}
	if readFile(t, path) != before || readFile(t, restorationPath(path)) != `{broken}` {
		t.Fatal("failed reset changed active files")
	}
	if readFile(t, result.Backups[1]) != before {
		t.Fatal("current snapshot missing after failure")
	}
}

func TestRecoveryRejectsChangesDuringBackup(t *testing.T) {
	_, path := recoveryFixture(t, Pi)
	before := readFile(t, path)
	journal := restorationPath(path)
	writeFileOrDie(t, journal, `{broken}`)
	io := &recoveryFaultIO{mutatePath: journal}
	result := recoveryClient(Pi, path, testPort+1, io).(ForcedModule).ApplyForced()
	if result.OK || !strings.Contains(result.Reason, "changed while saving") {
		t.Fatal(result)
	}
	if readFile(t, path) != before || readFile(t, journal) != `{newer}` {
		t.Fatal("concurrent state overwritten")
	}
}

func TestClaudeRecoveryRemovalPreservesUnprovenUserValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	before := `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787","ANTHROPIC_AUTH_TOKEN":"prism-loopback","ANTHROPIC_MODEL":"claude-router--user-choice","USER_SETTING":"keep"},"theme":"dark"}`
	writeFileOrDie(t, path, before)
	writeFileOrDie(t, restorationPath(path), `{broken}`)
	module := recoveryClient(Claude, path, testPort, nil).(ForcedRollbackModule)
	result := module.RollbackForced()
	if !result.OK || result.Backup == "" {
		t.Fatal(result)
	}
	got := readFile(t, path)
	if !strings.Contains(got, `"ANTHROPIC_MODEL":"claude-router--user-choice"`) || !strings.Contains(got, `"USER_SETTING":"keep"`) || strings.Contains(got, `ANTHROPIC_AUTH_TOKEN`) {
		t.Fatalf("removed user settings or retained managed token: %s", got)
	}
}

func TestUnsafeCurrentConfigBlocksJournalRecovery(t *testing.T) {
	for _, id := range []ID{Claude, Pi, Opencode, Hermes, Omp} {
		t.Run(string(id), func(t *testing.T) {
			_, path := recoveryFixture(t, id)
			before := `{"providers":false,"env":false}`
			if id == Hermes || id == Omp {
				before = "providers: [bad]\n"
			}
			writeFileOrDie(t, path, before)
			writeFileOrDie(t, restorationPath(path), `{broken}`)
			module := recoveryClient(id, path, testPort, nil)
			if status := module.Status(); !status.Blocked {
				t.Fatalf("invalid config not visible: %+v", status)
			}
			result := module.(ForcedModule).ApplyForced()
			if result.OK || result.Retryable || result.Backup != "" {
				t.Fatal(result)
			}
			if readFile(t, path) != before || readFile(t, restorationPath(path)) != `{broken}` {
				t.Fatal("unsafe recovery mutated state")
			}
		})
	}
}
