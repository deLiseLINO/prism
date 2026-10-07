package server

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestPlannerTargetsClineOverChat(t *testing.T) {
	doc := config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"cline": {Wire: config.WireCline, BaseURL: "https://gw.example", Models: []string{"m1"}},
	}}
	p := &ConfigPlanner{get: func() config.Document { return doc }}
	plan, ok := p.Plan("cline/m1")
	if !ok || len(plan.Targets) != 1 {
		t.Fatalf("plan = %+v, ok=%v", plan, ok)
	}
	if got := plan.Targets[0].Wire; got != provider.WireChat {
		t.Fatalf("cline target wire = %d, want chat (%d)", got, provider.WireChat)
	}
}
