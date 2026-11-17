package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/deLiseLINO/prism/internal/account"
)

// ErrMoveConflict reports that both the source and the destination of an
// account move hold credentials, so neither can be chosen safely.
var ErrMoveConflict = errors.New("store: rename destination exists")

// RefreshFingerprint keys the cross-process refresh lock of one account by
// stable provider/account identity.
func RefreshFingerprint(p account.ProviderID, a account.AccountID) []byte {
	return []byte("prismd-refresh:" + string(p) + ":" + string(a))
}

// AccountPresent reports whether account a of provider p has a credential
// directory.
func (s *FileCredentialStore) AccountPresent(p account.ProviderID, a account.AccountID) (bool, error) {
	return exists(s.accountDir(p, a))
}

// MoveAccount renames the credential directory of (fromP, fromA) to the one
// of (toP, toA) while holding the refresh locks of both, so no refresh can
// publish a generation under either path mid-move. The directory is moved,
// never copied: a copied rotating refresh token would leave one copy dead.
// It reports whether anything moved. A missing source is a finished move;
// a source and a destination both present is ErrMoveConflict.
func (s *FileCredentialStore) MoveAccount(ctx context.Context, fromP account.ProviderID, fromA account.AccountID, toP account.ProviderID, toA account.AccountID) (bool, error) {
	fingerprints := [][]byte{RefreshFingerprint(fromP, fromA), RefreshFingerprint(toP, toA)}
	slices.SortFunc(fingerprints, func(a, b []byte) int { return slices.Compare(a, b) })
	for _, fp := range fingerprints {
		lock, err := s.waitRefreshLock(ctx, fp)
		if err != nil {
			return false, err
		}
		defer lock.Release()
	}
	src, dst := s.accountDir(fromP, fromA), s.accountDir(toP, toA)
	srcExists, err := exists(src)
	if err != nil || !srcExists {
		return false, err
	}
	dstExists, err := exists(dst)
	if err != nil {
		return false, err
	}
	if dstExists {
		return false, ErrMoveConflict
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, err
	}
	if err := os.Rename(src, dst); err != nil {
		return false, err
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return true, err
	}
	// Removing the emptied provider directory is best effort: other
	// accounts of the provider may still live there.
	if os.Remove(filepath.Dir(src)) != nil {
		return true, syncDir(filepath.Dir(src))
	}
	return true, syncDir(s.root)
}

func (s *FileCredentialStore) waitRefreshLock(ctx context.Context, fingerprint []byte) (*RefreshLock, error) {
	for {
		lock, err := s.AcquireRefreshLock(ctx, fingerprint)
		if !errors.Is(err, ErrLockUnavailable) {
			return lock, err
		}
	}
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
