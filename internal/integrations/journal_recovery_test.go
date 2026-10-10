package integrations

import (
	"os"
	"strings"
	"testing"
)

func appliedOmp(t *testing.T) (*OmpIntegration, string) {
	t.Helper()
	path := tempFile(t, t.TempDir(), "models.yml", userModelYaml)
	o := NewOmp(OmpOptions{ModelsPath: path, Port: testPort, Models: ompTestModels})
	if res := o.Apply(); !res.OK {
		t.Fatalf("apply: %+v", res)
	}
	return o, path
}

func breakJournal(t *testing.T, path string) string {
	t.Helper()
	journal := readFile(t, restorationPath(path))
	broken := strings.Replace(journal, `"version": 1`, `"version": 2`, 1)
	if broken == journal {
		t.Fatal("fixture did not change the journal version")
	}
	writeFileOrDie(t, restorationPath(path), broken)
	return readFile(t, path)
}

func TestOmpJournalConflictRefusesPlainApply(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"invalid journal": func(t *testing.T, path string) {
			writeFileOrDie(t, restorationPath(path), "{broken")
		},
		"unsupported journal version": func(t *testing.T, path string) {
			breakJournal(t, path)
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			o, path := appliedOmp(t)
			corrupt(t, path)
			before := readFile(t, path)
			res := o.Apply()
			if res.OK || !res.Retryable || res.Conflict != ConflictJournal || !strings.Contains(res.Reason, "restoration journal") {
				t.Fatalf("plain apply: %+v", res)
			}
			if got := o.Status().Conflict; got != ConflictJournal {
				t.Fatalf("status conflict %q", got)
			}
			if readFile(t, path) != before {
				t.Fatal("refused apply changed the file")
			}
		})
	}
}

func TestOmpApplyForcedRetiresJournalAndRollsBackToNewBaseline(t *testing.T) {
	_, path := appliedOmp(t)
	baseline := breakJournal(t, path)
	oldJournal := readFile(t, restorationPath(path))
	o := NewOmp(OmpOptions{ModelsPath: path, Port: testPort + 1, Models: ompTestModels})
	res := o.ApplyForced()
	if !res.OK || res.Backup != restorationPath(path)+".bak" {
		t.Fatalf("forced apply: %+v", res)
	}
	if got := readFile(t, res.Backup); got != oldJournal {
		t.Fatalf("backup differs from the old journal:\n%q\nwant:\n%q", got, oldJournal)
	}
	if got := o.Status(); got.Conflict != "" || got.Endpoint == nil || *got.Endpoint != ProviderBaseUrl(testPort+1) {
		t.Fatalf("status after forced apply: %+v", got)
	}
	if res := o.Rollback(); !res.OK {
		t.Fatalf("rollback: %+v", res)
	}
	if got := readFile(t, path); got != baseline {
		t.Fatalf("rollback did not restore the pre-force file:\n%q\nwant:\n%q", got, baseline)
	}
}

func TestOmpRollbackForcedRemovesOnlyPrismBlock(t *testing.T) {
	o, path := appliedOmp(t)
	userEdit := "locale: en\n" + readFile(t, path)
	writeFileOrDie(t, path, userEdit)
	breakJournal(t, path)
	oldJournal := readFile(t, restorationPath(path))
	if res := o.Rollback(); res.OK || res.Conflict != ConflictJournal {
		t.Fatalf("plain rollback: %+v", res)
	}
	res := o.RollbackForced()
	if !res.OK || res.Backup != restorationPath(path)+".bak" {
		t.Fatalf("forced rollback: %+v", res)
	}
	if got := readFile(t, path); got != "locale: en\n"+userModelYaml {
		t.Fatalf("file after forced rollback:\n%q", got)
	}
	if got := readFile(t, res.Backup); got != oldJournal {
		t.Fatal("backup differs from the old journal")
	}
	if _, err := os.Stat(restorationPath(path)); !os.IsNotExist(err) {
		t.Fatalf("journal left in place: %v", err)
	}
}

