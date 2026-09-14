package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"prism/internal/account"
)

var (
	prov  = account.ProviderID("codex")
	acct  = account.AccountID("acct-1")
	gen1  = account.CredentialGeneration(1)
	gen2  = account.CredentialGeneration(2)
	gen3  = account.CredentialGeneration(3)
	blobA = []byte("token-a")
	blobB = []byte("token-b")
)

func newStore(t *testing.T) *FileCredentialStore {
	t.Helper()
	return NewFileCredentialStore(t.TempDir())
}

func TestPutGetRoundtrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, prov, acct, gen1, blobA); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.Get(ctx, prov, acct, gen1)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got, blobA) {
		t.Fatalf("blob mismatch: got %q want %q", got, blobA)
	}
	_, ok, err = s.Get(ctx, prov, acct, gen2)
	if err != nil || ok {
		t.Fatalf("missing generation: ok=%v err=%v", ok, err)
	}
}

func TestPutNeverOverwritesExistingGeneration(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, prov, acct, gen1, blobA); err != nil {
		t.Fatalf("put: %v", err)
	}
	err := s.Put(ctx, prov, acct, gen1, blobB)
	if !errors.Is(err, ErrCredentialExists) {
		t.Fatalf("second put: got %v want ErrCredentialExists", err)
	}
	got, ok, err := s.Get(ctx, prov, acct, gen1)
	if err != nil || !ok || !bytes.Equal(got, blobA) {
		t.Fatalf("original blob altered: ok=%v got=%q err=%v", ok, got, err)
	}
}

func TestRefreshWritesNewGenerationOldRemainsReadable(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, prov, acct, gen1, blobA); err != nil {
		t.Fatalf("put gen1: %v", err)
	}
	if err := s.Put(ctx, prov, acct, gen2, blobB); err != nil {
		t.Fatalf("put gen2: %v", err)
	}
	got, ok, err := s.Get(ctx, prov, acct, gen1)
	if err != nil || !ok || !bytes.Equal(got, blobA) {
		t.Fatalf("gen1 after refresh: ok=%v got=%q err=%v", ok, got, err)
	}
	got, ok, err = s.Get(ctx, prov, acct, gen2)
	if err != nil || !ok || !bytes.Equal(got, blobB) {
		t.Fatalf("gen2: ok=%v got=%q err=%v", ok, got, err)
	}
}

func TestPutIdempotentIdenticalBytesConverge(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.PutIdempotent(ctx, prov, acct, gen1, blobA); err != nil {
		t.Fatalf("initial put: %v", err)
	}
	if err := s.PutIdempotent(ctx, prov, acct, gen1, blobA); err != nil {
		t.Fatalf("retry after rename crash with identical bytes: %v", err)
	}
	got, ok, err := s.Get(ctx, prov, acct, gen1)
	if err != nil || !ok || !bytes.Equal(got, blobA) {
		t.Fatalf("blob after idempotent retry: ok=%v got=%q err=%v", ok, got, err)
	}
}

func TestPutIdempotentDifferentBytesConflict(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, prov, acct, gen1, blobA); err != nil {
		t.Fatalf("put: %v", err)
	}
	err := s.PutIdempotent(ctx, prov, acct, gen1, blobB)
	if !errors.Is(err, ErrCredentialConflict) {
		t.Fatalf("conflicting put: got %v want ErrCredentialConflict", err)
	}
	got, ok, err := s.Get(ctx, prov, acct, gen1)
	if err != nil || !ok || !bytes.Equal(got, blobA) {
		t.Fatalf("original blob altered by conflict: ok=%v got=%q err=%v", ok, got, err)
	}
}

func TestCrashBetweenWriteAndRenameLeavesPreviousGeneration(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, prov, acct, gen1, blobA); err != nil {
		t.Fatalf("put gen1: %v", err)
	}
	dir := s.accountDir(prov, acct)
	tmp, err := writeTemp(dir, blobB)
	if err != nil {
		t.Fatalf("crashed writeTemp: %v", err)
	}
	got, ok, err := s.Get(ctx, prov, acct, gen1)
	if err != nil || !ok || !bytes.Equal(got, blobA) {
		t.Fatalf("gen1 after crash: ok=%v got=%q err=%v", ok, got, err)
	}
	_, ok, err = s.Get(ctx, prov, acct, gen2)
	if err != nil || ok {
		t.Fatalf("gen2 after crash: ok=%v err=%v", ok, err)
	}
	if err := s.Put(ctx, prov, acct, gen2, blobB); err != nil {
		t.Fatalf("retry put gen2 after crash: %v", err)
	}
	got, ok, err = s.Get(ctx, prov, acct, gen2)
	if err != nil || !ok || !bytes.Equal(got, blobB) {
		t.Fatalf("gen2 after retry: ok=%v got=%q err=%v", ok, got, err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("crashed temp missing before retry: %v", err)
	}
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("remove crashed temp: %v", err)
	}
}

