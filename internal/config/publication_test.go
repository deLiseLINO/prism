package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
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

func TestCurrentAdoptsValidOutsideEdit(t *testing.T) {
	for _, bump := range []uint64{0, 1} {
		t.Run(map[uint64]string{0: "same-generation", 1: "bumped-generation"}[bump], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			mgr, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.Update(validDoc(), 0); err != nil {
				t.Fatal(err)
			}
			changes := mgr.Subscribe()
			before := mgr.Get()
			edited := before.Config
			edited.Daemon.Listen = "127.0.0.1:4999"
			writeOutsideEdit(t, path, before.Generation+bump, edited)
			got, err := mgr.Current()
			if err != nil {
				t.Fatalf("outside edit rejected: %v", err)
			}
			if got.Config.Daemon.Listen != edited.Daemon.Listen || got.Generation != before.Generation+bump {
				t.Fatalf("current=%+v", got)
			}
			if mgr.Get().Config.Daemon.Listen != edited.Daemon.Listen {
				t.Fatal("outside edit not adopted")
			}
			select {
			case <-changes:
			default:
				t.Fatal("adopted outside edit left subscribers unsignaled")
			}
			next := mgr.Get()
			next.Config.Daemon.Listen = "127.0.0.1:5000"
			if _, err := mgr.Update(next.Config, next.Generation); err != nil {
				t.Fatalf("write after adoption: %v", err)
			}
		})
	}
}

func TestCurrentKeepsLastGoodConfigWhenFileBreaks(t *testing.T) {
	invalid := validDoc()
	invalid.Daemon.Listen = "no-port"
	invalidRaw, err := json.Marshal(fileFormat{Version: SchemaVersion, Generation: 1, Config: invalid})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"corrupt": []byte("{broken"), "invalid": invalidRaw} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			path := filepath.Join(t.TempDir(), "config.json")
			mgr, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.Update(validDoc(), 0); err != nil {
				t.Fatal(err)
			}
			before := mgr.Get()
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				got, err := mgr.Current()
				if err != nil || got.Generation != before.Generation || got.Config.Daemon.Listen != before.Config.Daemon.Listen {
					t.Fatalf("broken file replaced last good config: %+v err=%v", got, err)
				}
			}
			if n := strings.Count(logs.String(), "keeping the last good config"); n != 1 {
				t.Fatalf("logged %d times: %s", n, logs.String())
			}
			fixed := before.Config
			fixed.Daemon.Listen = "127.0.0.1:4999"
			writeOutsideEdit(t, path, before.Generation, fixed)
			if got, err := mgr.Current(); err != nil || got.Config.Daemon.Listen != fixed.Daemon.Listen {
				t.Fatalf("repaired file not adopted: %+v err=%v", got, err)
			}
		})
	}
}

func writeOutsideEdit(t *testing.T, path string, generation uint64, doc Document) {
	t.Helper()
	raw, err := json.MarshalIndent(fileFormat{Version: SchemaVersion, Generation: generation, Config: doc}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
