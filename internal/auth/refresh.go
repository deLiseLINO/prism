package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/store"
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
	// RefreshFlows carries refresh-only flows for providers whose login is
	// external to prism (credentials imported from the vendor CLI).
	RefreshFlows map[account.ProviderID]RefreshFlow
	Now          func() time.Time
	Wait         time.Duration
}

// Refresher is the context-aware credential source behind the Codex and
// Antigravity runners. It reads the lease generation record, returns fresh
// credentials unchanged, and renews expiring ones under the store's
// cross-process account refresh lock: re-read the repository generation,
// adopt a newer record when another actor already refreshed, otherwise call
// the provider flow's refresh endpoint, write generation N+1, and advance the
// runtime pool.
type Refresher struct {
	file         *store.FileCredentialStore
	repos        map[account.ProviderID]*account.Repository
	pool         RefreshPool
	flows        map[account.ProviderID]Flow
	refreshFlows map[account.ProviderID]RefreshFlow
	now          func() time.Time
	wait         time.Duration
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
		file:         opts.File,
		repos:        opts.Repos,
		pool:         opts.Pool,
		flows:        opts.Flows,
		refreshFlows: opts.RefreshFlows,
		now:          now,
		wait:         wait,
	}, nil
}

// Credential returns the credential for the lease's account. A credential
// expiring outside the provider's skew window is returned unchanged; an
// expiring one is refreshed at most once per account across processes.
func (r *Refresher) Credential(ctx context.Context, lease account.Lease) (account.Credential, error) {
	return r.credential(ctx, lease, "")
}

// RefreshRejected renews the credential after the upstream refused rejected
// (an access token the expiry check still considered fresh, e.g. revoked or
// rotated server-side). A newer credential another actor already stored is
// returned as is; otherwise the refresh grant is exchanged regardless of the
// recorded expiry, at most once per account across processes.
func (r *Refresher) RefreshRejected(ctx context.Context, lease account.Lease, rejected string) (account.Credential, error) {
	return r.credential(ctx, lease, rejected)
}

func (r *Refresher) credential(ctx context.Context, lease account.Lease, rejected string) (account.Credential, error) {
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
	var rf RefreshFlow
	if flow, ok := r.flows[lease.Provider]; ok {
		rf, ok = flow.(RefreshFlow)
		if !ok {
			return account.Credential{}, fmt.Errorf("auth: provider %s does not support credential refresh", lease.Provider)
		}
	} else if only, ok := r.refreshFlows[lease.Provider]; ok {
		rf = only
	} else {
		return account.Credential{}, fmt.Errorf("auth: unknown provider %s", lease.Provider)
	}
	current, err := repo.CurrentGeneration(lease.Provider, lease.Account)
	if err != nil {
		return account.Credential{}, err
	}
	if current == 0 {
		return account.Credential{}, account.ErrNotFound
	}
	if current < lease.CredGen {
		return account.Credential{}, account.ErrStale
	}
	if current > lease.CredGen {
		lease.CredGen = current
		if err := r.pool.AdvanceGeneration(lease.Account, current); err != nil {
			return account.Credential{}, err
		}
	}
	if blob, ok, err := r.file.Get(ctx, lease.Provider, lease.Account, lease.CredGen); err != nil {
		return account.Credential{}, err
	} else if ok {
		cred, err := account.ParseCredential(blob)
		if err != nil {
			return account.Credential{}, err
		}
		cred.Generation = lease.CredGen
		if usable(cred, rf, r.now, rejected) {
			return cred, nil
		}
	}
	publication, err := lockPublication(ctx, r.file, lease.Provider, lease.Account, r.wait)
	if err != nil {
		return account.Credential{}, err
	}
	defer publication.lock.Release()
	return r.refreshLocked(ctx, lease, repo, rf, rejected, publication)
}

// fresh reports whether the credential may be dispatched: legacy tokens
// without an expiry never expire, otherwise the expiry must lie beyond the
// provider's skew window.
func fresh(cred account.Credential, rf RefreshFlow, now func() time.Time) bool {
	return cred.ExpiresAt.IsZero() || now().Before(cred.ExpiresAt.Add(-rf.RefreshSkew()))
}

