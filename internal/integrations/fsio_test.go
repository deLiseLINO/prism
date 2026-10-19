package integrations

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLocalIOCreateBackup(t *testing.T) {
	testCreateBackup(t, LocalIO{})
}

func testCreateBackup(t *testing.T, transport durableBackupIO) {
	t.Helper()
	t.Run("exact bytes and exclusive creation", func(t *testing.T) {
		for _, content := range []string{"", "no trailing newline", "a\r\nwith\ttabs\x00\xff\n", strings.Repeat("backup bytes\x00\n", 100000)} {
			path := filepath.Join(t.TempDir(), "quoted ' backup.json")
			if err := transport.CreateBackup(path, content); err != nil {
				t.Fatal(err)
			}
			if got := readText(t, path); got != content {
				t.Fatalf("backup bytes differ: want length %d, got length %d", len(content), len(got))
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("backup mode = %o, want 600", info.Mode().Perm())
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := transport.CreateBackup(path, "replacement"); err == nil {
				t.Fatal("existing backup was accepted")
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
				t.Fatal("existing backup mode was changed")
			}
			if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 1 {
				t.Fatal("backup creation left temporary files")
			}
			if got := readText(t, path); got != content {
				t.Fatal("existing backup was changed")
			}
		}
	})
	t.Run("existing symlinks and directory", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"target", "missing"} {
			link := filepath.Join(dir, name+".bak")
			if err := os.Symlink(filepath.Join(dir, name), link); err != nil {
				t.Fatal(err)
			}
			if err := transport.CreateBackup(link, "replacement"); err == nil {
				t.Fatal("existing symlink was accepted")
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("backup symlink was replaced")
			}
		}
		if got := readText(t, target); got != "original" {
			t.Fatal("symlink target was changed")
		}
		if _, err := os.Stat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
			t.Fatal("dangling symlink target was created")
		}
		if err := transport.CreateBackup(dir, "replacement"); err == nil {
			t.Fatal("directory was accepted as a backup")
		}
	})
	t.Run("missing parent", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "missing")
		if err := transport.CreateBackup(filepath.Join(dir, "backup"), "content"); err == nil {
			t.Fatal("missing parent was accepted")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("backup creation created the missing parent")
		}
	})
	t.Run("unreadable parent", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permissions")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "backup")
		if err := os.Chmod(dir, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if err := transport.CreateBackup(path, "content"); err == nil {
			t.Fatal("unreadable parent was accepted")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("backup created in unreadable parent: %v", err)
		}
	})
	t.Run("concurrent creation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "backup")
		start := make(chan struct{})
		winners := make(chan string, 8)
		var wg sync.WaitGroup
		for i := 0; i < cap(winners); i++ {
			wg.Add(1)
			go func(content string) {
				defer wg.Done()
				<-start
				if err := transport.CreateBackup(path, content); err == nil {
					winners <- content
				}
			}(strings.Repeat(string(rune('a'+i)), 10000))
		}
		close(start)
		wg.Wait()
		close(winners)
		if len(winners) != 1 {
			t.Fatalf("successful creators = %d, want 1", len(winners))
		}
		if got, want := readText(t, path), <-winners; got != want {
			t.Fatal("backup does not contain the successful creator's bytes")
		}
	})
}

func TestStagedPathSiblingTemp(t *testing.T) {
	got := StagedPath("/home/u/.codex/config.toml")
	want := filepath.Join("/home/u/.codex", ".config.toml.prism-tmp")
	if got != want {
		t.Fatalf("staged path:\nwant %q\ngot  %q", want, got)
	}
}

func TestDominantEol(t *testing.T) {
	if DominantEol("a\nb\nc") != EolLF {
		t.Fatalf("expected LF")
	}
	if DominantEol("a\r\nb\r\nc") != EolCRLF {
		t.Fatalf("expected CRLF")
	}
}

func TestApplyEOLRoundTrip(t *testing.T) {
	src := "alpha\nbeta\ngamma"
	if got := ApplyEol(src, EolLF); got != src {
		t.Fatalf("LF round trip failed: %q", got)
	}
	got := ApplyEol(src, EolCRLF)
	if got != "alpha\r\nbeta\r\ngamma" {
		t.Fatalf("CRLF apply failed: %q", got)
	}
}

func TestAtomicWriteAndRecover(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "config.toml")
	if err := AtomicWrite(LocalIO{}, path, "v1\n"); err != nil {
		t.Fatalf("atomic write: %v", err)
	}
	if got := readText(t, path); got != "v1\n" {
		t.Fatalf("expected v1, got %q", got)
	}
	// Simulate a crash: stage a temp without committing.
	local := LocalIO{}
	if err := local.StageWrite(path, "v2\n"); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := os.Stat(StagedPath(path)); err != nil {
		t.Fatalf("expected staged temp: %v", err)
	}
	if got := readText(t, path); got != "v1\n" {
		t.Fatalf("target changed before commit: %q", got)
	}
	if !local.RecoverStaged(path) {
		t.Fatalf("expected recovery to remove the staged temp")
	}
	if _, err := os.Stat(StagedPath(path)); !os.IsNotExist(err) {
		t.Fatalf("expected temp gone, got err=%v", err)
	}
	if got := readText(t, path); got != "v1\n" {
		t.Fatalf("recovery mutated target: %q", got)
	}
}

func TestApplyEOLCRLFSource(t *testing.T) {
	src := "a\r\nb\r\nc"
	got := ApplyEol(src, EolLF)
	if got != "a\nb\nc" {
		t.Fatalf("expected normalized LF, got %q", got)
	}
}
func TestRecoverStagedAbsent(t *testing.T) {
	local := LocalIO{}
	if local.RecoverStaged("/nonexistent/config.toml") {
		t.Fatalf("expected removed=false for absent staged file")
	}
}
