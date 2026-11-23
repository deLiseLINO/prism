package management

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/connectlink"
)

// connect adds the provider a connect link describes. A link never
// overwrites a provider, and the caller sends no generation: the handler
// creates against the current one and retries while the config moves.
func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[ConnectRequest](w, r)
	if !ok {
		return
	}
	link, err := connectlink.Parse(body.Link)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_link", err.Error())
		return
	}
	snap := s.cfg.Get()
	_, exists := snap.Config.Providers[link.ID]
	plan := connectPlan(link, exists)
	if body.DryRun {
		writeJSON(w, http.StatusOK, ConnectResponse{Generation: snap.Generation, Plan: plan})
		return
	}
	write := connectWrite(link)
	for range 3 {
		if _, exists := snap.Config.Providers[link.ID]; exists {
			writeError(w, http.StatusConflict, "already_exists", "provider "+link.ID+" exists")
			return
		}
		updated, err := s.commitProvider(r.Context(), snap, link.ID, write, snap.Generation)
		if errors.Is(err, config.ErrStaleGeneration) {
			snap = s.cfg.Get()
			continue
		}
		if err != nil {
			writeMutationError(w, err)
			return
		}
		warning := ""
		if len(link.Models) == 0 {
			updated, warning = s.discoverConnected(r.Context(), updated, link)
		}
		v, err := s.providerView(r.Context(), updated.Config, link.ID, updated.Config.Providers[link.ID])
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ConnectResponse{Applied: true, Generation: updated.Generation, Plan: plan, Provider: &v, Warning: warning})
		return
	}
	writeError(w, http.StatusConflict, "stale_generation", "config kept changing while the link was applied")
}

// discoverConnected fills the model list of a provider created from a link
// that named no models. The provider stays whatever happens; a failure only
// becomes the returned warning, with the key masked in case the upstream
// echoed it.
func (s *Server) discoverConnected(ctx context.Context, snap config.Snapshot, link connectlink.Link) (config.Snapshot, string) {
	if s.syncer == nil {
		return snap, "model discovery is not available, so add the provider's models by hand"
	}
	for range 3 {
		updated, err := s.syncModels(ctx, snap, link.ID, snap.Generation)
		if errors.Is(err, config.ErrStaleGeneration) {
			snap = s.cfg.Get()
			continue
		}
		if err != nil {
			reason := err.Error()
			if link.Key != "" {
				reason = strings.ReplaceAll(reason, link.Key, connectlink.MaskKey(link.Key))
			}
			return snap, "model discovery failed: " + reason
		}
		return updated, ""
	}
	return s.cfg.Get(), "model discovery stopped because the config kept changing"
}

func connectPlan(link connectlink.Link, exists bool) ConnectPlan {
	mode := config.EndpointModeFailover
	if link.Spread {
		mode = config.EndpointModeSpread
	}
	modelWires := make([]ConnectModelWire, len(link.ModelWires))
	for i, w := range link.ModelWires {
		modelWires[i] = ConnectModelWire{Model: w.Model, Wire: w.Wire}
	}
	return ConnectPlan{
		ID:               link.ID,
		Wire:             link.Wire,
		BaseURL:          link.Endpoints[0],
		FallbackBaseURLs: append([]string{}, link.Endpoints[1:]...),
		EndpointMode:     string(mode),
		Models:           append([]string{}, link.Models...),
		ModelWires:       modelWires,
		DefaultModel:     link.DefaultModel,
		KeyPreview:       connectlink.MaskKey(link.Key),
		HasKey:           link.Key != "",
		Exists:           exists,
	}
}

func connectWrite(link connectlink.Link) ProviderWrite {
	w := ProviderWrite{ID: link.ID, Wire: link.Wire, BaseURL: &link.Endpoints[0], Models: link.Models, Credential: link.Key}
	if fallbacks := link.Endpoints[1:]; len(fallbacks) > 0 {
		w.FallbackBaseURLs = &fallbacks
	}
	if link.Spread {
		mode := string(config.EndpointModeSpread)
		w.EndpointMode = &mode
	}
	if link.DefaultModel != "" {
		w.DefaultModel = &link.DefaultModel
	}
	if len(link.ModelWires) > 0 {
		settings := make(map[string]config.ModelSettings, len(link.ModelWires))
		for _, mw := range link.ModelWires {
			settings[mw.Model] = config.ModelSettings{Wire: config.Wire(mw.Wire)}
		}
		w.ModelSettings = &settings
	}
	return w
}
