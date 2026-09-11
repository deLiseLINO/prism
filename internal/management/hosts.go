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
	external   map[string]bool
	addresses  map[string]config.Host
}

func NewHostRegistries(local *integrations.Registry) *HostRegistries {
	return &HostRegistries{local: local, remote: make(map[string]*integrations.Registry), unresolved: make(map[string]string), external: make(map[string]bool), addresses: make(map[string]config.Host)}
}

// SetRemote installs or replaces the registry for one remote host.
func (h *HostRegistries) SetRemote(id string, registry *integrations.Registry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remote[id] = registry
	delete(h.unresolved, id)
	delete(h.external, id)
}

// SetHostConfig records a host's config and classifies it: a host with its
// own daemon port runs its own prismd, so it is external — listed as ok with
// no local registry and no reverse tunnel. Plain hosts stay managed by the
// supervisor's Ensure.
func (h *HostRegistries) SetHostConfig(id string, host config.Host) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.addresses[id] = host
	if host.DaemonPort > 0 {
		h.external[id] = true
		delete(h.remote, id)
		delete(h.unresolved, id)
	} else {
		delete(h.external, id)
	}
}

// SetUnresolved records why a host has no registry (probe failed, host down).
func (h *HostRegistries) SetUnresolved(id string, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unresolved[id] = reason
	delete(h.remote, id)
	delete(h.external, id)
}

// Forget drops a host's live state (registry, tunnel, status) but keeps its
// configured address: the supervisor calls this when re-bringing a host up,
// and the config entry still exists. ForgetConfig additionally drops the
// address book entry; the hosts endpoint calls it when the config entry is
// deleted.
func (h *HostRegistries) Forget(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.remote, id)
	delete(h.unresolved, id)
	delete(h.external, id)
}

func (h *HostRegistries) ForgetConfig(id string) {
	h.Forget(id)
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.addresses, id)
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
	ID         string `json:"id"`
	Local      bool   `json:"local"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	Address    string `json:"address,omitempty"`
	DaemonPort int    `json:"daemonPort,omitempty"`
}

type HostsResponse struct {
	Hosts []HostView `json:"hosts"`
}

type HostWrite struct {
	ID                 string `json:"id"`
	Address            string `json:"address"`
	DaemonPort         int    `json:"daemonPort,omitempty"`
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
		hc := h.addresses[id]
		out = append(out, HostView{ID: id, Status: "ok", Address: hc.Address, DaemonPort: hc.DaemonPort})
	}
	for id := range h.external {
		hc := h.addresses[id]
		out = append(out, HostView{ID: id, Status: "ok", Address: hc.Address, DaemonPort: hc.DaemonPort})
	}
	for id, reason := range h.unresolved {
		hc := h.addresses[id]
		out = append(out, HostView{ID: id, Status: "unresolved", Detail: reason, Address: hc.Address, DaemonPort: hc.DaemonPort})
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
	if body.DaemonPort < 0 || body.DaemonPort > 65535 {
		writeError(w, http.StatusBadRequest, "invalid_document", "daemonPort must be a valid port")
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
	doc.Hosts[body.ID] = config.Host{Address: body.Address, DaemonPort: body.DaemonPort}
	updated, err := s.cfg.Update(doc, body.ExpectedGeneration)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	s.hosts.SetHostConfig(body.ID, doc.Hosts[body.ID])
	s.ensureHost(w, r, body.ID, body.Address, body.DaemonPort, updated)
}

type HostReplaceWrite struct {
	Address            string `json:"address"`
	DaemonPort         int    `json:"daemonPort,omitempty"`
	ExpectedGeneration uint64 `json:"expectedGeneration"`
}

func (s *Server) hostsReplace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("host")
	body, ok := decodeJSON[HostReplaceWrite](w, r)
	if !ok {
		return
	}
	if id == "" || id == HostIDLocal {
		writeError(w, http.StatusBadRequest, "invalid_document", "host id "+HostIDLocal+" cannot be replaced")
		return
	}
	if body.Address == "" {
		writeError(w, http.StatusBadRequest, "invalid_document", "host address is required")
		return
	}
	if body.DaemonPort < 0 || body.DaemonPort > 65535 {
		writeError(w, http.StatusBadRequest, "invalid_document", "daemonPort must be a valid port")
		return
	}
	snap := s.cfg.Get()
	if _, exists := snap.Config.Hosts[id]; !exists {
		writeError(w, http.StatusNotFound, "not_found", "host "+id+" not found")
		return
	}
	doc := snap.Config
	doc.Hosts[id] = config.Host{Address: body.Address, DaemonPort: body.DaemonPort}
	updated, err := s.cfg.Update(doc, body.ExpectedGeneration)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	if s.lifecycle != nil && body.DaemonPort > 0 {
		s.lifecycle.Remove(id)
	}
	s.hosts.SetHostConfig(id, doc.Hosts[id])
	s.ensureHost(w, r, id, body.Address, body.DaemonPort, updated)
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
	s.hosts.ForgetConfig(id)
	writeJSON(w, http.StatusOK, GenerationResponse{Generation: updated.Generation})
}

func (s *Server) ensureHost(w http.ResponseWriter, r *http.Request, id string, address string, daemonPort int, updated config.Snapshot) {
	if s.lifecycle == nil || daemonPort > 0 {
		writeJSON(w, http.StatusOK, HostMutationResponse{Generation: updated.Generation, Host: HostView{ID: id, Status: "ok", Address: address, DaemonPort: daemonPort}})
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
	if h.external[id] {
		return nil, false, "host " + id + " runs its own daemon; reach it through its management API"
	}
	return nil, false, "unknown host " + id
}