func usable(cred account.Credential, rf RefreshFlow, now func() time.Time, rejected string) bool {
	return fresh(cred, rf, now) && (rejected == "" || cred.Access != rejected)
}

type credentialPublication struct {
	file     *store.FileCredentialStore
	provider account.ProviderID
	account  account.AccountID
	lock     *store.RefreshLock
}

func lockPublication(ctx context.Context, file *store.FileCredentialStore, p account.ProviderID, id account.AccountID, wait time.Duration) (credentialPublication, error) {
	fp := refreshFingerprint(p, id)
	for {
		lock, err := file.AcquireRefreshLock(ctx, fp)
		if err == nil {
			return credentialPublication{file: file, provider: p, account: id, lock: lock}, nil
		}
		if !errors.Is(err, store.ErrLockUnavailable) {
			return credentialPublication{}, err
		}
		if err := waitRefresh(ctx, wait); err != nil {
			return credentialPublication{}, err
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

func (r *Refresher) refreshLocked(ctx context.Context, lease account.Lease, repo *account.Repository, rf RefreshFlow, rejected string, publication credentialPublication) (account.Credential, error) {
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
		if usable(cred, rf, r.now, rejected) {
			return cred, nil
		}
		lease.CredGen = gen
	}
	prev, err := r.load(ctx, p, id, gen)
	if err != nil {
		return account.Credential{}, err
	}
	if usable(prev, rf, r.now, rejected) {
		return prev, nil
	}
	if prev.Refresh == "" {
		if rejected != "" {
			return account.Credential{}, fmt.Errorf("%w: access token rejected and no refresh grant is stored", account.ErrNeedsReauth)
		}
		return account.Credential{}, fmt.Errorf("auth: credential for %s/%s has no refresh grant; reauthenticate", p, id)
	}
	next, err := rf.Refresh(ctx, prev)
	if err != nil {
		return account.Credential{}, r.classifyRefreshError(ctx, err, p, id, repo)
	}
	if next.Access == "" || next.ExpiresAt.IsZero() {
		return account.Credential{}, fmt.Errorf("auth: refresh response for %s/%s is incomplete", p, id)
	}
	if prev.AccountID != "" && next.AccountID != "" && next.AccountID != prev.AccountID && !importedIdentity(prev.AccountID) {
		return account.Credential{}, fmt.Errorf("auth: refresh returned a credential for a different account")
	}
	if next.AccountID == "" {
		next.AccountID = prev.AccountID
	}
	nextGen := gen + 1
	if err := publication.write(ctx, repo, gen, nextGen, next.Encode(), next.Email, false); err != nil {
		return account.Credential{}, err
	}
	if err := r.pool.AdvanceGeneration(id, nextGen); err != nil {
		return account.Credential{}, err
	}
	next.Generation = nextGen
	return next, nil
}

// importedIdentity marks credentials whose vendor file carried no account
// identity; the first refresh adopts the provider-reported one.
func importedIdentity(id string) bool {
	return id == "imported"
}

func (r *Refresher) load(ctx context.Context, p account.ProviderID, id account.AccountID, gen account.CredentialGeneration) (account.Credential, error) {
	blob, ok, err := r.file.Get(ctx, p, id, gen)
	if err != nil {
		return account.Credential{}, err
	}
	if !ok {
		return account.Credential{}, fmt.Errorf("auth: credential generation %d for %s/%s is missing", gen, p, id)
	}
	cred, err := account.ParseCredential(blob)
	cred.Generation = gen
	return cred, err
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

func (publication credentialPublication) write(ctx context.Context, repo *account.Repository, prevGen, gen account.CredentialGeneration, blob []byte, email string, login bool) error {
	if publication.lock == nil {
		return errors.New("auth: credential publication requires account lock")
	}
	file, p, id := publication.file, publication.provider, publication.account
	current, err := repo.CurrentGeneration(p, id)
	if err != nil {
		return err
	}
	if current != prevGen {
		return account.ErrStale
	}
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
	if login {
		return repo.CASLoginGeneration(p, id, prevGen, gen, email)
	}
	return repo.CASGeneration(p, id, prevGen, gen, email)
}
