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
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/deLiseLINO/prism/internal/account"
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

func (s *FileCredentialStore) customKeyPath(p account.ProviderID, ref string) (string, bool, error) {
	id := string(p)
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\:\x00") || filepath.IsAbs(id) {
		return "", false, errors.New("store: invalid provider path")
	}
	if ref == "" || ref == id+":default" {
		return s.blobPath(p, account.AccountID(id+":default"), 1), true, nil
	}
	revision, err := uuid.Parse(strings.TrimPrefix(ref, "prism-key:"))
	if !strings.HasPrefix(ref, "prism-key:") || err != nil || ref != "prism-key:"+revision.String() {
		return "", false, errors.New("store: invalid custom credential reference")
	}
	return filepath.Join(s.accountDir(p, account.AccountID(id+":default")), "key-"+revision.String()+".blob"), false, nil
}

func (s *FileCredentialStore) GetCustomDefaultSecret(ctx context.Context, p account.ProviderID, ref string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	path, _, err := s.customKeyPath(p, ref)
	if err != nil {
		return nil, false, err
	}
	blob, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return blob, err == nil, err
}

func (s *FileCredentialStore) CustomDefaultGeneration(ctx context.Context, p account.ProviderID, ref string) (account.CredentialGeneration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	path, legacy, err := s.customKeyPath(p, ref)
	if err != nil {
		return 0, err
	}
	if legacy {
		return 1, nil
	}
	raw, err := os.ReadFile(strings.TrimSuffix(path, ".blob") + ".generation")
	if os.IsNotExist(err) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	gen, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil || gen == 0 {
		return 0, errors.New("store: invalid custom credential generation")
	}
	return account.CredentialGeneration(gen), nil
}

func (s *FileCredentialStore) StageCustomDefaultSecret(ctx context.Context, p account.ProviderID, currentRef string, secret []byte) (string, error) {
	previous, exists, err := s.GetCustomDefaultSecret(ctx, p, currentRef)
	if err != nil {
		return "", err
	}
	if exists && bytes.Equal(previous, secret) {
		if currentRef == "" {
			currentRef = string(p) + ":default"
		}
		return currentRef, nil
	}
	gen, err := s.CustomDefaultGeneration(ctx, p, currentRef)
	if err != nil {
		return "", err
	}
	if gen == ^account.CredentialGeneration(0) {
		return "", errors.New("store: credential generation exhausted")
	}
	dir := s.accountDir(p, account.AccountID(string(p)+":default"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := writeTemp(dir, secret)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	revision := uuid.NewString()
	if err := os.Link(tmp, filepath.Join(dir, "key-"+revision+".blob")); err != nil {
		return "", err
	}
	genTmp, err := writeTemp(dir, []byte(strconv.FormatUint(uint64(gen+1), 10)))
	if err != nil {
		return "", err
	}
	defer os.Remove(genTmp)
	if err := os.Link(genTmp, filepath.Join(dir, "key-"+revision+".generation")); err != nil {
		return "", err
	}
	for path := dir; ; path = filepath.Dir(path) {
		if err := syncDir(path); err != nil {
			return "", err
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	return "prism-key:" + revision, nil
}

func (s *FileCredentialStore) Put(ctx context.Context, p account.ProviderID, a account.AccountID, g account.CredentialGeneration, blob []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	defer os.Remove(tmp)
	if err := os.Link(tmp, final); err != nil {
		if os.IsExist(err) {
			return ErrCredentialExists
		}
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
	f *os.File
}

func (s *FileCredentialStore) lockPath(fingerprint []byte) string {
	sum := sha256.Sum256(fingerprint)
	return filepath.Join(s.root, "locks", "refresh-"+hex.EncodeToString(sum[:])+".lock")
}

func (s *FileCredentialStore) AcquireRefreshLock(ctx context.Context, fingerprint []byte) (*RefreshLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := s.lockPath(fingerprint)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		err = tryLockNonBlocking(f)
		if err == nil && sameInode(f, path) {
			if err := stampLock(f, s.now()); err != nil {
				f.Close()
				return nil, err
			}
			return &RefreshLock{f: f}, nil
		}
		if err != nil && !wouldBlock(err) {
			f.Close()
			return nil, err
		}
		f.Close()
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
	err := unlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	return err
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
