package management

import (
	"errors"
	"net/http"

	"prism/internal/agentinstall"
	"prism/internal/integrations"
)

// AgentActions is the kill switch for agent binary install and update jobs.
// Status and job reads stay open; only mutations are gated. Off unless the
// daemon is started with PRISM_AGENT_ACTIONS set to 1/true — the escape hatch
// for the operator, invisible to end users.
var AgentActions = false

// ParseAgentActionsEnv maps the PRISM_AGENT_ACTIONS value onto the switch.
// Anything but 1/true keeps actions off.
func ParseAgentActionsEnv(raw string) bool {
	return raw == "1" || raw == "true"
}

// Installer is the daemon's agent install/update manager as the routes see
// it: derived statuses, job launches, and job snapshots.
type Installer interface {
	StatusAll() []agentinstall.AgentStatus
	StatusOf(id integrations.ID) (agentinstall.AgentStatus, bool)
	Install(id integrations.ID, force bool) (agentinstall.Job, error)
	Update(id integrations.ID) (agentinstall.Job, error)
	JobOf(id integrations.ID) agentinstall.Job
}

func agentID(w http.ResponseWriter, r *http.Request) (integrations.ID, bool) {
	raw := r.PathValue("id")
	id, ok := integrations.ValidID(raw)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown agent "+raw)
		return "", false
	}
	return id, true
}

func (s *Server) agentsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, AgentsResponse{Agents: s.installer.StatusAll(), ActionsEnabled: AgentActions})
}

func (s *Server) agentGet(w http.ResponseWriter, r *http.Request) {
	id, ok := agentID(w, r)
	if !ok {
		return
	}
	status, ok := s.installer.StatusOf(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown agent "+string(id))
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) agentInstall(w http.ResponseWriter, r *http.Request) {
	if !AgentActions {
		writeError(w, http.StatusNotImplemented, "agent_actions_disabled", "agent install and update are disabled until the feature flag is turned on")
		return
	}
	id, ok := agentID(w, r)
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	job, err := s.installer.Install(id, force)
	if err != nil {
		if errors.Is(err, agentinstall.ErrInstallActive) {
			writeError(w, http.StatusConflict, "install_active", "an install or update job is already active for this agent's binary")
			return
		}
		writeError(w, http.StatusBadRequest, "install_failed", err.Error())
		return
	}
	if job.State == agentinstall.StateUnsupported {
		writeJSON(w, http.StatusOK, AgentJobResponse{Job: job})
		return
	}
	writeJSON(w, http.StatusAccepted, AgentJobResponse{Job: job})
}

func (s *Server) agentUpdate(w http.ResponseWriter, r *http.Request) {
	if !AgentActions {
		writeError(w, http.StatusNotImplemented, "agent_actions_disabled", "agent install and update are disabled until the feature flag is turned on")
		return
	}
	id, ok := agentID(w, r)
	if !ok {
		return
	}
	job, err := s.installer.Update(id)
	if err != nil {
		if errors.Is(err, agentinstall.ErrInstallActive) {
			writeError(w, http.StatusConflict, "install_active", "an install or update job is already active for this agent's binary")
			return
		}
		if errors.Is(err, agentinstall.ErrNotInstalled) {
			writeError(w, http.StatusBadRequest, "not_installed", "agent is not installed; install it first")
			return
		}
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	if job.State == agentinstall.StateUnsupported {
		writeJSON(w, http.StatusOK, AgentJobResponse{Job: job})
		return
	}
	writeJSON(w, http.StatusAccepted, AgentJobResponse{Job: job})
}

func (s *Server) agentJob(w http.ResponseWriter, r *http.Request) {
	id, ok := agentID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, AgentJobResponse{Job: s.installer.JobOf(id)})
}
