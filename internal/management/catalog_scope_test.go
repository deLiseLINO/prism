package management

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/deLiseLINO/prism/internal/config"
)

func TestModelSyncRetainsCatalogsByExactProviderAccountAndEndpoint(t *testing.T) {
	base := "https://catalog.example"
	prior := config.ModelCatalog{BaseURL: base, Account: "ag:other", Project: "other-project", RawModels: []string{"gemini-3.7-flash-low"}}
	env, syncer := syncModelsEnv(t, config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"ag":    {Wire: config.WireAntigravity, BaseURL: base, Pool: &config.PoolSettings{PinnedAccount: "ag:selected"}, ModelCatalogs: []config.ModelCatalog{prior}},
		"other": {Wire: config.WireAntigravity, BaseURL: base, ModelCatalogs: []config.ModelCatalog{prior}},
	}})
	syncer.catalogs = map[string]*config.ModelCatalog{"ag": {BaseURL: base, Account: "ag:selected", Project: "selected-project", RawModels: []string{"gemini-3.7-flash-high"}}}
	syncer.models["ag"] = []ListedModel{{ID: "gemini-3.7-flash"}}
	stored := syncModelsRequest(t, env, "ag", []string{"gemini-3.7-flash"}, []string{"gemini-3.7-flash"})
	p := stored.Providers["ag"]
	if len(p.ModelCatalogs) != 2 || !reflect.DeepEqual(p.ModelCatalogs[0], prior) || !reflect.DeepEqual(stored.Providers["other"].ModelCatalogs, []config.ModelCatalog{prior}) {
		t.Fatalf("catalog scopes mixed: %+v", stored.Providers)
	}
	view := env.do(t, http.MethodGet, "/api/v1/providers", "")
	if view.Code != http.StatusOK {
		t.Fatalf("list=%d %s", view.Code, view.Body.String())
	}
	listed := decodeBody[ProvidersResponse](t, view)
	for _, provider := range listed.Providers {
		if provider.ID == "ag" && !reflect.DeepEqual(provider.RawModels, []string{"gemini-3.7-flash-high"}) {
			t.Fatalf("selected catalog leaked foreign members: %v", provider.RawModels)
		}
	}
	syncer.catalogs["ag"] = &config.ModelCatalog{BaseURL: base, Account: "ag:selected", Project: "selected-project", RawModels: []string{"gemini-3.7-flash-medium"}}
	syncModelsRequest(t, env, "ag", []string{"gemini-3.7-flash"}, []string{"gemini-3.7-flash"})
	p = env.cfg.Get().Config.Providers["ag"]
	if len(p.ModelCatalogs) != 2 || !reflect.DeepEqual(p.ModelCatalogs[1].RawModels, []string{"gemini-3.7-flash-medium"}) {
		t.Fatalf("same binding did not replace prior listing: %+v", p.ModelCatalogs)
	}
}
