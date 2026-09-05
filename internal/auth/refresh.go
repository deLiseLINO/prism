package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"prism/internal/account"
	"prism/internal/store"
)

// refreshWait is the pause between refresh-lock acquisition rounds once the
// store's own bounded retry budget is exhausted.
const refreshWait = 25 * time.Millisecond

// refreshFingerprint keys the cross-process refresh lock by stable
// provider/account identity, so a re-login that rotates the refresh grant
// serializes against in-flight refreshes instead of racing them.
func refreshFingerprint(p account.ProviderID, id account.AccountID) []byte {
	return []byte("prismd-refresh:" + string(p) + ":" + string(id))
}

// RefreshPool is the runtime pool surface the refresher needs.
type RefreshPool interface {
	AdvanceGeneration(id account.AccountID, gen account.CredentialGeneration) error
	MarkNeedsReauth(id account.AccountID) error
}

// RefresherOptions configures a Refresher.
type RefresherOptions struct {
	File  *store.FileCredentialStore
	Repos map[account.ProviderID]*account.Repository
	Pool  RefreshPool
	Flows map[account.ProviderID]Flow
	Now   func() time.Time
	Wait  time.Duration
}

// Refresher is the context-aware credential source behind the Codex and
// Antigravity runners. It reads the lease generation record, returns fresh
// credentials unchanged, and renews expiring ones under the store's
// cross-process account refresh lock: re-read the repository generation,
// adopt a newer record when another actor already refreshed, otherwise call
// the provider flow's refresh endpoint, write generation N+1, and advance the
// runtime pool.
type Refresher struct {
	file  *store.FileCredentialStore
	repos map[account.ProviderID]*account.Repository
	pool  RefreshPool
	flows map[account.ProviderID]Flow
	now   func() time.Time
	wait  time.Duration
}

