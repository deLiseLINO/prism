package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/agentinstall"
	"prism/internal/config"
	"prism/internal/integrations"
)
type fakeInstaller struct {
	statuses  []agentinstall.AgentStatus
	install   map[integrations.ID]agentinstall.Job
	installOK map[integrations.ID]error
	update    map[integrations.ID]agentinstall.Job
	updateOK  map[integrations.ID]error
	jobs      map[integrations.ID]agentinstall.Job
	forced    []integrations.ID
}

func (f *fakeInstaller) StatusAll() []agentinstall.AgentStatus { return f.statuses }

func (f *fakeInstaller) StatusOf(id integrations.ID) (agentinstall.AgentStatus, bool) {
	for _, st := range f.statuses {
		if st.ID == id {
			return st, true
		}
	}
	return agentinstall.AgentStatus{}, false
}

func (f *fakeInstaller) Install(id integrations.ID, force bool) (agentinstall.Job, error) {
	if force {
		f.forced = append(f.forced, id)
	}
	if err, ok := f.installOK[id]; ok {
		return agentinstall.Job{}, err
	}
	if job, ok := f.install[id]; ok {
		return job, nil
	}
	return agentinstall.Job{Key: string(id), State: agentinstall.StateInstalling}, nil
}

func (f *fakeInstaller) Update(id integrations.ID) (agentinstall.Job, error) {
	if err, ok := f.updateOK[id]; ok {
		return agentinstall.Job{}, err
	}
	if job, ok := f.update[id]; ok {
		return job, nil
	}
	return agentinstall.Job{Key: string(id), State: agentinstall.StateRunning}, nil
}

func (f *fakeInstaller) JobOf(id integrations.ID) agentinstall.Job {
	if job, ok := f.jobs[id]; ok {
		return job
	}
	return agentinstall.Job{Key: string(id), State: agentinstall.StateIdle}
}

func agentsEnv(t *testing.T, installer *fakeInstaller) *httptest.Server {
	t.Helper()
	m, err := config.Open(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&fakePool{}, m, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}}, &fakeAuth{}, integrations.NewRegistry(), nil, installer)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func agentsGet(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	res, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func agentsPost(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	res, err := http.Post(ts.URL+path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
func allAgentStatuses() []agentinstall.AgentStatus {
	out := make([]agentinstall.AgentStatus, 0, len(integrations.IDs))
	for _, id := range integrations.IDs {
		out = append(out, agentinstall.AgentStatus{ID: id, Key: string(id)})
	}
	return out
}

func TestAgentsListRendersStatuses(t *testing.T) {
	ts := agentsEnv(t, &fakeInstaller{statuses: allAgentStatuses()})
	res := agentsGet(t, ts, "/api/v1/agents")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var body struct {
		Agents         []agentinstall.AgentStatus `json:"agents"`
		ActionsEnabled bool                       `json:"actionsEnabled"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != len(integrations.IDs) {
		t.Fatalf("expected %d agents, got %d", len(integrations.IDs), len(body.Agents))
	}
	if body.Agents[0].ID != integrations.Codex {
		t.Fatalf("expected first agent codex, got %s", body.Agents[0].ID)
	}
	if body.ActionsEnabled {
		t.Fatal("actionsEnabled should be false by default")
	}
}

func TestAgentsListReportsActionsEnabled(t *testing.T) {
	AgentActions = true
	t.Cleanup(func() { AgentActions = false })
	ts := agentsEnv(t, &fakeInstaller{statuses: allAgentStatuses()})
	res := agentsGet(t, ts, "/api/v1/agents")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var body struct {
		ActionsEnabled bool `json:"actionsEnabled"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.ActionsEnabled {
		t.Fatal("actionsEnabled should mirror the switch")
	}
}

func TestParseAgentActionsEnv(t *testing.T) {
	for _, raw := range []string{"1", "true"} {
		if !ParseAgentActionsEnv(raw) {
			t.Fatalf("ParseAgentActionsEnv(%q) should be true", raw)
		}
	}
	for _, raw := range []string{"", "0", "false", "yes", "TRUE", "on"} {
		if ParseAgentActionsEnv(raw) {
			t.Fatalf("ParseAgentActionsEnv(%q) should be false", raw)
		}
	}
}

func TestAgentGetKnownAndUnknown(t *testing.T) {
	ts := agentsEnv(t, &fakeInstaller{statuses: allAgentStatuses()})
	res := agentsGet(t, ts, "/api/v1/agents/grok")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var status agentinstall.AgentStatus
	if err := json.NewDecoder(res.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.ID != integrations.Grok {
		t.Fatalf("expected grok, got %s", status.ID)
	}

	unknown := agentsGet(t, ts, "/api/v1/agents/frobnicate")
	defer unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", unknown.StatusCode)
	}
}

func TestAgentInstallAcceptedAndForceForwarded(t *testing.T) {
	AgentActions = true
	t.Cleanup(func() { AgentActions = false })
	installer := &fakeInstaller{statuses: allAgentStatuses()}
	ts := agentsEnv(t, installer)
	res := agentsPost(t, ts, "/api/v1/agents/codex/install")
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", res.StatusCode)
	}
	forced := agentsPost(t, ts, "/api/v1/agents/claude/install?force=true")
	defer forced.Body.Close()
	if forced.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", forced.StatusCode)
	}
	if len(installer.forced) != 1 || installer.forced[0] != integrations.Claude {
		t.Fatalf("force not forwarded: %v", installer.forced)
	}
}

func TestAgentInstallConflictAndUnsupported(t *testing.T) {
	AgentActions = true
	t.Cleanup(func() { AgentActions = false })
	installer := &fakeInstaller{
		statuses:  allAgentStatuses(),
		installOK: map[integrations.ID]error{integrations.Omp: agentinstall.ErrInstallActive},
		install: map[integrations.ID]agentinstall.Job{
			integrations.Opencode2: {Key: "opencode2", State: agentinstall.StateUnsupported, Error: "no plan"},
		},
	}
	ts := agentsEnv(t, installer)

	conflict := agentsPost(t, ts, "/api/v1/agents/omp/install")
	defer conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", conflict.StatusCode)
	}
	var conflictBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(conflict.Body).Decode(&conflictBody); err != nil {
		t.Fatal(err)
	}
	if conflictBody.Error.Code != "install_active" {
		t.Fatalf("expected install_active, got %s", conflictBody.Error.Code)
	}

	unsupported := agentsPost(t, ts, "/api/v1/agents/opencode2/install")
	defer unsupported.Body.Close()
	if unsupported.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for unsupported plan, got %d", unsupported.StatusCode)
	}
	var jobBody struct {
		Job agentinstall.Job `json:"job"`
	}
	if err := json.NewDecoder(unsupported.Body).Decode(&jobBody); err != nil {
		t.Fatal(err)
	}
	if jobBody.Job.State != agentinstall.StateUnsupported {
		t.Fatalf("expected unsupported state, got %s", jobBody.Job.State)
	}
}

