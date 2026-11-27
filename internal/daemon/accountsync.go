package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/auth"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
)

const (
	// renewAheadEvery is how often the main machine looks for pushed
	// credentials about to expire.
	renewAheadEvery = 30 * time.Second
	// renewAheadMargin pads the renewal window beyond two push intervals.
	renewAheadMargin = time.Minute
)

// replicaPool is the pool surface that keeps replicas live without a restart.
type replicaPool interface {
	auth.PoolRegistrar
	PublishCredential(id account.AccountID, gen account.CredentialGeneration, changed bool) error
}

// accountSync carries login accounts from the main machine to its hosts.
// On the main machine it lists replicas for the push, renews the credentials
// hosts report refused and renews pushed credentials ahead of expiry. On a
// host it stores and serves the replicas pushed to it. The main machine is
// the only owner: a host never signs a replica in, out or renews a grant
// that rotates.
type accountSync struct {
	env      *daemonEnv
	pool     replicaPool
	rejected hostsync.RejectedTokens
}

func newAccountSync(env *daemonEnv, pool replicaPool) *accountSync {
	return &accountSync{env: env, pool: pool}
}

func (s *accountSync) repo(provider string) *account.Repository {
	p := s.env.cfg.Get().Config.Providers[provider]
	return s.env.ensureRepo(account.ProviderID(provider), p.Pool)
}

