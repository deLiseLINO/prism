package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentManagersPublishOnlyOneCASWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	managers := []*Manager{first, second}
	errs := make([]error, 2)
	for i, mgr := range managers {
		wg.Add(1)
		go func(i int, mgr *Manager) {
			defer wg.Done()
			doc := validDoc()
			doc.Daemon.Listen = []string{"127.0.0.1:4101", "127.0.0.1:4102"}[i]
			<-start
			_, errs[i] = mgr.Update(doc, 0)
		}(i, mgr)
	}
	close(start)
	wg.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner >= 0 {
				t.Fatal("both managers published same generation")
			}
			winner = i
		} else if !errors.Is(err, ErrStaleGeneration) {
			t.Fatalf("writer error=%v", err)
		}
	}
	if winner < 0 {
		t.Fatalf("no CAS winner: %v", errs)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Get()
	if got.Generation != 1 || got.Config.Daemon.Listen != []string{"127.0.0.1:4101", "127.0.0.1:4102"}[winner] {
		t.Fatalf("disk lost winner=%+v", got)
	}
}

func TestUnreadableOrCorruptDiskCannotBeReplacedByCachedSnapshot(t *testing.T) {
	for _, kind := range []string{"corrupt", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			mgr, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.Update(validDoc(), 0); err != nil {
				t.Fatal(err)
			}
			before := mgr.Get()
			if kind == "corrupt" {
				if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := mgr.Update(validDoc(), before.Generation); err == nil {
				t.Fatal("unreadable disk overwritten")
			}
			if mgr.Get().Generation != before.Generation {
				t.Fatal("failed publication advanced snapshot")
			}
			if kind == "corrupt" {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "{broken" {
					t.Fatalf("corrupt evidence replaced: %q err=%v", raw, err)
				}
			}
		})
	}
}

func TestCurrentRejectsSameGenerationExternalPolicyChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	mgr, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Update(validDoc(), 0); err != nil {
		t.Fatal(err)
	}
	before, err := mgr.Current()
	if err != nil {
		t.Fatalf("current persisted document rejected: %v", err)
	}
	foreign := before.Config
	foreign.Daemon.Listen = "127.0.0.1:4999"
	raw, err := json.Marshal(fileFormat{Version: SchemaVersion, Generation: before.Generation, Config: foreign})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Current(); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("same-generation external change accepted: %v", err)
	}
	if mgr.Get().Config.Daemon.Listen == foreign.Daemon.Listen {
		t.Fatal("disk probe silently adopted unmanaged policy")
	}
}
