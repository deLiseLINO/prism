package tui

import (
	"strings"

	"prism/internal/integrations"
	"prism/internal/management"
)

type integrationStatus struct {
	ID        string
	Installed bool
	Managed   bool
	Drift     bool
	Detail    string
}

func integrationStatusesFromList(items []integrations.Status) []integrationStatus {
	out := make([]integrationStatus, 0, len(items))
	for _, item := range items {
		out = append(out, integrationStatus{
			ID:        string(item.ID),
			Installed: item.Installed,
			Managed:   item.Managed,
			Drift:     item.Drift,
			Detail:    strings.TrimSpace(item.Detail),
		})
	}
	return out
}

const managementHostLocal = "local"

type hostView struct {
	ID     string
	Local  bool
	Status string
	Detail string
}

func hostsFromList(items []management.HostView) []hostView {
	out := make([]hostView, 0, len(items))
	for _, item := range items {
		out = append(out, hostView{ID: item.ID, Local: item.Local, Status: item.Status, Detail: strings.TrimSpace(item.Detail)})
	}
	return out
}

func (m *Model) cycleActiveHost() {
	if len(m.HostsView) < 2 {
		return
	}
	m.HostsCursor = (m.HostsCursor + 1) % len(m.HostsView)
	m.ActiveHost = m.HostsView[m.HostsCursor].ID
	m.IntegrationsCursor = 0
	m.IntegrationConfirm = ""
}

func (m Model) activeHostLabel() string {
	if m.ActiveHost == "" || m.ActiveHost == managementHostLocal {
		return "local"
	}
	return m.ActiveHost
}

func (m Model) activeHostID() string {
	if m.ActiveHost == "" {
		return managementHostLocal
	}
	return m.ActiveHost
}