// Replicas lists every login account this machine signed in under doc. On a
// wire whose refresh token rotates the replica carries no refresh token, so
// only this machine ever spends the grant.
func (s *accountSync) Replicas(ctx context.Context, doc config.Document) ([]hostsync.Replica, error) {
	var out []hostsync.Replica
	for _, id := range slices.Sorted(maps.Keys(doc.Providers)) {
		p := doc.Providers[id]
		if p.Wire.Custom() {
			continue
		}
		providerID := account.ProviderID(id)
		rows, err := s.env.ensureRepo(providerID, p.Pool).Load(providerID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Origin == account.OriginMain {
				continue
			}
			r := hostsync.Replica{Provider: id, ID: string(row.ID), Email: row.Email, State: replicaState(row.State), Priority: row.Priority}
			blob, ok, err := s.env.creds.file.Get(ctx, providerID, row.ID, row.CredGen)
			if err != nil {
				return nil, err
			}
			if ok {
				cred, err := account.ParseCredential(blob)
				if err != nil {
					return nil, fmt.Errorf("credential of %s: %w", row.ID, err)
				}
				if p.Wire.RotatesRefreshToken() {
					cred.Refresh = ""
				}
				r.Credential = cred.Encode()
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// Renew renews this machine's credentials whose access tokens host reported
// refused. A report about a token this machine already replaced needs
// nothing: the push carries the newer one.
func (s *accountSync) Renew(ctx context.Context, host string, rejected []hostsync.RejectedToken) error {
	var errs []error
	for _, r := range rejected {
		providerID, id := account.ProviderID(r.Provider), account.AccountID(r.Account)
		repo := s.repo(r.Provider)
		if replica, err := repo.IsReplica(providerID, id); err != nil || replica {
			errs = append(errs, err)
			continue
		}
		gen, err := repo.CurrentGeneration(providerID, id)
		if err != nil || gen == 0 {
			errs = append(errs, err)
			continue
		}
		blob, ok, err := s.env.creds.file.Get(ctx, providerID, id, gen)
		if err != nil || !ok {
			errs = append(errs, err)
			continue
		}
		cred, err := account.ParseCredential(blob)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if hostsync.TokenHash(cred.Access) != r.Hash {
			continue
		}
		if _, err := s.env.refresher.RefreshRejected(ctx, account.Lease{Provider: providerID, Account: id, CredGen: gen}, cred.Access); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		log.Printf("prism: sync host %s: renewed %s, which the machine reported refused", host, id)
	}
	return errors.Join(errs...)
}

// renewAhead renews, every renewAheadEvery while a host takes pushes, the
// credentials of rotating-grant wires that expire within the provider's skew
// plus ahead. Hosts cannot renew those themselves, so each must get the new
// token from a push before its copy expires.
func (s *accountSync) renewAhead(ctx context.Context, ahead time.Duration) {
	ticker := time.NewTicker(renewAheadEvery)
	defer ticker.Stop()
	for {
		s.renewDue(ctx, ahead)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *accountSync) renewDue(ctx context.Context, ahead time.Duration) {
	doc := s.env.cfg.Get().Config
	if !slices.ContainsFunc(slices.Collect(maps.Values(doc.Hosts)), func(h config.Host) bool { return h.DaemonPort > 0 && h.SyncEnabled() }) {
		return
	}
	for _, id := range slices.Sorted(maps.Keys(doc.Providers)) {
		p := doc.Providers[id]
		if !p.Wire.RotatesRefreshToken() {
			continue
		}
		providerID := account.ProviderID(id)
		rows, err := s.env.ensureRepo(providerID, p.Pool).Load(providerID)
		if err != nil {
			log.Printf("prism: renew pushed credentials of %s: %v", id, err)
			continue
		}
		for _, row := range rows {
			if row.Origin == account.OriginMain || row.State != account.Active {
				continue
			}
			if _, err := s.env.refresher.RefreshAhead(ctx, account.Lease{Provider: providerID, Account: row.ID, CredGen: row.CredGen}, ahead); err != nil {
				log.Printf("prism: renew pushed credential of %s: %v", row.ID, err)
			}
		}
	}
}

// Owned lists the accounts of provider signed in on this machine.
func (s *accountSync) Owned(provider string) []string {
	rows, err := s.repo(provider).Load(account.ProviderID(provider))
	if err != nil {
		log.Printf("prism: accounts of %s: %v", provider, err)
		return nil
	}
	var out []string
	for _, row := range rows {
		if row.Origin != account.OriginMain {
			out = append(out, string(row.ID))
		}
	}
	return out
}

// Rejected lists the replica access tokens refused here that are still
// current.
func (s *accountSync) Rejected() []hostsync.RejectedToken {
	return s.rejected.Pending(func(provider, id string) string {
		providerID, accountID := account.ProviderID(provider), account.AccountID(id)
		gen, err := s.repo(provider).CurrentGeneration(providerID, accountID)
		if err != nil || gen == 0 {
			return ""
		}
		blob, ok, err := s.env.creds.file.Get(context.Background(), providerID, accountID, gen)
		if err != nil || !ok {
			return ""
		}
		cred, err := account.ParseCredential(blob)
		if err != nil {
			return ""
		}
		return hostsync.TokenHash(cred.Access)
	})
}

// noteRejected records that an upstream refused the current access token of
// a replica, for the main machine to renew.
func (s *accountSync) noteRejected(p account.ProviderID, id account.AccountID, access string) {
	s.rejected.Add(string(p), string(id), access)
}

// Apply makes replicas this host's replica set under doc and serves them
// without a restart. Accounts signed in here are never replaced or deleted;
// on a single-account wire a local login replaces the replica altogether.
// Replicas the main machine no longer has are deleted.
func (s *accountSync) Apply(ctx context.Context, doc config.Document, replicas []hostsync.Replica) error {
	s.env.renameMu.Lock()
	defer s.env.renameMu.Unlock()
	byProvider := map[string][]hostsync.Replica{}
	for _, r := range replicas {
		byProvider[r.Provider] = append(byProvider[r.Provider], r)
	}
	var errs []error
	for _, id := range slices.Sorted(maps.Keys(doc.Providers)) {
		p := doc.Providers[id]
		if p.Wire.Custom() {
			continue
		}
		if err := s.applyProvider(ctx, id, p, byProvider[id]); err != nil {
			errs = append(errs, fmt.Errorf("accounts of %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func (s *accountSync) applyProvider(ctx context.Context, id string, p config.Provider, replicas []hostsync.Replica) error {
	providerID := account.ProviderID(id)
	repo := s.env.ensureRepo(providerID, p.Pool)
	rows, err := repo.Load(providerID)
	if err != nil {
		return err
	}
	local := map[account.AccountID]bool{}
	for _, row := range rows {
		if row.Origin != account.OriginMain {
			local[row.ID] = true
		}
	}
	if p.Wire.SingleAccount() && len(local) > 0 {
		replicas = nil
	}
	if len(replicas) > 0 {
		if err := s.env.ensureProvider(ctx, id, p); err != nil {
			return err
		}
	}
	wanted := map[account.AccountID]bool{}
	for _, r := range replicas {
		wanted[account.AccountID(r.ID)] = true
	}
	var errs []error
	for _, row := range rows {
		if row.Origin != account.OriginMain || wanted[row.ID] {
			continue
		}
		if err := s.pool.DeleteAccount(ctx, row.ID); err != nil && !errors.Is(err, account.ErrNotFound) {
			errs = append(errs, err)
			continue
		}
		if err := auth.DeleteReplica(ctx, s.env.creds.file, repo, providerID, row.ID); err != nil {
			errs = append(errs, err)
		}
	}
	for _, r := range replicas {
		if local[account.AccountID(r.ID)] {
			continue
		}
		if err := s.applyReplica(ctx, repo, providerID, r); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *accountSync) applyReplica(ctx context.Context, repo *account.Repository, providerID account.ProviderID, pushed hostsync.Replica) error {
	state, err := parseReplicaState(pushed.State)
	if err != nil {
		return err
	}
	r := auth.Replica{Provider: providerID, ID: account.AccountID(pushed.ID), Email: pushed.Email, State: state, Priority: pushed.Priority}
	if len(pushed.Credential) > 0 {
		cred, err := account.ParseCredential(pushed.Credential)
		if err != nil {
			return err
		}
		r.Credential = &cred
	}
	gen, changed, err := auth.PublishReplica(ctx, s.env.creds.file, repo, r)
	if errors.Is(err, account.ErrLocalAccount) {
		return nil
	}
	if err != nil || gen == 0 {
		return err
	}
	for range 3 {
		err = s.live(ctx, r, gen, changed)
		if !errors.Is(err, account.ErrStale) {
			return err
		}
	}
	return err
}

// live brings the pool entry of r to the stored generation and the main
// machine's state and priority. A new credential clears a rejection, as a
// re-login does, and so does the main machine reporting the account usable.
func (s *accountSync) live(ctx context.Context, r auth.Replica, gen account.CredentialGeneration, changed bool) error {
	cur, ok := s.poolAccount(r.ID)
	if !ok || cur.Provider != r.Provider {
		s.pool.Register(account.Account{ID: r.ID, Provider: r.Provider, Priority: r.Priority, State: r.State, Email: r.Email, Origin: account.OriginMain, CredGen: gen, Version: 1})
		return nil
	}
	if changed || cur.CredGen < gen || r.State != account.NeedsReauth && cur.State == account.NeedsReauth {
		if err := s.pool.PublishCredential(r.ID, gen, true); err != nil {
			return err
		}
		cur, _ = s.poolAccount(r.ID)
	}
	switch {
	case r.State == account.Paused && cur.State != account.Paused:
		if err := s.pool.Pause(ctx, r.ID, cur.Version); err != nil {
			return err
		}
	case r.State != account.Paused && cur.State == account.Paused:
		if err := s.pool.Resume(ctx, r.ID, cur.Version); err != nil {
			return err
		}
	}
	cur, _ = s.poolAccount(r.ID)
	if r.State == account.NeedsReauth && cur.State != account.NeedsReauth {
		s.pool.Register(account.Account{ID: r.ID, Provider: r.Provider, Priority: r.Priority, State: account.NeedsReauth, Email: r.Email, Origin: account.OriginMain, CredGen: gen})
		cur, _ = s.poolAccount(r.ID)
	}
	if cur.Priority != r.Priority {
		return s.pool.UpdatePriority(ctx, r.ID, r.Priority, cur.Version)
	}
	return nil
}

func (s *accountSync) poolAccount(id account.AccountID) (account.Account, bool) {
	for _, a := range s.pool.Snapshot().Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return account.Account{}, false
}

func replicaState(state account.State) string {
	switch state {
	case account.Paused:
		return hostsync.ReplicaPaused
	case account.NeedsReauth:
		return hostsync.ReplicaNeedsReauth
	default:
		return hostsync.ReplicaActive
	}
}

func parseReplicaState(state string) (account.State, error) {
	switch state {
	case hostsync.ReplicaActive:
		return account.Active, nil
	case hostsync.ReplicaPaused:
		return account.Paused, nil
	case hostsync.ReplicaNeedsReauth:
		return account.NeedsReauth, nil
	default:
		return 0, fmt.Errorf("unknown account state %q", state)
	}
}
