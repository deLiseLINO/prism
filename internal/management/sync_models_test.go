package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/deLiseLINO/prism/internal/config"
)

func syncModelsEnv(t *testing.T, doc config.Document) (*testEnv, *fakeSyncer) {
	t.Helper()
	env := newEnv(t)
	if _, err := env.cfg.Update(doc, 0); err != nil {
		t.Fatal(err)
	}
	syncer := &fakeSyncer{models: map[string][]ListedModel{}}
	env.srv.syncer = syncer
	return env, syncer
}

func syncModelsRequest(t *testing.T, env *testEnv, id string, wantModels, wantSynced []string) config.Document {
	t.Helper()
	generation := env.cfg.Get().Generation
	rec := env.do(t, http.MethodPost, "/api/v1/providers/"+id+"/sync-models?expectedGeneration="+strconv.FormatUint(generation, 10), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d, body=%s", rec.Code, rec.Body.String())
	}
	response := decodeBody[ProviderMutationResponse](t, rec)
	if response.Generation != generation+1 {
		t.Fatalf("generation = %d, want %d", response.Generation, generation+1)
	}
	if !slices.Equal(response.Provider.Models, wantModels) || !slices.Equal(response.Provider.SyncedModels, wantSynced) {
		t.Fatalf("response models = %v, synced = %v; want %v, %v", response.Provider.Models, response.Provider.SyncedModels, wantModels, wantSynced)
	}
	doc := env.cfg.Get().Config
	stored := doc.Providers[id]
	if !slices.Equal(stored.Models, wantModels) || !slices.Equal(stored.SyncedModels, wantSynced) {
		t.Fatalf("stored models = %v, synced = %v; want %v, %v", stored.Models, stored.SyncedModels, wantModels, wantSynced)
	}
	return doc
}

func TestSyncModelsRemovesPreviousListingAndPreservesManualState(t *testing.T) {
	for _, defaultModel := range []string{"model-old", "model-kept"} {
		t.Run(defaultModel, func(t *testing.T) {
			env, syncer := syncModelsEnv(t, config.Document{
				Version: config.SchemaVersion,
				Providers: map[string]config.Provider{
					"edge": {Wire: config.WireOpenAIChat, BaseURL: "https://edge.example/v1"},
				},
			})
			syncer.models["edge"] = []ListedModel{{ID: "model-old"}, {ID: "model-kept"}}
			doc := syncModelsRequest(t, env, "edge", []string{"model-kept", "model-old"}, []string{"model-old", "model-kept"})
			p := doc.Providers["edge"]
			p.Models = append(p.Models, "model-manual")
			p.DefaultModel = defaultModel
			p.DisabledModels = []string{"model-old", "model-kept", "model-manual"}
			p.ModelSettings = map[string]config.ModelSettings{
				"model-old":    {ContextWindow: 1000},
				"model-kept":   {ContextWindow: 2000, ImageInput: boolPtr(false)},
				"model-manual": {ContextWindow: 3000, ReasoningEfforts: []string{"low", "high"}},
			}
			doc.Providers["edge"] = p
			if _, err := env.cfg.Update(doc, env.cfg.Get().Generation); err != nil {
				t.Fatal(err)
			}
			syncer.models["edge"] = []ListedModel{{ID: "model-kept"}, {ID: "model-new"}}
			stored := syncModelsRequest(t, env, "edge", []string{"model-kept", "model-manual", "model-new"}, []string{"model-kept", "model-new"}).Providers["edge"]
			if !slices.Equal(stored.DisabledModels, []string{"model-kept", "model-manual"}) {
				t.Fatalf("disabled models = %v", stored.DisabledModels)
			}
			wantSettings := map[string]config.ModelSettings{
				"model-kept":   {ContextWindow: 2000, ImageInput: boolPtr(false)},
				"model-manual": {ContextWindow: 3000, ReasoningEfforts: []string{"low", "high"}},
			}
			if !reflect.DeepEqual(stored.ModelSettings, wantSettings) {
				t.Fatalf("settings = %+v, want %+v", stored.ModelSettings, wantSettings)
			}
			wantDefault := ""
			if defaultModel == "model-kept" {
				wantDefault = "model-kept"
			}
			if stored.DefaultModel != wantDefault {
				t.Fatalf("default model = %q, want %q", stored.DefaultModel, wantDefault)
			}
		})
	}
}

