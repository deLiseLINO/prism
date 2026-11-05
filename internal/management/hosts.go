package management

import (
	"maps"
	"net/http"
	"slices"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
)

// HostIDLocal names the always-present local machine.
const HostIDLocal = "local"

// HostSync reports each host's last config push. The pusher watches config
// changes itself, so host mutations need no extra signal.
type HostSync interface {
	Status(id string, host config.Host) hostsync.Status
}

// SetHostSync wires the pusher whose status the hosts list shows.
func (s *Server) SetHostSync(sync HostSync) {
	s.hostSync = sync
}

type HostView struct {
	ID         string           `json:"id"`
	Local      bool             `json:"local"`
	Status     string           `json:"status"`
	Address    string           `json:"address,omitempty"`
	DaemonPort int              `json:"daemonPort,omitempty"`
	AutoSync   *bool            `json:"autoSync,omitempty"`
	Sync       *hostsync.Status `json:"sync,omitempty"`
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

type HostReplaceWrite struct {
	Address            string `json:"address"`
	DaemonPort         int    `json:"daemonPort,omitempty"`
	ExpectedGeneration uint64 `json:"expectedGeneration"`
}

type HostAutoSyncWrite struct {
	Enabled            bool   `json:"enabled"`
	ExpectedGeneration uint64 `json:"expectedGeneration"`
}

// hostView describes one configured host. A host is ok once its own prism
// daemon is registered; until then it is not_installed and gets no pushes.
func (s *Server) hostView(id string, host config.Host) HostView {
	enabled := host.SyncEnabled()
	v := HostView{ID: id, Status: "not_installed", Address: host.Address, DaemonPort: host.DaemonPort, AutoSync: &enabled}
	if host.DaemonPort > 0 {
		v.Status = "ok"
		if s.hostSync != nil {
			st := s.hostSync.Status(id, host)
			v.Sync = &st
		}
	}
	return v
}

func (s *Server) hostsList(w http.ResponseWriter, r *http.Request) {
	hosts := s.cfg.Get().Config.Hosts
	out := []HostView{{ID: HostIDLocal, Local: true, Status: "ok"}}
	for _, id := range slices.Sorted(maps.Keys(hosts)) {
		out = append(out, s.hostView(id, hosts[id]))
	}
	writeJSON(w, http.StatusOK, HostsResponse{Hosts: out})
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
	writeJSON(w, http.StatusOK, HostMutationResponse{Generation: updated.Generation, Host: s.hostView(body.ID, updated.Config.Hosts[body.ID])})
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
	current, exists := snap.Config.Hosts[id]
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "host "+id+" not found")
		return
	}
	doc := snap.Config
	doc.Hosts[id] = config.Host{Address: body.Address, DaemonPort: body.DaemonPort, AutoSync: current.AutoSync}
	updated, err := s.cfg.Update(doc, body.ExpectedGeneration)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, HostMutationResponse{Generation: updated.Generation, Host: s.hostView(id, updated.Config.Hosts[id])})
}

func (s *Server) hostsAutoSync(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("host")
	body, ok := decodeJSON[HostAutoSyncWrite](w, r)
	if !ok {
		return
	}
	if id == HostIDLocal {
		writeError(w, http.StatusBadRequest, "invalid_document", "this machine is the sync source")
		return
	}
	snap := s.cfg.Get()
	host, exists := snap.Config.Hosts[id]
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "host "+id+" not found")
		return
	}
	doc := snap.Config
	host.AutoSync = &body.Enabled
	doc.Hosts[id] = host
	updated, err := s.cfg.Update(doc, body.ExpectedGeneration)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, HostMutationResponse{Generation: updated.Generation, Host: s.hostView(id, updated.Config.Hosts[id])})
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
	writeJSON(w, http.StatusOK, GenerationResponse{Generation: updated.Generation})
}
