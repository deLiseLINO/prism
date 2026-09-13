package main

import (
	"context"
	"log"

	"prism/internal/config"
	"prism/internal/integrations"
)

// watchIntegrations keeps applied client configs converged on the enabled set:
// an apply pass runs at startup so a daemon restart lands every enabled module,
// and every config change replays the pass, so toggles and hand-edits alike
// reach the files. Refusals are logged, never fatal.
func watchIntegrations(ctx context.Context, cfg *config.Manager, ints *integrations.Registry) {
	applyEnabled(ints)
	for {
		select {
		case <-ctx.Done():
			return
		case <-cfg.Changes():
			applyEnabled(ints)
		}
	}
}

func applyEnabled(ints *integrations.Registry) {
	for _, result := range ints.ApplyEnabled() {
		log.Printf("prismd: auto-apply %s: %s", result.ID, result.Reason)
	}
}
