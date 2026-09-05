package management

import (
	"net/http"

	"prism/internal/integrations"
)

type IntegrationsResponse struct {
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
	writeJSON(w, http.StatusOK, IntegrationsResponse{Integrations: s.ints.Status()})
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
	writeJSON(w, http.StatusOK, s.ints.Apply(id))
}

func (s *Server) integrationRollback(w http.ResponseWriter, r *http.Request) {
	id, ok := integrationID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.ints.Rollback(id))
}
