package integrations

import (
	"fmt"
	"path/filepath"
	"testing"
)

type journalSyncFailureIO struct {
	LocalIO
	fail bool
}

func (io *journalSyncFailureIO) CommitStaged(path string) error {
	if err := io.LocalIO.CommitStaged(path); err != nil {
		return err
	}
	if io.fail && filepath.Ext(path) == ".json" && filepath.Base(path)[0] == '.' {
		return fmt.Errorf("injected post-rename sync failure")
	}
	return nil
}

func TestReplacementPublicationRetainsHistoricalReceiptAfterRenameFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	io := &journalSyncFailureIO{}
	module := NewPi(PiOptions{ConfigPath: path, Port: testPort, Models: []Model{{ID: "router/first"}}, IO: io})
	if r := module.Apply(); !r.OK {
		t.Fatal(r)
	}
	if r := module.Rollback(); !r.OK {
		t.Fatal(r)
	}
	io.fail = true
	module = NewPi(PiOptions{ConfigPath: path, Port: testPort + 1, Models: []Model{{ID: "router/second"}}, IO: io})
	if r := module.Apply(); r.OK {
		t.Fatal("uncertain journal publication reported success")
	}
	j, err := loadJournal(LocalIO{}, path)
	if err != nil || j == nil || len(j.History) != 1 || len(j.History[0]) != 1 || j.History[0][0].Original.Present {
		t.Fatalf("historical absent-original receipt lost: %v %v", j, err)
	}
	io.fail = false
	if r := module.Apply(); !r.OK {
		t.Fatal("replacement retry", r)
	}
	if r := module.Rollback(); !r.OK {
		t.Fatal(r)
	}
}
