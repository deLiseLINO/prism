package management

import (
	"net/http"

	"prism/internal/config"
	"prism/internal/integrations"
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

func (s *Server) hostsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HostsResponse{Hosts: s.hosts.List()})
}

func (s *Server) hostRegistry(w http.ResponseWriter, r *http.Request) (*integrations.Registry, bool) {
	registry, ok, detail := s.hosts.Lookup(r.PathValue("host"))
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "host_unavailable", detail)
		return nil, false
	}
	return registry, true
}

func (s *Server) hostIntegrationsList(w http.ResponseWriter, r *http.Request) {
	registry, ok := s.hostRegistry(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, IntegrationsResponse{Integrations: registry.Status()})
}

func (s *Server) hostIntegrationGet(w http.ResponseWriter, r *http.Request) {
	registry, ok := s.hostRegistry(w, r)
	if !ok {
		return
	}
	id, ok := integrationID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, registry.StatusOf(id))
}

func (s *Server) hostIntegrationApply(w http.ResponseWriter, r *http.Request) {
	registry, ok := s.hostRegistry(w, r)
	if !ok {
		return
	}
	id, ok := integrationID(w, r)
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	writeJSON(w, http.StatusOK, registry.ApplyForced(id, force))
}

func (s *Server) hostIntegrationRollback(w http.ResponseWriter, r *http.Request) {
	registry, ok := s.hostRegistry(w, r)
	if !ok {
		return
	}
	id, ok := integrationID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, registry.Rollback(id))
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
	writeJSON(w, http.StatusOK, s.ints.Rollback(id))
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
