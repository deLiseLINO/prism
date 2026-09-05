// Package integrations owns Codex, Grok, and OMP client-config mutation: honest
// status, apply, and rollback over sandboxable paths, with staged atomic writes
// and fail-closed transforms. Paths and environment are injected; nothing here
// reads process environment or HOME.
package integrations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Eol string

const (
	EolLF   Eol = "\n"
	EolCRLF Eol = "\r\n"
)

func ReadTextIfExists(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func DominantEol(content string) Eol {
	if strings.Contains(content, "\r\n") {
		return EolCRLF
	}
	return EolLF
}

func ApplyEol(content string, eol Eol) string {
	if strings.Contains(content, "\r\n") {
		content = strings.ReplaceAll(content, "\r\n", "\n")
	}
	return strings.ReplaceAll(content, "\n", string(eol))
}

func StagedPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".prism-tmp")
}

func writeFileDurable(path string, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func syncDirectory(path string) {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// StageWrite stages the next config state in a sibling temp file without touching the target.
func StageWrite(path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileDurable(StagedPath(path), content)
}

// CommitStaged renames the staged temp over the target: readers see the old or
// the new complete file, never a mix.
func CommitStaged(path string) error {
	if err := os.Rename(StagedPath(path), path); err != nil {
		return err
	}
	syncDirectory(path)
	return nil
}

func AtomicWrite(path string, content string) error {
	if err := StageWrite(path, content); err != nil {
		return err
	}
	return CommitStaged(path)
}

// RecoverStaged is the recovery pass after a crash between stage and rename:
// the target still holds the last complete state, so the staged temp is
// discarded, never promoted.
func RecoverStaged(path string) bool {
	staged := StagedPath(path)
	if !FileExists(staged) {
		return false
	}
	return os.Remove(staged) == nil
}

func failureReason(scope string, err error) string {
	return fmt.Sprintf("prism: %s failed: %s", scope, err)
}
