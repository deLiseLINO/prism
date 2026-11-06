package management

import (
	"net/http"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/integrations"
)

type IntegrationsResponse struct {
	Generation   uint64                `json:"generation"`
	Integrations []integrations.Status `json:"integrations"`
}

func integrationID(w http.ResponseWriter, r *http.Request) (integrations.ID, bool) {
	raw := r.PathValue("client")
	id, ok := integrations.ValidID(raw)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown integration client "+raw)
		return "", false
	}
	return id, true
}

func (s *Server) integrationsList(w http.ResponseWriter, r *http.Request) {
	snap := s.cfg.Get()
	writeJSON(w, http.StatusOK, IntegrationsResponse{Generation: snap.Generation, Integrations: s.ints.Status()})
}

func (s *Server) integrationGet(w http.ResponseWriter, r *http.Request) {
	id, ok := integrationID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.ints.StatusOf(id))
}

func (s *Server) integrationApply(w http.ResponseWriter, r *http.Request) {
	id, ok := integrationID(w, r)
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	writeJSON(w, http.StatusOK, s.ints.ApplyForced(id, force))
}

func (s *Server) integrationRollback(w http.ResponseWriter, r *http.Request) {
	id, ok := integrationID(w, r)
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	writeJSON(w, http.StatusOK, s.ints.RollbackForced(id, force))
}

func (s *Server) integrationToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := integrationID(w, r)
	if !ok {
		return
	}
	body, ok := decodeJSON[IntegrationToggleWrite](w, r)
	if !ok {
		return
	}
	snap := s.cfg.Get()
	doc := snap.Config
	doc.Integrations[string(id)] = config.IntegrationSettings{Enabled: body.Enabled}
	updated, err := s.cfg.Update(doc, body.ExpectedGeneration)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, IntegrationToggleResponse{Generation: updated.Generation, Enabled: updated.Config.Integrations[string(id)].Enabled})
}