func TestAgentUpdateNotInstalledAndConflict(t *testing.T) {
	AgentActions = true
	t.Cleanup(func() { AgentActions = false })
	installer := &fakeInstaller{
		statuses: allAgentStatuses(),
		updateOK: map[integrations.ID]error{
			integrations.Hermes: agentinstall.ErrNotInstalled,
			integrations.Codex:  errors.Join(agentinstall.ErrInstallActive),
		},
	}
	ts := agentsEnv(t, installer)

	missing := agentsPost(t, ts, "/api/v1/agents/hermes/update")
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", missing.StatusCode)
	}

	conflict := agentsPost(t, ts, "/api/v1/agents/codex/update")
	defer conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", conflict.StatusCode)
	}
}

func TestAgentJobIdleWhenNeverRan(t *testing.T) {
	ts := agentsEnv(t, &fakeInstaller{statuses: allAgentStatuses()})
	res := agentsGet(t, ts, "/api/v1/agents/pi/job")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var body struct {
		Job agentinstall.Job `json:"job"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Job.State != agentinstall.StateIdle {
		t.Fatalf("expected idle, got %s", body.Job.State)
	}
}

func TestAgentMutationsDisabledByDefault(t *testing.T) {
	installer := &fakeInstaller{statuses: allAgentStatuses()}
	ts := agentsEnv(t, installer)

	for _, path := range []string{
		"/api/v1/agents/codex/install",
		"/api/v1/agents/codex/update",
	} {
		res := agentsPost(t, ts, path)
		defer res.Body.Close()
		if res.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s: expected 501, got %d", path, res.StatusCode)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != "agent_actions_disabled" {
			t.Fatalf("%s: expected agent_actions_disabled, got %s", path, body.Error.Code)
		}
	}

	// Read routes stay open while the flag is off.
	status := agentsGet(t, ts, "/api/v1/agents/codex")
	defer status.Body.Close()
	if status.StatusCode != http.StatusOK {
		t.Fatalf("status route should stay open, got %d", status.StatusCode)
	}
	job := agentsGet(t, ts, "/api/v1/agents/codex/job")
	defer job.Body.Close()
	if job.StatusCode != http.StatusOK {
		t.Fatalf("job route should stay open, got %d", job.StatusCode)
	}
}
