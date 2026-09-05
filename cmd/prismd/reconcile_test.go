package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/config"
	"prism/internal/provider"
	"prism/internal/store"
)

func testEnv(t *testing.T, doc config.Document) (*daemonEnv, *config.Manager) {
	t.Helper()
	dir := t.TempDir()
	mgr, err := config.Open(filepath.Join(dir, "prism.json"))
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	if _, err := mgr.Update(doc, 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	pool := account.New([]byte("test-secret-0123456789abcdef"), time.Now)
	env := newDaemonEnv(
		filepath.Join(dir, "credentials"),
		mgr,
		pool,
		newQuotaTable(pool, nil, nil),
		provider.NewRegistry(),
		credentialStore{file: store.NewFileCredentialStore(filepath.Join(dir, "credentials"))},
		http.DefaultClient,
	)
	return env, mgr
}

func modelsServer(t *testing.T, body string, check func(r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestFetchModelsParsesOpenAIList(t *testing.T) {
	var auth string
	srv := modelsServer(t, `{"object":"list","data":[{"id":"b"},{"id":"a"},{"id":"a"},{"id":""}]}`, func(r *http.Request) {
		auth = r.Header.Get("Authorization")
	})
	defer srv.Close()

	got, err := fetchModels(context.Background(), srv.Client(), srv.URL+"/v1", "secret-key")
	if err != nil {
		t.Fatalf("fetch models: %v", err)
	}
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("unexpected models: %v", got)
	}
	if auth != "Bearer secret-key" {
		t.Fatalf("auth header not forwarded: %q", auth)
	}
}

func TestFetchModelsFailsCleanly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := fetchModels(context.Background(), srv.Client(), srv.URL, ""); err == nil {
		t.Fatal("expected error on 500")
	}

	empty := modelsServer(t, `{"data":[]}`, nil)
	defer empty.Close()
	if _, err := fetchModels(context.Background(), empty.Client(), empty.URL, ""); err == nil {
		t.Fatal("expected error on empty list")
	}
}

func TestEnsureProviderRegistersCustomRunner(t *testing.T) {
	env, _ := testEnv(t, config.Document{Version: config.SchemaVersion})
	p := config.Provider{Wire: config.WireOpenAIResponses, BaseURL: "http://127.0.0.1:9/v1"}

	if err := env.ensureProvider(context.Background(), "edge", p); err != nil {
		t.Fatalf("ensure provider: %v", err)
	}
	if _, ok := env.registry.Lookup("edge"); !ok {
		t.Fatal("runner missing after ensure")
	}
	if err := env.ensureProvider(context.Background(), "edge", p); err != nil {
		t.Fatalf("second ensure not idempotent: %v", err)
	}
}

func TestReconcileFillsEmptyModels(t *testing.T) {
	srv := modelsServer(t, `{"data":[{"id":"m1"},{"id":"m2"}]}`, nil)
	defer srv.Close()

	env, mgr := testEnv(t, config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"edge": {Wire: config.WireOpenAIResponses, BaseURL: srv.URL + "/v1"},
		},
	})
	env.lastDiscover["edge"] = time.Now().Add(-10 * time.Minute)

	env.reconcileOnce(context.Background())

	got := mgr.Get().Config.Providers["edge"].Models
	if len(got) != 2 || got[0] != "m1" || got[1] != "m2" {
		t.Fatalf("models not discovered: %v", got)
	}
	if _, ok := env.registry.Lookup("edge"); !ok {
		t.Fatal("runner missing after reconcile")
	}
}

func TestReconcileSetsKeyRefFromStoredCredential(t *testing.T) {
	srv := modelsServer(t, `{"data":[{"id":"m1"}]}`, nil)
	defer srv.Close()

	env, mgr := testEnv(t, config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"edge": {Wire: config.WireOpenAIResponses, BaseURL: srv.URL + "/v1"},
		},
	})
	if err := env.creds.Put(context.Background(), "edge", []byte("test-key")); err != nil {
		t.Fatalf("store credential: %v", err)
	}

	env.reconcileOnce(context.Background())

	got := mgr.Get().Config.Providers["edge"]
	if got.APIKeyRef != "edge:default" {
		t.Fatalf("key ref not set: %q", got.APIKeyRef)
	}
	if len(got.Models) != 1 || got.Models[0] != "m1" {
		t.Fatalf("models not discovered: %v", got.Models)
	}
}

func TestReconcileKeepsExistingModels(t *testing.T) {
	srv := modelsServer(t, `{"data":[{"id":"intruder"}]}`, nil)
	defer srv.Close()

	env, mgr := testEnv(t, config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"edge": {Wire: config.WireOpenAIResponses, BaseURL: srv.URL, Models: []string{"pinned"}},
		},
	})

	env.reconcileOnce(context.Background())

	got := mgr.Get().Config.Providers["edge"].Models
	if len(got) != 1 || got[0] != "pinned" {
		t.Fatalf("existing models overwritten: %v", got)
	}
}
