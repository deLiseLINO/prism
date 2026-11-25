package management

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostinstall"
	"github.com/deLiseLINO/prism/internal/hostsync"
)

// HostHeader names the machine a management request is for. Requests that
// name a remote host go to that host's daemon over SSH; the UI sets it while
// it manages another machine.
const HostHeader = "X-Prism-Host"

const maxForwardBody = 16 << 20

// HostInstaller installs prism on one remote machine.
type HostInstaller interface {
	Install(ctx context.Context, host, address string, register func(port int) error) hostinstall.Reply
}

// HostInstallStatus is the latest install of one machine. Reply is nil
// until an install finishes.
type HostInstallStatus struct {
	Running bool               `json:"running"`
	Reply   *hostinstall.Reply `json:"reply"`
}

type hostInstalls struct {
	mu   sync.Mutex
	runs map[string]HostInstallStatus
}

// SetHostInstaller wires the installer behind POST /api/v1/hosts/{host}/install
// and the transport that reaches installed hosts. ctx bounds the installs.
func (s *Server) SetHostInstaller(ctx context.Context, installer HostInstaller, transport hostsync.Transport) {
	s.hostCtx = ctx
	s.hostInstaller = installer
	s.hostTransport = transport
}

func (s *Server) hostsInstallStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("host")
	host, ok := s.remoteHost(w, id)
	if !ok {
		return
	}
	if s.hostInstaller == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "remote install is not available")
		return
	}
	s.installs.mu.Lock()
	defer s.installs.mu.Unlock()
	if s.installs.runs == nil {
		s.installs.runs = map[string]HostInstallStatus{}
	}
	status := s.installs.runs[id]
	if status.Running {
		writeJSON(w, http.StatusAccepted, status)
		return
	}
	status = HostInstallStatus{Running: true}
	s.installs.runs[id] = status
	go func() {
		reply := s.hostInstaller.Install(s.hostCtx, id, host.Address, func(port int) error {
			return s.registerHostPort(id, host.Address, port)
		})
		s.installs.mu.Lock()
		s.installs.runs[id] = HostInstallStatus{Reply: &reply}
		s.installs.mu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, status)
}

func (s *Server) hostsInstallStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("host")
	if _, ok := s.remoteHost(w, id); !ok {
		return
	}
	s.installs.mu.Lock()
	status := s.installs.runs[id]
	s.installs.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) remoteHost(w http.ResponseWriter, id string) (config.Host, bool) {
	if id == HostIDLocal {
		writeError(w, http.StatusBadRequest, "invalid_document", "host id "+HostIDLocal+" is this machine")
		return config.Host{}, false
	}
	host, ok := s.cfg.Get().Config.Hosts[id]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "host "+id+" not found")
		return config.Host{}, false
	}
	return host, true
}

// registerHostPort records the daemon port of host. A recorded port writes
// nothing, so repeated installs do not churn the config. It retries once
// when another write lands between the read and the update.
func (s *Server) registerHostPort(id, address string, port int) error {
	var err error
	for range 2 {
		snap := s.cfg.Get()
		host, ok := snap.Config.Hosts[id]
		if !ok {
			return fmt.Errorf("host %s was removed", id)
		}
		if host.Address != address {
			return fmt.Errorf("the address of host %s changed during the install", id)
		}
		if host.DaemonPort == port {
			return nil
		}
		doc := snap.Config
		host.DaemonPort = port
		doc.Hosts[id] = host
		if _, err = s.cfg.Update(doc, snap.Generation); err == nil {
			return nil
		}
	}
	return err
}

// forwardToHost sends r to the daemon of the installed host id and copies
// its reply back.
func (s *Server) forwardToHost(w http.ResponseWriter, r *http.Request, id string) {
	host, ok := s.remoteHost(w, id)
	if !ok {
		return
	}
	if host.DaemonPort <= 0 {
		writeError(w, http.StatusConflict, "host_not_installed", "prism is not installed on "+id+" yet")
		return
	}
	if s.hostTransport == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "remote hosts are not available")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxForwardBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_document", err.Error())
		return
	}
	if len(body) > maxForwardBody {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body is too large")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, fmt.Sprintf("http://127.0.0.1:%d%s", host.DaemonPort, r.URL.RequestURI()), bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_document", err.Error())
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := s.hostTransport(r.Context(), host, req)
	if err != nil {
		if errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		writeError(w, http.StatusBadGateway, "host_unreachable", "could not reach "+id+": "+err.Error())
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
