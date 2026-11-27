package management

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
)

// SyncBase stores the last bundle this machine applied as a host.
type SyncBase interface {
	Load() (hostsync.Base, bool, error)
	Save(hostsync.Base) error
}

// SetSyncBase lets this machine, as a host, keep its own edits across pushes
// and report them to the main machine. secrets reads this machine's API keys.
func (s *Server) SetSyncBase(base SyncBase, secrets hostsync.SecretSource) {
	s.syncBase = base
	s.syncSecrets = secrets
}

// AccountReplicas keeps, on a host, the replicas of the login accounts the
// main machine pushes.
type AccountReplicas interface {
	// Owned lists the accounts of provider signed in on this machine.
	Owned(provider string) []string
	// Apply makes replicas this machine's replica set under doc, the config
	// the push produced, and serves them at once.
	Apply(ctx context.Context, doc config.Document, replicas []hostsync.Replica) error
	// Rejected lists the replica access tokens an upstream refused here and
	// the main machine has not replaced yet.
	Rejected() []hostsync.RejectedToken
}

// SetAccountReplicas lets this machine, as a host, take the main machine's
// login accounts.
func (s *Server) SetAccountReplicas(replicas AccountReplicas) {
	s.replicas = replicas
}

// syncConfigApply takes the config the main machine pushed and lays it over
// this daemon's document. API keys go through the credential store, so the
// document only ever holds this machine's own references. An identical
// result writes nothing, which keeps the integration watcher quiet between
// periodic pushes. Renames in the pushed ledger are replayed first, so
// this machine's own data under an old provider id moves with it; a rename
// this machine refuses blocks the whole sync until the conflict is fixed,
// because merging without it would leave the old provider beside the new.
// Edits made here since the last push survive where the main machine left
// the same entry untouched; the main machine takes them on its next pull.
// The pushed account replicas go live last, once their providers are in the
// config and under their current ids.
func (s *Server) syncConfigApply(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[hostsync.Bundle](w, r)
	if !ok {
		return
	}
	base, err := s.loadSyncBase()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sync_base", err.Error())
		return
	}
	start := s.cfg.Get().Generation
	if s.renamer != nil && len(body.Document.ProviderRenames) > 0 {
		if err := s.renamer.Replay(r.Context(), body.Document.ProviderRenames); err != nil {
			writeRenameError(w, err, s.cfg.Get().Generation)
			return
		}
	}
	var owned hostsync.Owned
	if s.replicas != nil {
		owned = s.replicas.Owned
	}
	for range 3 {
		snap := s.cfg.Get()
		next := hostsync.Merge(snap.Config, body.Document, owned)
		if base != nil {
			edits, err := hostsync.Diff(r.Context(), *base, snap.Config, s.syncSecrets)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "credential_store", err.Error())
				return
			}
			next = hostsync.Keep(next, snap.Config, *base, body.Document, edits)
		}
		for id, secret := range body.Secrets {
			p, synced := next.Providers[id]
			if !synced || !p.Wire.Custom() || hostsync.KeepsKey(base, id, secret) {
				continue
			}
			ref, err := s.creds.Stage(r.Context(), id, p.APIKeyRef, []byte(secret))
			if err != nil {
				writeError(w, http.StatusInternalServerError, "credential_store", err.Error())
				return
			}
			p.APIKeyRef = ref
			next.Providers[id] = p
		}
		if config.Equivalent(next, snap.Config) {
			if !s.applyReplicas(r.Context(), w, snap.Config, body.Accounts) {
				return
			}
			s.saveSyncBase(body)
			writeJSON(w, http.StatusOK, hostsync.ApplyResponse{Changed: snap.Generation != start, Generation: snap.Generation})
			return
		}
		updated, err := s.cfg.Update(next, snap.Generation)
		if errors.Is(err, config.ErrStaleGeneration) {
			continue
		}
		if err != nil {
			writeConfigError(w, err)
			return
		}
		s.publishSynced(snap.Config, updated.Config)
		if !s.applyReplicas(r.Context(), w, updated.Config, body.Accounts) {
			return
		}
		s.saveSyncBase(body)
		writeJSON(w, http.StatusOK, hostsync.ApplyResponse{Changed: true, Generation: updated.Generation})
		return
	}
	writeError(w, http.StatusConflict, "stale_generation", "config kept changing while the sync was applied")
}