func NewRefresher(opts RefresherOptions) (*Refresher, error) {
	if opts.File == nil {
		return nil, fmt.Errorf("auth: refresher requires a credential store")
	}
	if opts.Pool == nil {
		return nil, fmt.Errorf("auth: refresher requires a runtime pool")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	wait := opts.Wait
	if wait <= 0 {
		wait = refreshWait
	}
	return &Refresher{
		file:  opts.File,
		repos: opts.Repos,
		pool:  opts.Pool,
		flows: opts.Flows,
		now:   now,
		wait:  wait,
	}, nil
}

// Credential returns the credential for the lease's account. A credential
// expiring outside the provider's skew window is returned unchanged; an
// expiring one is refreshed at most once per account across processes.
func (r *Refresher) Credential(ctx context.Context, lease account.Lease) (account.Credential, error) {
	if err := ctx.Err(); err != nil {
		return account.Credential{}, err
	}
	if lease.CredGen == 0 {
		return account.Credential{}, fmt.Errorf("auth: lease for %s/%s carries no credential generation", lease.Provider, lease.Account)
	}
	repo := r.repos[lease.Provider]
	if repo == nil {
		return account.Credential{}, fmt.Errorf("auth: no account repository configured for %s", lease.Provider)
	}
	flow, ok := r.flows[lease.Provider]
	if !ok {
		return account.Credential{}, fmt.Errorf("auth: unknown provider %s", lease.Provider)
	}
	rf, ok := flow.(RefreshFlow)
	if !ok {
		return account.Credential{}, fmt.Errorf("auth: provider %s does not support credential refresh", lease.Provider)
	}
	if blob, ok, err := r.file.Get(ctx, lease.Provider, lease.Account, lease.CredGen); err != nil {
		return account.Credential{}, err
	} else if ok {
		cred, err := account.ParseCredential(blob)
		if err != nil {
			return account.Credential{}, err
		}
		if fresh(cred, rf, r.now) {
			return cred, nil
		}
	}
	release, err := withRefreshLock(ctx, r.file, lease.Provider, lease.Account, r.wait)
	if err != nil {
		return account.Credential{}, err
	}
	defer release()
	return r.refreshLocked(ctx, lease, repo, rf)
}

// fresh reports whether the credential may be dispatched: legacy tokens
// without an expiry never expire, otherwise the expiry must lie beyond the
// provider's skew window.
func fresh(cred account.Credential, rf RefreshFlow, now func() time.Time) bool {
	return cred.ExpiresAt.IsZero() || now().Before(cred.ExpiresAt.Add(-rf.RefreshSkew()))
}

// withRefreshLock holds the store's account refresh lock, retrying past the
// store's bounded budget until the holder finishes or ctx ends. The lock is
// the only serialization point: no in-process mutex complements it.
func withRefreshLock(ctx context.Context, file *store.FileCredentialStore, p account.ProviderID, id account.AccountID, wait time.Duration) (func(), error) {
	fp := refreshFingerprint(p, id)
	for {
		lock, err := file.AcquireRefreshLock(ctx, fp)
		if err == nil {
			return func() { _ = lock.Release() }, nil
		}
		if !errors.Is(err, store.ErrLockUnavailable) {
			return nil, err
		}
		if err := waitRefresh(ctx, wait); err != nil {
			return nil, err
		}
	}
}

func waitRefresh(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// refreshLocked runs under the account refresh lock.
func (r *Refresher) refreshLocked(ctx context.Context, lease account.Lease, repo *account.Repository, rf RefreshFlow) (account.Credential, error) {
	p, id := lease.Provider, lease.Account
	gen, err := repo.CurrentGeneration(p, id)
	if err != nil {
		return account.Credential{}, err
	}
	if gen == 0 {
		return account.Credential{}, fmt.Errorf("auth: account %s/%s has no credential generation", p, id)
	}
	if gen > lease.CredGen {
		// Another actor (re-login or refresh) advanced the generation.
		cred, err := r.load(ctx, p, id, gen)
		if err != nil {
			return account.Credential{}, err
		}
		if err := r.pool.AdvanceGeneration(id, gen); err != nil {
			return account.Credential{}, err
		}
		if fresh(cred, rf, r.now) {
			return cred, nil
		}
		lease.CredGen = gen
	}
	prev, err := r.load(ctx, p, id, gen)
	if err != nil {
		return account.Credential{}, err
	}
	if fresh(prev, rf, r.now) {
		return prev, nil
	}
	if prev.Refresh == "" {
		return account.Credential{}, fmt.Errorf("auth: credential for %s/%s has no refresh grant; reauthenticate", p, id)
	}
	next, err := rf.Refresh(ctx, prev)
	if err != nil {
		return account.Credential{}, r.classifyRefreshError(ctx, err, p, id, repo)
	}
	if next.Access == "" || next.ExpiresAt.IsZero() {
		return account.Credential{}, fmt.Errorf("auth: refresh response for %s/%s is incomplete", p, id)
	}
	if prev.AccountID != "" && next.AccountID != "" && next.AccountID != prev.AccountID {
		return account.Credential{}, fmt.Errorf("auth: refresh returned a credential for a different account")
	}
	if next.AccountID == "" {
		next.AccountID = prev.AccountID
	}
	nextGen := gen + 1
	if err := writeGeneration(ctx, r.file, repo, p, id, gen, nextGen, next.Encode(), next.Email); err != nil {
		return account.Credential{}, err
	}
	if err := r.pool.AdvanceGeneration(id, nextGen); err != nil {
		return account.Credential{}, err
	}
	return next, nil
}

func (r *Refresher) load(ctx context.Context, p account.ProviderID, id account.AccountID, gen account.CredentialGeneration) (account.Credential, error) {
	blob, ok, err := r.file.Get(ctx, p, id, gen)
	if err != nil {
		return account.Credential{}, err
	}
	if !ok {
		return account.Credential{}, fmt.Errorf("auth: credential generation %d for %s/%s is missing", gen, p, id)
	}
	return account.ParseCredential(blob)
}

// classifyRefreshError maps a failed refresh to typed errors. Invalid grants
// tear down the account; transient network, timeout, and server failures stay
// retryable and leave the prior generation intact; every other failure is
// fail-closed without touching stored state.
func (r *Refresher) classifyRefreshError(ctx context.Context, err error, p account.ProviderID, id account.AccountID, repo *account.Repository) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var te *TokenError
	if errors.As(err, &te) {
		switch {
		case te.Code == codeInvalidGrant:
			if ierr := r.invalidate(ctx, p, id, repo); ierr != nil {
				return fmt.Errorf("%w: revoking the account failed: %v", account.ErrNeedsReauth, ierr)
			}
			return fmt.Errorf("%w: provider rejected the refresh grant", account.ErrNeedsReauth)
		case te.Status >= 500 || te.Status == http.StatusTooManyRequests:
			return fmt.Errorf("%w: token endpoint returned status %d", account.ErrRefreshTransient, te.Status)
		default:
			return err
		}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%w: %v", account.ErrRefreshTransient, err)
	}
	return err
}

// invalidate drops the rejected grant: credential blobs are removed, the
// repository row is marked needs_reauth, and the runtime account stops
// dispatching with its affinity entries dropped.
func (r *Refresher) invalidate(ctx context.Context, p account.ProviderID, id account.AccountID, repo *account.Repository) error {
	if err := r.file.Delete(ctx, p, id); err != nil {
		return err
	}
	if err := repo.SetState(p, id, account.NeedsReauth); err != nil {
		return err
	}
	return r.pool.MarkNeedsReauth(id)
}

// writeGeneration stores blob as gen and points the repository row at it. Any
// blob already occupying gen is an unreferenced orphan from a crash between
// the blob write and the repository update; the caller holds the account
// refresh lock, so clearing it cannot race a concurrent writer.
func writeGeneration(ctx context.Context, file *store.FileCredentialStore, repo *account.Repository, p account.ProviderID, id account.AccountID, prevGen, gen account.CredentialGeneration, blob []byte, email string) error {
	if _, ok, err := file.Get(ctx, p, id, gen); err != nil {
		return err
	} else if ok {
		if err := file.RemoveGeneration(ctx, p, id, gen); err != nil {
			return err
		}
	}
	if err := file.PutIdempotent(ctx, p, id, gen, blob); err != nil {
		return err
	}
	return repo.CASGeneration(p, id, prevGen, gen, email)
}
