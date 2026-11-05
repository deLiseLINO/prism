package management

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
)

// syncConfigApply takes the config the main machine pushed and lays it over
// this daemon's document. API keys go through the credential store, so the
// document only ever holds this machine's own references. An identical
// result writes nothing, which keeps the integration watcher quiet between
// periodic pushes.
func (s *Server) syncConfigApply(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[hostsync.Bundle](w, r)
	if !ok {
		return
	}
	for range 3 {
		snap := s.cfg.Get()
		next := hostsync.Merge(snap.Config, body.Document)
		for id, secret := range body.Secrets {
			p, synced := next.Providers[id]
			if !synced || !p.Wire.Custom() {
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
			writeJSON(w, http.StatusOK, hostsync.ApplyResponse{Generation: snap.Generation})
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
		writeJSON(w, http.StatusOK, hostsync.ApplyResponse{Changed: true, Generation: updated.Generation})
		return
	}
	writeError(w, http.StatusConflict, "stale_generation", "config kept changing while the sync was applied")
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