func TestDeleteRemovesAllGenerationsAndIsIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for g, b := range map[account.CredentialGeneration][]byte{gen1: blobA, gen2: blobB, gen3: []byte("token-c")} {
		if err := s.Put(ctx, prov, acct, g, b); err != nil {
			t.Fatalf("put gen%d: %v", g, err)
		}
	}
	if err := s.Delete(ctx, prov, acct); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for g := range map[account.CredentialGeneration][]byte{gen1: nil, gen2: nil, gen3: nil} {
		if _, ok, err := s.Get(ctx, prov, acct, g); err != nil || ok {
			t.Fatalf("gen%d after delete: ok=%v err=%v", g, ok, err)
		}
	}
	if err := s.Delete(ctx, prov, acct); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDeleteIsolatedPerAccount(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	other := account.AccountID("acct-2")
	if err := s.Put(ctx, prov, acct, gen1, blobA); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.Put(ctx, prov, other, gen1, blobB); err != nil {
		t.Fatalf("put other: %v", err)
	}
	if err := s.Delete(ctx, prov, acct); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, ok, err := s.Get(ctx, prov, other, gen1)
	if err != nil || !ok || !bytes.Equal(got, blobB) {
		t.Fatalf("sibling account affected: ok=%v got=%q err=%v", ok, got, err)
	}
}

func acquire(t *testing.T, s *FileCredentialStore, fp []byte) (*RefreshLock, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.AcquireRefreshLock(ctx, fp)
}

func TestRefreshLockExclusiveBetweenSimulatedProcesses(t *testing.T) {
	s := newStore(t)
	fp := []byte("codex:acct-1:refresh-grant-v1")
	held := simulateForeignProcess(t, s.lockPath(fp), false)
	_, err := acquire(t, s, fp)
	if !errors.Is(err, ErrLockUnavailable) {
		t.Fatalf("process B acquire while A holds: got %v want ErrLockUnavailable", err)
	}
	releaseForeignProcess(t, held)
	lock, err := acquire(t, s, fp)
	if err != nil {
		t.Fatalf("process B acquire after A releases: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestRefreshLockKeyedByFingerprintSHA256(t *testing.T) {
	s := newStore(t)
	fp1 := []byte("codex:acct-1:grant")
	fp2 := []byte("codex:acct-2:grant")
	if s.lockPath(fp1) == s.lockPath(fp2) {
		t.Fatal("distinct fingerprints map to one lock file")
	}
	l1, err := acquire(t, s, fp1)
	if err != nil {
		t.Fatalf("acquire fp1: %v", err)
	}
	defer l1.Release()
	l2, err := acquire(t, s, fp2)
	if err != nil {
		t.Fatalf("acquire fp2 while fp1 held: %v", err)
	}
	defer l2.Release()
	other := NewFileCredentialStore(t.TempDir())
	if other.lockPath(fp1) == s.lockPath(fp1) {
		t.Fatal("lock path not scoped to store root")
	}
}

func TestStaleRefreshLockReplaced(t *testing.T) {
	s := newStore(t)
	fp := []byte("codex:acct-1:grant")
	held := simulateForeignProcess(t, s.lockPath(fp), true)
	_, err := acquire(t, s, fp)
	if err != nil {
		t.Fatalf("acquire stale lock: %v", err)
	}
	_ = held
}

func TestFreshForeignLockIsNotStolen(t *testing.T) {
	s := newStore(t)
	fp := []byte("codex:acct-1:grant")
	held := simulateForeignProcess(t, s.lockPath(fp), false)
	defer releaseForeignProcess(t, held)
	_, err := acquire(t, s, fp)
	if !errors.Is(err, ErrLockUnavailable) {
		t.Fatalf("fresh lock stolen: got %v want ErrLockUnavailable", err)
	}
}

func TestReleaseRemovesLockFile(t *testing.T) {
	s := newStore(t)
	fp := []byte("codex:acct-1:grant")
	lock, err := acquire(t, s, fp)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	path := s.lockPath(fp)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file missing while held: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock file after release: err=%v", err)
	}
	if _, err := acquire(t, s, fp); err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
}

func simulateForeignProcess(t *testing.T, path string, backdate bool) *os.File {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("lock dir: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("foreign process open: %v", err)
	}
	if err := tryLockNonBlocking(f); err != nil {
		f.Close()
		t.Fatalf("foreign process flock: %v", err)
	}
	if backdate {
		old := time.Now().Add(-staleLockAge - time.Minute)
		if err := os.Chtimes(path, old, old); err != nil {
			f.Close()
			t.Fatalf("backdate: %v", err)
		}
	}
	return f
}

func releaseForeignProcess(t *testing.T, f *os.File) {
	t.Helper()
	if err := unlock(f); err != nil {
		f.Close()
		t.Fatalf("foreign unlock: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("foreign close: %v", err)
	}
}

func TestRemoveGeneration(t *testing.T) {
	s := NewFileCredentialStore(t.TempDir())
	ctx := context.Background()
	if err := s.PutIdempotent(ctx, "codex", "a", 1, []byte(`{"version":1}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.RemoveGeneration(ctx, "codex", "a", 2); err != nil {
		t.Fatalf("remove missing generation must be a no-op: %v", err)
	}
	if err := s.RemoveGeneration(ctx, "codex", "a", 1); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok, _ := s.Get(ctx, "codex", "a", 1); ok {
		t.Fatal("generation still present after removal")
	}
}