// applyReplicas applies the pushed account replicas after the config they
// belong to is in place, and writes the failure when that is refused.
func (s *Server) applyReplicas(ctx context.Context, w http.ResponseWriter, doc config.Document, replicas []hostsync.Replica) bool {
	if s.replicas == nil {
		return true
	}
	if err := s.replicas.Apply(ctx, doc, replicas); err != nil {
		writeError(w, http.StatusInternalServerError, "account_sync", err.Error())
		return false
	}
	return true
}

// syncEdits reports what this machine changed since the last push, and the
// replica access tokens refused here. A machine that never applied a push has
// no edits.
func (s *Server) syncEdits(w http.ResponseWriter, r *http.Request) {
	base, err := s.loadSyncBase()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sync_base", err.Error())
		return
	}
	var edits hostsync.Edits
	if base != nil {
		edits, err = hostsync.Diff(r.Context(), *base, s.cfg.Get().Config, s.syncSecrets)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "credential_store", err.Error())
			return
		}
	}
	if s.replicas != nil {
		edits.RejectedTokens = s.replicas.Rejected()
	}
	writeJSON(w, http.StatusOK, edits)
}

// AdoptHostEdits lays the edits a host reported over this machine's config,
// so the next push carries them to every host.
func (s *Server) AdoptHostEdits(ctx context.Context, host string, edits hostsync.Edits) error {
	for range 3 {
		snap := s.cfg.Get()
		next := hostsync.Apply(snap.Config, edits)
		for id, secret := range edits.Secrets {
			p, ok := next.Providers[id]
			if !ok || !p.Wire.Custom() {
				continue
			}
			ref, err := s.creds.Stage(ctx, id, p.APIKeyRef, []byte(secret))
			if err != nil {
				return err
			}
			p.APIKeyRef = ref
			next.Providers[id] = p
		}
		if config.Equivalent(next, snap.Config) {
			return nil
		}
		updated, err := s.cfg.Update(next, snap.Generation)
		if errors.Is(err, config.ErrStaleGeneration) {
			continue
		}
		if err != nil {
			return err
		}
		s.publishSynced(snap.Config, updated.Config)
		log.Printf("prism: sync host %s: took its edits", host)
		return nil
	}
	return config.ErrStaleGeneration
}

func (s *Server) loadSyncBase() (*hostsync.Base, error) {
	if s.syncBase == nil {
		return nil, nil
	}
	base, ok, err := s.syncBase.Load()
	if err != nil || !ok {
		return nil, err
	}
	return &base, nil
}

// saveSyncBase records the applied bundle. A failure is logged: the next push
// records it again, and until then edits are measured against the older base.
func (s *Server) saveSyncBase(body hostsync.Bundle) {
	if s.syncBase == nil {
		return
	}
	if err := s.syncBase.Save(hostsync.NewBase(body)); err != nil {
		log.Printf("prism: save sync base: %v", err)
	}
}

// publishSynced makes new API keys live and drops the keys of custom
// providers the sync removed, as the provider routes do for one provider.
// A failure here is retried by the daemon's reconcile loop, so it is logged.
func (s *Server) publishSynced(previous, next config.Document) {
	ctx := context.Background()
	for id, p := range next.Providers {
		if !p.Wire.Custom() {
			continue
		}
		if err := s.creds.Committed(ctx, id, previous.Providers[id], p); err != nil {
			log.Printf("prism: sync provider %s credential publication: %v", id, err)
		}
	}
	for id, p := range previous.Providers {
		if _, kept := next.Providers[id]; kept || !p.Wire.Custom() {
			continue
		}
		if err := s.creds.Delete(ctx, id); err != nil {
			log.Printf("prism: sync provider %s credential removal: %v", id, err)
		}
	}
}
