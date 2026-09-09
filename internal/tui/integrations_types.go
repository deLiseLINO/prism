package tui

import (
	"strings"

	"prism/internal/integrations"
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
