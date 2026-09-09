package management

import (
	"context"
	"net/http"
	"sync"

	"prism/internal/config"
	"prism/internal/integrations"
)

// HostIDLocal names the always-present local machine.
const HostIDLocal = "local"

// HostRegistries is the host table the integrations endpoints dispatch on:
// one registry per configured host, with the local machine always present.
// A host that is unreachable or unresolved keeps a nil registry and surfaces
// an honest status rather than a guess.
type HostRegistries struct {
	mu         sync.Mutex
	local      *integrations.Registry
	remote     map[string]*integrations.Registry
	unresolved map[string]string
}

func NewHostRegistries(local *integrations.Registry) *HostRegistries {
	return &HostRegistries{local: local, remote: make(map[string]*integrations.Registry), unresolved: make(map[string]string)}
}

// SetRemote installs or replaces the registry for one remote host.
func (h *HostRegistries) SetRemote(id string, registry *integrations.Registry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remote[id] = registry
	delete(h.unresolved, id)
}

// SetUnresolved records why a host has no registry (probe failed, host down).
func (h *HostRegistries) SetUnresolved(id string, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unresolved[id] = reason
	delete(h.remote, id)
}

// Forget drops a host from the table entirely; used when its config entry
// is deleted, unlike SetUnresolved which keeps the id listed.
func (h *HostRegistries) Forget(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.remote, id)
	delete(h.unresolved, id)
}

type HostLifecycle interface {
	// Ensure brings one host to life after its config entry was written:
	// probe, registry, tunnel. Returning an error records an unresolved host
	// rather than rolling the config back.
	Ensure(ctx context.Context, id string, address string) error
	// Remove tears down whatever Ensure started.
	Remove(id string)
}

type HostView struct {
	ID     string `json:"id"`
	Local  bool   `json:"local"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type HostsResponse struct {
	Hosts []HostView `json:"hosts"`
}

type HostWrite struct {
	ID                 string `json:"id"`
	Address            string `json:"address"`
	ExpectedGeneration uint64 `json:"expectedGeneration"`
}

type HostMutationResponse struct {
	Generation uint64   `json:"generation"`
	Host       HostView `json:"host"`
}

func (h *HostRegistries) List() []HostView {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []HostView{{ID: HostIDLocal, Local: true, Status: "ok"}}
	for id := range h.remote {
		out = append(out, HostView{ID: id, Status: "ok"})
	}
	for id, reason := range h.unresolved {
		out = append(out, HostView{ID: id, Status: "unresolved", Detail: reason})
	}
	return out
}

func (s *Server) hostsCreate(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[HostWrite](w, r)
	if !ok {
		return
	}
	if body.ID == "" || body.Address == "" {
		writeError(w, http.StatusBadRequest, "invalid_document", "host id and address are required")
		return
	}
	if body.ID == HostIDLocal {
		writeError(w, http.StatusBadRequest, "invalid_document", "host id "+HostIDLocal+" is reserved for this machine")
		return
	}
	snap := s.cfg.Get()
	if _, exists := snap.Config.Hosts[body.ID]; exists {
		writeError(w, http.StatusConflict, "already_exists", "host "+body.ID+" exists")
		return
	}
	doc := snap.Config
	if doc.Hosts == nil {
		doc.Hosts = map[string]config.Host{}
	}
	doc.Hosts[body.ID] = config.Host{Address: body.Address}
	updated, err := s.cfg.Update(doc, body.ExpectedGeneration)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	s.ensureHost(w, r, body.ID, body.Address, updated)
}

func (s *Server) hostsDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("host")
	expected, ok := generationFromQuery(w, r)
	if !ok {
		return
	}
	if id == HostIDLocal {
		writeError(w, http.StatusBadRequest, "invalid_document", "host id "+HostIDLocal+" cannot be deleted")
		return
	}
	snap := s.cfg.Get()
	if _, exists := snap.Config.Hosts[id]; !exists {
		writeError(w, http.StatusNotFound, "not_found", "host "+id+" not found")
		return
	}
	doc := snap.Config
	delete(doc.Hosts, id)
	updated, err := s.cfg.Update(doc, expected)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	if s.lifecycle != nil {
		s.lifecycle.Remove(id)
	}
	s.hosts.Forget(id)
	writeJSON(w, http.StatusOK, GenerationResponse{Generation: updated.Generation})
}

func (s *Server) ensureHost(w http.ResponseWriter, r *http.Request, id string, address string, updated config.Snapshot) {
	if s.lifecycle == nil {
		writeJSON(w, http.StatusOK, HostMutationResponse{Generation: updated.Generation, Host: HostView{ID: id, Status: "ok"}})
		return
	}
	if err := s.lifecycle.Ensure(r.Context(), id, address); err != nil {
		writeJSON(w, http.StatusOK, HostMutationResponse{Generation: updated.Generation, Host: HostView{ID: id, Status: "unresolved", Detail: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, HostMutationResponse{Generation: updated.Generation, Host: HostView{ID: id, Status: "ok"}})
}

// Lookup resolves a host id onto its registry; ok=false means the id is
// unknown or the host is unresolved, with detail saying which.
func (h *HostRegistries) Lookup(id string) (registry *integrations.Registry, ok bool, detail string) {
	if id == "" || id == HostIDLocal {
		return h.local, true, ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if registry, present := h.remote[id]; present {
		return registry, true, ""
	}
	if reason, present := h.unresolved[id]; present {
		return nil, false, "host is unresolved: " + reason
	}
	return nil, false, "unknown host " + id
}
