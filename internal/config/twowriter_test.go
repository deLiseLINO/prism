package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestUpdateRefusesWhenSecondManagerAdvancedTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Update(validDoc(), 0); err != nil {
		t.Fatal(err)
	}

	next := validDoc()
	next.Daemon.Listen = "127.0.0.1:9999"
	_, err = second.Update(next, 0)
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("second manager write: err = %v, want ErrStaleGeneration", err)
	}

	disk, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	snap := disk.Get()
	if snap.Config.Daemon.Listen == "127.0.0.1:9999" {
		t.Fatalf("second manager silently clobbered the file at generation %d", snap.Generation)
	}
	reloaded := second.Get()
	if reloaded.Generation != 1 {
		t.Fatalf("second manager did not adopt the on-disk snapshot: generation %d, want 1", reloaded.Generation)
	}
}
