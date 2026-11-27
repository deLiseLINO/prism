package auth

import (
	"bytes"
	"context"
	"errors"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/store"
)

// Replica is what the main machine pushes for one account it owns. A nil
// Credential means the main machine has none to give (it lost the grant and
// waits for a new login).
type Replica struct {
	Provider   account.ProviderID
	ID         account.AccountID
	Email      string
	State      account.State
	Priority   int
	Credential *account.Credential
}

// PublishReplica stores r on this machine under the account's refresh lock,
// the path a local refresh takes: the credential moves to the next local
// generation and the metadata row is marked as a replica. The main machine's
// generation numbers are never copied, so a re-login there, which restarts
// its count, still wins here. A credential equal to the current one stores
// nothing new, and neither does one with the same grant that expires no later
// than the current one, which this machine already renewed itself. An account
// signed in on this machine is refused with account.ErrLocalAccount. It
// returns the current generation (0 when nothing is stored) and whether the
// credential changed.
func PublishReplica(ctx context.Context, file *store.FileCredentialStore, repo *account.Repository, r Replica) (account.CredentialGeneration, bool, error) {
	publication, err := lockPublication(ctx, file, r.Provider, r.ID, refreshWait)
	if err != nil {
		return 0, false, err
	}
	defer publication.lock.Release()
	current, err := repo.CurrentGeneration(r.Provider, r.ID)
	if err != nil {
		return 0, false, err
	}
	if current > 0 {
		replica, err := repo.IsReplica(r.Provider, r.ID)
		if err != nil {
			return 0, false, err
		}
		if !replica {
			return 0, false, account.ErrLocalAccount
		}
	}
	if r.Credential == nil {
		if current == 0 {
			return 0, false, nil
		}
		return current, false, repo.PutReplica(r.Provider, r.ID, current, current, r.Email, r.State, r.Priority)
	}
	blob := r.Credential.Encode()
	if current > 0 {
		stored, ok, err := file.Get(ctx, r.Provider, r.ID, current)
		if err != nil {
			return 0, false, err
		}
		if ok && keepStored(stored, *r.Credential, blob) {
			return current, false, repo.PutReplica(r.Provider, r.ID, current, current, r.Email, r.State, r.Priority)
		}
	}
	next := current + 1
	if _, ok, err := file.Get(ctx, r.Provider, r.ID, next); err != nil {
		return 0, false, err
	} else if ok {
		if err := file.RemoveGeneration(ctx, r.Provider, r.ID, next); err != nil {
			return 0, false, err
		}
	}
	if err := file.PutIdempotent(ctx, r.Provider, r.ID, next, blob); err != nil {
		return 0, false, err
	}
	if err := repo.PutReplica(r.Provider, r.ID, current, next, r.Email, r.State, r.Priority); err != nil {
		return 0, false, err
	}
	return next, true, nil
}

func keepStored(stored []byte, incoming account.Credential, blob []byte) bool {
	cur, err := account.ParseCredential(stored)
	if err != nil {
		return false
	}
	if bytes.Equal(cur.Encode(), blob) {
		return true
	}
	return cur.Refresh != "" && cur.Refresh == incoming.Refresh && !incoming.ExpiresAt.After(cur.ExpiresAt)
}

// DeleteReplica removes the stored credentials and metadata of a replica the
// main machine no longer has. An account signed in on this machine is refused
// with account.ErrLocalAccount; a missing one is already gone.
func DeleteReplica(ctx context.Context, file *store.FileCredentialStore, repo *account.Repository, p account.ProviderID, id account.AccountID) error {
	publication, err := lockPublication(ctx, file, p, id, refreshWait)
	if err != nil {
		return err
	}
	defer publication.lock.Release()
	current, err := repo.CurrentGeneration(p, id)
	if err != nil {
		return err
	}
	if current > 0 {
		replica, err := repo.IsReplica(p, id)
		if err != nil {
			return err
		}
		if !replica {
			return account.ErrLocalAccount
		}
	}
	if err := file.Delete(ctx, p, id); err != nil {
		return err
	}
	if err := repo.Delete(p, id); err != nil && !errors.Is(err, account.ErrNotFound) {
		return err
	}
	return nil
}
