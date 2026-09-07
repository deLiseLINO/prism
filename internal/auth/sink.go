package auth

import (
	"context"
	"fmt"

	"prism/internal/account"
	"prism/internal/store"
)

// PoolRegistrar is the runtime account pool plus registration.
type PoolRegistrar interface {
	account.Pool
	Register(a account.Account)
}

// FileSink implements Sink over the file credential store, the account
// metadata repository, and the runtime pool. The credential blob is written
// first, then the metadata row; the pool is registered by the service only
// after both writes succeed.
type FileSink struct {
	file  *store.FileCredentialStore
	repos map[account.ProviderID]*account.Repository
	pool  PoolRegistrar
}

func NewFileSink(file *store.FileCredentialStore, repos map[account.ProviderID]*account.Repository, pool PoolRegistrar) *FileSink {
	return &FileSink{file: file, repos: repos, pool: pool}
}

func AccountRowID(provider account.ProviderID, identity string) account.AccountID {
	return account.AccountID(string(provider) + ":" + identity)
}

func (s *FileSink) Persist(ctx context.Context, provider account.ProviderID, cred account.Credential) (account.Account, error) {
	if cred.AccountID == "" {
		return account.Account{}, fmt.Errorf("auth: credential has no account identity")
	}
	repo, ok := s.repos[provider]
	if !ok {
		return account.Account{}, fmt.Errorf("auth: no account repository configured for %s", provider)
	}
	id := AccountRowID(provider, cred.AccountID)
	// Re-login serializes with refresh by stable provider/account identity so
	// a concurrent refresh can never interleave with the generation bump.
	release, err := withRefreshLock(ctx, s.file, provider, id, refreshWait)
	if err != nil {
		return account.Account{}, err
	}
	defer release()
	current, err := repo.CurrentGeneration(provider, id)
	if err != nil {
		return account.Account{}, err
	}
	gen := current + 1
	if err := writeGeneration(ctx, s.file, repo, provider, id, current, gen, cred.Encode(), cred.Email); err != nil {
		return account.Account{}, err
	}
	if err := repo.SetState(provider, id, account.Active); err != nil {
		return account.Account{}, err
	}
	return account.Account{
		ID:       id,
		Provider: provider,
		State:    account.Active,
		Email:    cred.Email,
		CredGen:  gen,
		Version:  1,
	}, nil
}

func (s *FileSink) Register(a account.Account) {
	s.pool.Register(a)
}

// Registered reports whether the provider has at least one durable account
// metadata row, i.e. a completed login. Startup placeholder rows that only
// exist in the runtime pool do not count.
func (s *FileSink) Registered(provider account.ProviderID) bool {
	repo, ok := s.repos[provider]
	if !ok {
		return false
	}
	rows, err := repo.Load(provider)
	return err == nil && len(rows) > 0
}
