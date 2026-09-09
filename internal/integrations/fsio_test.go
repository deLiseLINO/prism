package integrations

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if err := AtomicWrite(path, "v1\n"); err != nil {
		t.Fatalf("atomic write: %v", err)
	}
	if got := readText(t, path); got != "v1\n" {
		t.Fatalf("expected v1, got %q", got)
	}
	// Simulate a crash: stage a temp without committing.
	if err := StageWrite(path, "v2\n"); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := os.Stat(StagedPath(path)); err != nil {
		t.Fatalf("expected staged temp: %v", err)
	}
	if got := readText(t, path); got != "v1\n" {
		t.Fatalf("target changed before commit: %q", got)
	}
	if !RecoverStaged(path) {
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
	if RecoverStaged("/nonexistent/config.toml") {
		t.Fatalf("expected removed=false for absent staged file")
	}
}
