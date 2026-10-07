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
	ReadText(path string) (string, bool, error)
	FileExists(path string) bool
	StageWrite(path string, content string) error
	CommitStaged(path string) error
	RecoverStaged(path string) bool
	Remove(path string) error
	RemoveDurable(path string) error
}

// LocalIO is the local-disk FileIO.
type LocalIO struct{}

func (LocalIO) ReadText(path string) (string, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

func (LocalIO) FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (LocalIO) StageWrite(path string, content string) error {
	if err := makeDirectoriesDurable(filepath.Dir(path)); err != nil {
		return err
	}
	return writeFileDurable(StagedPath(path), content)
}

func (LocalIO) CommitStaged(path string) error {
	if err := os.Rename(StagedPath(path), path); err != nil {
		return err
	}
	return syncDirectoryChecked(path)
}

func makeDirectoriesDurable(dir string) error {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(dir)
	if err := makeDirectoriesDurable(parent); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		return err
	}
	if err := syncDirectoryChecked(filepath.Join(dir, "entry")); err != nil {
		return err
	}
	return syncDirectoryChecked(dir)
}

func (LocalIO) RecoverStaged(path string) bool {
	staged := StagedPath(path)
	if _, err := os.Stat(staged); err != nil {
		return false
	}
	return os.Remove(staged) == nil
}

func (LocalIO) Remove(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (l LocalIO) RemoveDurable(path string) error {
	if err := l.Remove(path); err != nil {
		return err
	}
	return syncDirectoryChecked(path)
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
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
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

func syncDirectoryChecked(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = d.Sync()
	closeErr := d.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func failureReason(scope string, err error) string {
	return fmt.Sprintf("prism: %s failed: %s", scope, err)
}
