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
	renewAheadEvery  = 30 * time.Second
	renewAheadMargin = time.Minute
)

type replicaPool interface {
	auth.PoolRegistrar
	PublishCredential(id account.AccountID, gen account.CredentialGeneration, changed bool) error
}

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

func (s *accountSync) noteRejected(p account.ProviderID, id account.AccountID, access string) {
	s.rejected.Add(string(p), string(id), access)
}

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
