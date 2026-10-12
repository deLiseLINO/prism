package integrations

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shellFileIO() *SshIO {
	return &SshIO{address: "fixture", run: func(ctx context.Context, address, stdin, script string) (string, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if exit, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("shell: %s", exit.Stderr)
		}
		return string(out), err
	}}
}

func TestSshIOCreateBackup(t *testing.T) {
	testCreateBackup(t, shellFileIO())
}

func TestSshIOCreateBackupRejectsInvalidInputBeforeCreation(t *testing.T) {
	transport := shellFileIO()
	run := transport.run
	transport.run = func(ctx context.Context, address, stdin, script string) (string, error) {
		return run(ctx, address, "cGFydGlhbA==\n%%%", script)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "backup")
	if err := transport.CreateBackup(path, "private content"); err == nil {
		t.Fatal("invalid base64 was accepted")
	} else if strings.Contains(err.Error(), "private content") || strings.Contains(err.Error(), "partial") {
		t.Fatal("backup content leaked in the error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("failed decoding left a backup or temporary file")
	}
}

func TestSshIOCreateBackupRequiresFileAndParentSync(t *testing.T) {
	for _, failCall := range []int{1, 2} {
		t.Run(fmt.Sprintf("sync call %d", failCall), func(t *testing.T) {
			dir := t.TempDir()
			commands := t.TempDir()
			logPath := filepath.Join(commands, "calls")
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$2\" >> %s\ntest \"$(wc -l < %s)\" -ne %d\n", shq(logPath), shq(logPath), failCall)
			if err := os.WriteFile(filepath.Join(commands, "sync"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			transport := shellFileIO()
			run := transport.run
			transport.run = func(ctx context.Context, address, stdin, script string) (string, error) {
				return run(ctx, address, stdin, "PATH="+shq(commands)+":\"$PATH\"; "+script)
			}
			path := filepath.Join(dir, "backup")
			if err := transport.CreateBackup(path, "exact bytes"); err == nil {
				t.Fatal("sync failure was accepted")
			}
			if got := readText(t, path); got != "exact bytes" {
				t.Fatal("failed sync changed the saved bytes")
			}
			wantCalls := path + "\n"
			if failCall == 2 {
				wantCalls += dir + "\n"
			}
			if got := readText(t, logPath); got != wantCalls {
				t.Fatalf("sync targets = %q, want %q", got, wantCalls)
			}
			if err := transport.CreateBackup(path, "replacement"); err == nil {
				t.Fatal("retry overwrote the unsynced backup")
			}
			if got := readText(t, path); got != "exact bytes" {
				t.Fatal("retry changed the saved bytes")
			}
		})
	}
}

func TestSshIOCreateBackupHonorsTimeout(t *testing.T) {
	transport := shellFileIO()
	transport.timeout = 100 * time.Millisecond
	run := transport.run
	transport.run = func(ctx context.Context, address, stdin, script string) (string, error) {
		return run(ctx, address, stdin, "exec sleep 5")
	}
	path := filepath.Join(t.TempDir(), "backup")
	if err := transport.CreateBackup(path, "private content"); err == nil {
		t.Fatal("timed out creation succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("timed out creation wrote a backup")
	}
}

func TestSshIOFilesystemLifecycle(t *testing.T) {
	io := shellFileIO()
	path := filepath.Join(t.TempDir(), "quoted ' name.json")
	for _, text := range []string{"", "no newline", "a\r\nwith\ttabs\x00\n"} {
		if err := io.StageWrite(path, text); err != nil {
			t.Fatal(err)
		}
		if err := io.CommitStaged(path); err != nil {
			t.Fatal(err)
		}
		got, present, err := io.ReadText(path)
		if err != nil || !present || got != text {
			t.Fatalf("read %q %v %v", got, present, err)
		}
	}
	if err := io.StageWrite(path, "discard"); err != nil {
		t.Fatal(err)
	}
	if !io.RecoverStaged(path) {
		t.Fatal("stage not discarded")
	}
	if err := io.RemoveDurable(path); err != nil {
		t.Fatal(err)
	}
	if _, present, err := io.ReadText(path); err != nil || present {
		t.Fatalf("absent %v %v", present, err)
	}
	if err := io.RemoveDurable(path); err != nil {
		t.Fatal("absent retirement retry", err)
	}
}

func TestSshIOReadFailureIsNotAbsence(t *testing.T) {
	io := shellFileIO()
	dir := t.TempDir()
	if _, present, err := io.ReadText(dir); err == nil || present {
		t.Fatalf("directory read must fail: %v %v", present, err)
	}
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, present, err := io.ReadText(filepath.Join(path, "child")); err == nil || present {
		t.Fatalf("non-directory ancestor must fail: %v %v", present, err)
	}
}
