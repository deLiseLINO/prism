package daemon

import (
	"context"
	"log"

	"github.com/deLiseLINO/prism/internal/integrations"
)

// watchIntegrations keeps applied client configs converged on the enabled set:
// an apply pass runs at startup so a daemon restart lands every enabled module,
// and every config change replays the pass, so toggles, hand-edits, and
// configs pushed by the main machine alike reach the files. Refusals are
// logged, never fatal.
func watchIntegrations(ctx context.Context, changes <-chan struct{}, ints *integrations.Registry) {
	applyEnabled(ints)
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			applyEnabled(ints)
		}
	}
}

func applyEnabled(ints *integrations.Registry) {
	for _, result := range ints.ApplyEnabled() {
		log.Printf("prism: auto-apply %s: %s", result.ID, result.Reason)
	}
}
