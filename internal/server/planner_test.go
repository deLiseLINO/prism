package server

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestTargetCarriesModelImageInput(t *testing.T) {
	doc := config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"p": {
				Wire:    config.WireOpenAIChat,
				BaseURL: "http://localhost",
				ModelSettings: map[string]config.ModelSettings{
					"vision-model": {ImageInput: boolPtr(true)},
				},
			},
		},
	}
	p := &ConfigPlanner{get: func() config.Document { return doc }}
	plan, ok := p.Plan("p/vision-model")
	if !ok {
		t.Fatal("plan not found")
	}
	if !plan.Targets[0].ImageInput {
		t.Fatal("ImageInput = false, want true")
	}
	plan, ok = p.Plan("p/text-model")
	if !ok {
		t.Fatal("plan not found")
	}
	if plan.Targets[0].ImageInput {
		t.Fatal("ImageInput = true, want false")
	}
}

func TestTargetUsesDiscoveredImage(t *testing.T) {
	on := true
	off := false
	doc := config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"p": {
				Wire:    config.WireOpenAIChat,
				BaseURL: "http://localhost",
				Discovered: map[string]config.DiscoveredFacts{
					"listed": {Image: &on},
					"forced": {Image: &on},
				},
				ModelSettings: map[string]config.ModelSettings{
					"forced": {ImageInput: &off},
				},
			},
		},
	}
	p := &ConfigPlanner{get: func() config.Document { return doc }}
	plan, ok := p.Plan("p/listed")
	if !ok || !plan.Targets[0].ImageInput {
		t.Fatal("discovered image did not make the target image-capable")
	}
	plan, ok = p.Plan("p/forced")
	if !ok || plan.Targets[0].ImageInput {
		t.Fatal("explicit false did not reject discovered image")
	}
}

func TestTargetPicksModelWireOverride(t *testing.T) {
	doc := config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"p": {
				Wire:    config.WireOpenAIResponses,
				BaseURL: "http://localhost",
				ModelSettings: map[string]config.ModelSettings{
					"chatty": {Wire: config.WireOpenAIChat},
				},
			},
		},
	}
	p := &ConfigPlanner{get: func() config.Document { return doc }}
	cases := map[string]provider.Wire{
		"p/chatty": provider.WireChat,
		"p/plain":  provider.WireResponses,
	}
	for ref, want := range cases {
		plan, ok := p.Plan(canon.ModelID(ref))
		if !ok {
			t.Fatalf("%s: plan not found", ref)
		}
		if got := plan.Targets[0].Wire; got != want {
			t.Fatalf("%s: wire = %d, want %d", ref, got, want)
		}
	}
}
