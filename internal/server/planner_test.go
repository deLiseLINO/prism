package server

import (
	"path/filepath"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/catalog"
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

type outputCatalog map[string]catalog.Facts

func (c outputCatalog) Lookup(id string) catalog.Facts { return c[id] }

func TestTargetCarriesCatalogMaxOutput(t *testing.T) {
	m, err := config.Open(filepath.Join(t.TempDir(), "prism.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.SetCatalog(outputCatalog{"sonnet-x": {MaxOutput: 64000}})
	doc := m.Get().Config
	doc.Providers = map[string]config.Provider{"p": {Wire: config.WireOpenAIChat, BaseURL: "http://localhost"}}
	p := &ConfigPlanner{get: func() config.Document { return doc }}
	for model, want := range map[string]int{"sonnet-x": 64000, "unlisted": 0} {
		plan, ok := p.Plan(canon.ModelID("p/" + model))
		if !ok {
			t.Fatalf("%s: plan not found", model)
		}
		if got := plan.Targets[0].MaxOutputTokens; got != want {
			t.Fatalf("%s: MaxOutputTokens = %d, want %d", model, got, want)
		}
	}
}
