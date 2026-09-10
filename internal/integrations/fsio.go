// Package integrations owns Codex, Grok, and OMP client-config mutation: honest
// status, apply, and rollback over sandboxable paths, with staged atomic writes
// and fail-closed transforms. Paths, environment, and file IO are injected;
// nothing here reads process environment, HOME, or touches the local disk
// directly.
package integrations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileIO is the transport every config read and write goes through: the local
// disk (LocalIO) or a remote host (SshIO). Implementations keep the staged
// atomic-write contract: StageWrite leaves the target untouched, CommitStaged
// swaps in the complete next state in one step, RecoverStaged discards an
// orphaned stage file, never promoting it.
type FileIO interface {
	ReadTextIfExists(path string) (string, bool)
	FileExists(path string) bool
	StageWrite(path string, content string) error
	CommitStaged(path string) error
	RecoverStaged(path string) bool
	Remove(path string) error
}

// LocalIO is the local-disk FileIO.
type LocalIO struct{}

func (LocalIO) ReadTextIfExists(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (LocalIO) FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (LocalIO) StageWrite(path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileDurable(StagedPath(path), content)
}

func (LocalIO) CommitStaged(path string) error {
	if err := os.Rename(StagedPath(path), path); err != nil {
		return err
	}
	syncDirectory(path)
	return nil
}

func (LocalIO) RecoverStaged(path string) bool {
	staged := StagedPath(path)
	if _, err := os.Stat(staged); err != nil {
		return false
	}
	return os.Remove(staged) == nil
}

func (LocalIO) Remove(path string) error {
	return os.Remove(path)
}

// withLocalIO defaults a nil FileIO to the local disk so existing callers and
// tests construct modules unchanged.
func withLocalIO(io FileIO) FileIO {
	if io == nil {
		return LocalIO{}
	}
	return io
}

type Eol string

const (
	EolLF   Eol = "\n"
	EolCRLF Eol = "\r\n"
)

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

func AtomicWrite(io FileIO, path string, content string) error {
	if err := io.StageWrite(path, content); err != nil {
		return err
	}
	return io.CommitStaged(path)
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

func failureReason(scope string, err error) string {
	return fmt.Sprintf("prism: %s failed: %s", scope, err)
}
