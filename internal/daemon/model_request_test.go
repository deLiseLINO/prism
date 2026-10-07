package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/config"
)

func TestCustomModelDiscoveryUsesWireAuthenticationAndVersionedBase(t *testing.T) {
	for _, wire := range []config.Wire{config.WireOpenAIChat, config.WireOpenAIResponses, config.WireAnthropicMessages} {
		t.Run(string(wire), func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v4/models" || r.URL.Query().Get("tenant") != "fixture" {
					t.Errorf("model request=%s %s", r.Method, r.URL)
				}
				if wire == config.WireAnthropicMessages {
					if r.Header.Get("x-api-key") != "fixture-key" || r.Header.Get("anthropic-version") == "" || r.Header.Get("Authorization") != "" {
						t.Error("messages discovery authentication changed")
					}
				} else if r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("x-api-key") != "" {
					t.Error("bearer discovery authentication changed")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"data":[{"id":"model"}]}`))
			}))
			defer up.Close()
			syncer := modelSyncer{creds: credentialStore{file: newSyncFileStore(t, "edge", "fixture-key")}, client: up.Client()}
			listing, err := syncer.RemoteModels(context.Background(), "edge", config.Provider{Wire: wire, BaseURL: up.URL + "/api/v4/messages?tenant=fixture"})
			if err != nil || len(listing.Models) != 1 || listing.Models[0].ID != "model" {
				t.Fatalf("listing=%+v err=%v", listing, err)
			}
		})
	}
}
