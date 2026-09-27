package server

import (
	"testing"

	"prism/internal/config"
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
