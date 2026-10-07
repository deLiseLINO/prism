package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestPeriodicCatalogRefreshRequiresEnabledExactManagedBinding(t *testing.T) {
	for _, state := range []string{"disabled", "enabled", "foreign-path"} {
		t.Run(state, func(t *testing.T) {
			env, mgr := testEnv(t, config.Document{Version: config.SchemaVersion, Integrations: map[string]config.IntegrationSettings{"codex": {Enabled: state != "disabled"}}})
			path := filepath.Join(t.TempDir(), "config.toml")
			models := []integrations.Model{{ID: "edge/old", Name: "Old", ContextWindow: 16000}}
			integration := integrations.NewCodex(integrations.CodexOptions{Port: 4101, ConfigPath: path, ModelsSource: func() []integrations.Model { return models }})
			if result := integration.Apply(); !result.OK {
				t.Fatalf("apply=%+v", result)
			}
			catalogPath := integrations.CodexCatalogPath(path)
			before, err := os.ReadFile(catalogPath)
			if err != nil {
				t.Fatal(err)
			}
			if state == "foreign-path" {
				foreign := filepath.Join(filepath.Dir(path), "foreign.json")
				if err := os.WriteFile(path, []byte("model_catalog_json = \""+foreign+"\"\nopenai_base_url = \"http://localhost:4101/v1\"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			models = []integrations.Model{{ID: "edge/new", Name: "New", ContextWindow: 32000}}
			env.codex = integration
			env.reconcileOnce(context.Background())
			after, err := os.ReadFile(catalogPath)
			if err != nil {
				t.Fatal(err)
			}
			changed := string(before) != string(after)
			if changed != (state == "enabled") {
				t.Fatalf("catalog overwrite state=%s changed=%t generation=%d", state, changed, mgr.Get().Generation)
			}
		})
	}
}