func TestSyncModelsPrunesOnlyRemovedModelReferences(t *testing.T) {
	other := config.Provider{Wire: config.WireOpenAIChat, BaseURL: "https://other.example/v1", Models: []string{"model-old"}, DefaultModel: "model-old", DisabledModels: []string{"model-old"}, ModelSettings: map[string]config.ModelSettings{"model-old": {ContextWindow: 4000}}}
	mixed := config.Combo{Strategy: config.ComboRoundRobin, StickyLimit: 2, DisplayName: "Mixed", Targets: []config.Target{{Provider: "edge", Model: "model-old", Weight: 3}, {Provider: "edge", Model: "model-kept", Weight: 4}, {Provider: "other", Model: "model-old", Weight: 5}}}
	env, syncer := syncModelsEnv(t, config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"edge":  {Wire: config.WireOpenAIChat, BaseURL: "https://edge.example/v1", Models: []string{"model-old", "model-kept", "model-old-extra"}, SyncedModels: []string{"model-old", "model-kept"}, ModelSettings: map[string]config.ModelSettings{"model-old": {ImageInput: boolPtr(true)}}},
			"other": other,
		},
		Routes:  map[string]string{"removed": "edge/model-old", "kept": "edge/model-kept", "prefix": "edge/model-old-extra", "other": "other/model-old", "empty": "empty", "mixed": "mixed"},
		Aliases: map[string]string{"removed-alias": "edge/model-old", "kept-alias": "edge/model-kept", "other-alias": "other/model-old", "empty-alias": "empty", "mixed-alias": "mixed"},
		Combos: map[string]config.Combo{
			"empty":      {Strategy: config.ComboFailover, Targets: []config.Target{{Provider: "edge", Model: "model-old"}}},
			"mixed":      mixed,
			"other-only": {Strategy: config.ComboFailover, Targets: []config.Target{{Provider: "other", Model: "model-old"}}},
		},
		VisionSidecar: config.VisionSidecarSettings{Enabled: true, Target: "edge/model-old"},
	})
	before := env.cfg.Get().Config
	syncer.models["edge"] = []ListedModel{{ID: "model-kept"}}
	stored := syncModelsRequest(t, env, "edge", []string{"model-kept", "model-old-extra"}, []string{"model-kept"})
	wantRoutes := map[string]string{"kept": "edge/model-kept", "prefix": "edge/model-old-extra", "other": "other/model-old", "mixed": "mixed"}
	wantAliases := map[string]string{"kept-alias": "edge/model-kept", "other-alias": "other/model-old", "mixed-alias": "mixed"}
	if !reflect.DeepEqual(stored.Routes, wantRoutes) || !reflect.DeepEqual(stored.Aliases, wantAliases) {
		t.Fatalf("routes = %v, aliases = %v; want %v, %v", stored.Routes, stored.Aliases, wantRoutes, wantAliases)
	}
	mixed.Targets = []config.Target{{Provider: "edge", Model: "model-kept", Weight: 4}, {Provider: "other", Model: "model-old", Weight: 5}}
	wantCombos := map[string]config.Combo{"mixed": mixed, "other-only": before.Combos["other-only"]}
	if !reflect.DeepEqual(stored.Combos, wantCombos) {
		t.Fatalf("combos = %+v, want %+v", stored.Combos, wantCombos)
	}
	if stored.VisionSidecar != (config.VisionSidecarSettings{}) {
		t.Fatalf("removed vision target survived: %+v", stored.VisionSidecar)
	}
	if !reflect.DeepEqual(stored.Providers["other"], before.Providers["other"]) {
		t.Fatalf("other provider changed: %+v", stored.Providers["other"])
	}
}

func TestSyncModelsManualProvenanceBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		prior      []string
		remote     []ListedModel
		wantModels []string
		wantSynced []string
	}{
		{name: "empty-success", prior: []string{"model-old"}, wantModels: []string{"model-manual"}},
		{name: "no-prior-listing", remote: []ListedModel{{ID: "model-new"}}, wantModels: []string{"model-manual", "model-new", "model-old"}, wantSynced: []string{"model-new"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, syncer := syncModelsEnv(t, config.Document{
				Version: config.SchemaVersion,
				Providers: map[string]config.Provider{
					"edge": {Wire: config.WireOpenAIChat, BaseURL: "https://edge.example/v1", Models: []string{"model-old", "model-manual"}, SyncedModels: tc.prior, DefaultModel: "model-manual", DisabledModels: []string{"model-manual"}, ModelSettings: map[string]config.ModelSettings{"model-manual": {ContextWindow: 3000}}},
				},
			})
			syncer.models["edge"] = tc.remote
			p := syncModelsRequest(t, env, "edge", tc.wantModels, tc.wantSynced).Providers["edge"]
			if p.DefaultModel != "model-manual" || !slices.Equal(p.DisabledModels, []string{"model-manual"}) || !reflect.DeepEqual(p.ModelSettings, map[string]config.ModelSettings{"model-manual": {ContextWindow: 3000}}) {
				t.Fatalf("manual state changed: %+v", p)
			}
		})
	}
}

