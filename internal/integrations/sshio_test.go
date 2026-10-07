package integrations

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