func TestOmpForceWithoutConflictLeavesJournal(t *testing.T) {
	o, path := appliedOmp(t)
	if res := o.ApplyForced(); !res.OK || res.Backup != "" {
		t.Fatalf("forced apply: %+v", res)
	}
	if _, err := os.Stat(restorationPath(path)); err != nil {
		t.Fatalf("journal missing after forced apply: %v", err)
	}
	if res := o.RollbackForced(); !res.OK || res.Backup != "" {
		t.Fatalf("forced rollback: %+v", res)
	}
	if _, err := os.Stat(restorationPath(path) + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("backup created without a conflict: %v", err)
	}
	if _, err := os.Stat(restorationPath(path)); err != nil {
		t.Fatalf("journal missing after forced rollback: %v", err)
	}
}

func TestOmpSecondForcedResetKeepsFirstBackup(t *testing.T) {
	o, path := appliedOmp(t)
	journalPath := restorationPath(path)
	writeFileOrDie(t, journalPath, "{one")
	first := o.ApplyForced()
	if !first.OK || first.Backup != journalPath+".bak" {
		t.Fatalf("first reset: %+v", first)
	}
	writeFileOrDie(t, journalPath, "{two")
	second := o.ApplyForced()
	if !second.OK || second.Backup != journalPath+".bak.1" {
		t.Fatalf("second reset: %+v", second)
	}
	if readFile(t, journalPath+".bak") != "{one" || readFile(t, journalPath+".bak.1") != "{two" {
		t.Fatal("a backup was overwritten")
	}
}

func TestRetireJournalMissingIsNoop(t *testing.T) {
	path := tempFile(t, t.TempDir(), "models.yml", userModelYaml)
	backup, err := retireJournal(LocalIO{}, path)
	if err != nil || backup != "" {
		t.Fatalf("retire without journal: %q %v", backup, err)
	}
}

type plainModule struct{ rollbacks int }

func (m *plainModule) ID() ID             { return Pi }
func (m *plainModule) Apply() ApplyResult { return ApplyResult{OK: true, ID: Pi} }
func (m *plainModule) Status() Status     { return Status{ID: Pi} }
func (m *plainModule) Rollback() ApplyResult {
	m.rollbacks++
	return ApplyResult{OK: true, ID: Pi}
}

type forcedRollbackModule struct {
	plainModule
	forced int
}

func (m *forcedRollbackModule) RollbackForced() ApplyResult {
	m.forced++
	return ApplyResult{OK: true, ID: Pi, Backup: "/tmp/journal.bak"}
}

func TestRegistryRollbackForced(t *testing.T) {
	forced := &forcedRollbackModule{}
	registry := NewRegistry()
	if err := registry.Register(forced); err != nil {
		t.Fatal(err)
	}
	if res := registry.RollbackForced(Pi, true); res.Backup != "/tmp/journal.bak" || forced.forced != 1 {
		t.Fatalf("forced rollback not routed: %+v forced=%d", res, forced.forced)
	}
	if registry.Rollback(Pi); forced.forced != 1 || forced.rollbacks != 1 {
		t.Fatalf("plain rollback hit the forced path: forced=%d plain=%d", forced.forced, forced.rollbacks)
	}

	plain := &plainModule{}
	fallback := NewRegistry()
	if err := fallback.Register(plain); err != nil {
		t.Fatal(err)
	}
	if res := fallback.RollbackForced(Pi, true); !res.OK || plain.rollbacks != 1 {
		t.Fatalf("fallback to plain rollback: %+v plain=%d", res, plain.rollbacks)
	}
}

func TestOmpForcedRetryFailureKeepsBackupInResult(t *testing.T) {
	path := tempFile(t, t.TempDir(), "models.yml", "providers: {openai: {api: x}}\n")
	writeFileOrDie(t, restorationPath(path), "{broken")
	o := NewOmp(OmpOptions{ModelsPath: path, Port: testPort, Models: ompTestModels})
	res := o.ApplyForced()
	backup := restorationPath(path) + ".bak"
	if res.OK || res.Backup != backup || !strings.Contains(res.Reason, backup) {
		t.Fatalf("forced apply: %+v", res)
	}
	if got := readFile(t, backup); got != "{broken" {
		t.Fatalf("backup bytes %q", got)
	}
	if _, err := os.Stat(restorationPath(path)); !os.IsNotExist(err) {
		t.Fatalf("journal left in place: %v", err)
	}
}