func TestSyncModelsRemovesRawAntigravityFamilyState(t *testing.T) {
	for _, mode := range []config.ModelMode{config.ModelModeLogical, config.ModelModeRaw} {
		t.Run(string(mode), func(t *testing.T) {
			env, syncer := syncModelsEnv(t, config.Document{
				Version: config.SchemaVersion,
				Providers: map[string]config.Provider{
					"ag": {
						Wire: config.WireAntigravity, ModelMode: mode,
						BaseURL:        "https://catalog.example",
						ModelCatalogs:  []config.ModelCatalog{{BaseURL: "https://catalog.example", Account: "ag:a", Project: "project", RawModels: []string{"gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high", "gemini-3.7-flash-tiered"}}},
						Models:         []string{"gemini-3.6-flash-low", "gemini-3.6-flash-high", "gemini-3.7-flash-low", "gemini-3.7-flash-high", "model-manual"},
						SyncedModels:   []string{"gemini-3.6-flash-low", "gemini-3.6-flash-high", "gemini-3.7-flash-low", "gemini-3.7-flash-high"},
						DisabledModels: []string{"gemini-3.6-flash-low", "gemini-3.7-flash-low"},
						ModelSettings:  map[string]config.ModelSettings{"gemini-3.6-flash-high": {ContextWindow: 1000}, "gemini-3.7-flash-high": {ContextWindow: 2000}},
					},
				},
			})
			syncer.models["ag"] = []ListedModel{{ID: "gemini-3.7-flash"}}
			wantSynced := []string{"gemini-3.7-flash"}
			wantModels := []string{"gemini-3.7-flash", "model-manual"}
			if mode == config.ModelModeRaw {
				wantSynced = []string{"gemini-3.7-flash-high", "gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-tiered"}
				wantModels = []string{"gemini-3.7-flash-high", "gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-tiered", "model-manual"}
			}
			p := syncModelsRequest(t, env, "ag", wantModels, wantSynced).Providers["ag"]
			if !slices.Equal(p.DisabledModels, wantSynced) {
				t.Fatalf("disabled models = %v, want %v", p.DisabledModels, wantSynced)
			}
			wantSettings := map[string]config.ModelSettings{"gemini-3.7-flash": {ContextWindow: 2000}}
			if mode == config.ModelModeRaw {
				wantSettings = map[string]config.ModelSettings{
					"gemini-3.7-flash-high":   {ContextWindow: 2000},
					"gemini-3.7-flash-low":    {ContextWindow: 2000},
					"gemini-3.7-flash-medium": {ContextWindow: 2000},
					"gemini-3.7-flash-tiered": {ContextWindow: 2000},
				}
			}
			if !reflect.DeepEqual(p.ModelSettings, wantSettings) {
				t.Fatalf("settings = %+v, want %+v", p.ModelSettings, wantSettings)
			}
		})
	}
}

func TestSyncModelsPreservesManuallyReaddedModel(t *testing.T) {
	env, syncer := syncModelsEnv(t, config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"edge": {Wire: config.WireOpenAIChat, BaseURL: "https://edge.example/v1"},
		},
	})
	syncer.models["edge"] = []ListedModel{{ID: "model-old"}, {ID: "model-kept"}}
	syncModelsRequest(t, env, "edge", []string{"model-kept", "model-old"}, []string{"model-old", "model-kept"})
	for _, models := range []string{`["model-kept"]`, `["model-kept","model-old"]`} {
		synced, err := json.Marshal(env.cfg.Get().Config.Providers["edge"].SyncedModels)
		if err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"models":%s,"syncedModels":%s,"expectedGeneration":%d}`, models, synced, env.cfg.Get().Generation)
		rec := env.do(t, http.MethodPut, "/api/v1/providers/edge", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("replace status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if !slices.Equal(env.cfg.Get().Config.Providers["edge"].SyncedModels, []string{"model-kept"}) {
			t.Fatalf("manual edit retained stale sync provenance: %v", env.cfg.Get().Config.Providers["edge"].SyncedModels)
		}
	}
	syncer.models["edge"] = []ListedModel{{ID: "model-kept"}}
	syncModelsRequest(t, env, "edge", []string{"model-kept", "model-old"}, []string{"model-kept"})
}

func TestSyncModelsKeepsCustomProviderIDsExact(t *testing.T) {
	for _, wire := range []config.Wire{config.WireOpenAIChat, config.WireOpenAIResponses, config.WireAnthropicMessages} {
		t.Run(string(wire), func(t *testing.T) {
			env, syncer := syncModelsEnv(t, config.Document{
				Version: config.SchemaVersion,
				Providers: map[string]config.Provider{
					"edge": {Wire: wire, BaseURL: "https://edge.example/v1", Models: []string{"gemini-3.6-flash-high", "gemini-3.7-flash-high"}, SyncedModels: []string{"gemini-3.6-flash-high"}, ModelSettings: map[string]config.ModelSettings{"gemini-3.6-flash-high": {ContextWindow: 1000}, "gemini-3.7-flash-high": {ContextWindow: 2000}}},
				},
			})
			syncer.models["edge"] = []ListedModel{{ID: "gemini-3.8-flash-high"}}
			p := syncModelsRequest(t, env, "edge", []string{"gemini-3.7-flash-high", "gemini-3.8-flash-high"}, []string{"gemini-3.8-flash-high"}).Providers["edge"]
			if !reflect.DeepEqual(p.ModelSettings, map[string]config.ModelSettings{"gemini-3.7-flash-high": {ContextWindow: 2000}}) {
				t.Fatalf("custom provider settings = %+v", p.ModelSettings)
			}
		})
	}
}
