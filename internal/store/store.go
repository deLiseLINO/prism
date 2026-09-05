package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"prism/internal/account"
)

type CredentialStore interface {
	Put(ctx context.Context, p account.ProviderID, a account.AccountID, g account.CredentialGeneration, blob []byte) error
	Get(ctx context.Context, p account.ProviderID, a account.AccountID, g account.CredentialGeneration) ([]byte, bool, error)
	Delete(ctx context.Context, p account.ProviderID, a account.AccountID) error
}

var (
	ErrCredentialExists   = errors.New("store: credential generation already exists")
	ErrCredentialConflict = errors.New("store: credential generation exists with different bytes")
	ErrLockUnavailable    = errors.New("store: refresh lock held by another process")
)

func (s *FileCredentialStore) PutIdempotent(ctx context.Context, p account.ProviderID, a account.AccountID, g account.CredentialGeneration, blob []byte) error {
	for {
		err := s.Put(ctx, p, a, g, blob)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrCredentialExists) {
			return err
		}
		existing, ok, gerr := s.Get(ctx, p, a, g)
		if gerr != nil {
			return gerr
		}
		if !ok {
			continue
		}
		if !bytes.Equal(existing, blob) {
			return ErrCredentialConflict
		}
		return nil
	}
}

const (
	staleLockAge = 60 * time.Second
	lockAttempts = 8
	lockDelay    = 20 * time.Millisecond
)

type FileCredentialStore struct {
	root string
	now  func() time.Time
}

func NewFileCredentialStore(root string) *FileCredentialStore {
	return &FileCredentialStore{root: root, now: time.Now}
}

func (s *FileCredentialStore) accountDir(p account.ProviderID, a account.AccountID) string {
	return filepath.Join(s.root, string(p), string(a))
}

func (s *FileCredentialStore) blobPath(p account.ProviderID, a account.AccountID, g account.CredentialGeneration) string {
	return filepath.Join(s.accountDir(p, a), "gen-"+strconv.FormatUint(uint64(g), 10)+".blob")
}

func (s *FileCredentialStore) Put(ctx context.Context, p account.ProviderID, a account.AccountID, g account.CredentialGeneration, blob []byte) error {
	if p == "" || a == "" {
		return fmt.Errorf("store: empty key: provider %q account %q", p, a)
	}
	dir := s.accountDir(p, a)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	final := s.blobPath(p, a, g)
	if _, err := os.Lstat(final); err == nil {
		return ErrCredentialExists
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := writeTemp(dir, blob)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

func (s *FileCredentialStore) Get(ctx context.Context, p account.ProviderID, a account.AccountID, g account.CredentialGeneration) ([]byte, bool, error) {
	blob, err := os.ReadFile(s.blobPath(p, a, g))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return blob, true, nil
}

// RemoveGeneration deletes one credential generation blob. A crash between
// the blob write and the repository update can leave a generation the
// repository never references; the refresh-lock holder clears it here before
// retrying the write.
func (s *FileCredentialStore) RemoveGeneration(ctx context.Context, p account.ProviderID, a account.AccountID, g account.CredentialGeneration) error {
	err := os.Remove(s.blobPath(p, a, g))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDir(s.accountDir(p, a))
}

func (s *FileCredentialStore) Delete(ctx context.Context, p account.ProviderID, a account.AccountID) error {
	if err := os.RemoveAll(s.accountDir(p, a)); err != nil {
		return err
	}
	return nil
}

func writeTemp(dir string, blob []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.Write(blob); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

type RefreshLock struct {
	path string
	f    *os.File
}

func (s *FileCredentialStore) lockPath(fingerprint []byte) string {
	sum := sha256.Sum256(fingerprint)
	return filepath.Join(s.root, "locks", "refresh-"+hex.EncodeToString(sum[:])+".lock")
}

func (s *FileCredentialStore) AcquireRefreshLock(ctx context.Context, fingerprint []byte) (*RefreshLock, error) {
	path := s.lockPath(fingerprint)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil && sameInode(f, path) {
			if err := stampLock(f, s.now()); err != nil {
				f.Close()
				return nil, err
			}
			return &RefreshLock{path: path, f: f}, nil
		}
		if err != nil && !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		held := err != nil
		f.Close()
		if held {
			if stale, statErr := isStale(path, s.now()); statErr == nil && stale {
				if rmErr := os.Remove(path); rmErr == nil || os.IsNotExist(rmErr) {
					continue
				}
			}
		}
		if attempt+1 >= lockAttempts {
			return nil, ErrLockUnavailable
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockDelay):
		}
	}
}

func (l *RefreshLock) Release() error {
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	if rerr := os.Remove(l.path); rerr != nil && !os.IsNotExist(rerr) && err == nil {
		err = rerr
	}
	return err
}

func isStale(path string, now time.Time) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return now.Sub(st.ModTime()) > staleLockAge, nil
}

func stampLock(f *os.File, now time.Time) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	_, err := f.WriteString("pid=" + strconv.Itoa(os.Getpid()) + " ts=" + strconv.FormatInt(now.UnixNano(), 10) + "\n")
	return err
}

func sameInode(f *os.File, path string) bool {
	var fStat, pStat syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &fStat); err != nil {
		return false
	}
	if err := syscall.Stat(path, &pStat); err != nil {
		return false
	}
	return fStat.Ino == pStat.Ino
}
