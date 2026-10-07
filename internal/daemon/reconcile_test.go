package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/auth"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/integrations"
	"github.com/deLiseLINO/prism/internal/management"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/store"
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
	pool := account.New()
	env := newDaemonEnv(
		filepath.Join(dir, "credentials"),
		mgr,
		pool,
		newQuotaTable(pool, nil, nil),
		provider.NewRegistry(),
		credentialStore{file: store.NewFileCredentialStore(filepath.Join(dir, "credentials")), pool: pool, publication: &credentialPublication{cfg: mgr, refs: map[string]string{}}},
		http.DefaultClient,
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

	got, err := fetchModels(context.Background(), srv.Client(), config.Provider{Wire: config.WireOpenAIChat, BaseURL: srv.URL + "/v1"}, "secret-key")
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
	if _, err := fetchModels(context.Background(), srv.Client(), config.Provider{Wire: config.WireOpenAIChat, BaseURL: srv.URL}, ""); err == nil {
		t.Fatal("expected error on 500")
	}

	empty := modelsServer(t, `{"data":[]}`, nil)
	defer empty.Close()
	if _, err := fetchModels(context.Background(), empty.Client(), config.Provider{Wire: config.WireOpenAIChat, BaseURL: empty.URL}, ""); err == nil {
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
	var body atomic.Value
	body.Store(`{"data":[{"id":"m1"},{"id":"m2"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
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

	body.Store(`{"data":[{"id":"m2"}]}`)
	server := management.New(nil, mgr, nil, nil, env.creds, nil, integrations.NewRegistry(),
		modelSyncer{creds: env.creds, client: srv.Client()}, nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/providers/edge/sync-models?expectedGeneration="+strconv.FormatUint(mgr.Get().Generation, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var response management.ProviderMutationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if got := response.Provider.Models; len(got) != 1 || got[0] != "m2" {
		t.Fatalf("automatically discovered model survived removal: %v", got)
	}
	if got := mgr.Get().Config.Providers["edge"].Models; len(got) != 1 || got[0] != "m2" {
		t.Fatalf("removed model persisted: %v", got)
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
	if err := env.creds.file.Put(context.Background(), "edge", "edge:default", 1, []byte("test-key")); err != nil {
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
	lease, err := env.pool.Acquire(context.Background(), account.AcquireRequest{Provider: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := env.creds.resolve(context.Background(), provider.Target{Provider: "edge", APIKeyRef: got.APIKeyRef}, lease)
	if err != nil || string(key) != "test-key" || lease.CredGen != 1 {
		t.Fatalf("backfilled key not immediately active: %q gen=%d err=%v", key, lease.CredGen, err)
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

func TestLoginWithoutProviderCardCanPersist(t *testing.T) {
	env, _ := testEnv(t, config.Document{Version: config.SchemaVersion})
	env.ensureFlows("cline", config.WireCline)
	sink := auth.NewFileSink(env.creds.file, env.repos, env.pool)
	acct, err := sink.Persist(context.Background(), "cline", account.Credential{
		AccountID: "usr-1",
		Email:     "a@b.c",
		Access:    "workos:token",
		Refresh:   "refresh",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("persist without a provider card: %v", err)
	}
	if acct.ID != "cline:usr-1" || acct.Email != "a@b.c" {
		t.Fatalf("persisted account: %+v", acct)
	}
}

func TestStoredAccountSeedsMissingLoginProvider(t *testing.T) {
	env, mgr := testEnv(t, config.Document{Version: config.SchemaVersion})
	env.noteStoredAccount("cline")
	got, ok := mgr.Get().Config.Providers["cline"]
	if !ok {
		t.Fatal("cline card missing after stored account")
	}
	if got.Wire != config.WireCline || got.BaseURL != "https://api.cline.bot" {
		t.Fatalf("cline card: %+v", got)
	}
	if _, ok := env.registry.Lookup("cline"); !ok {
		t.Fatal("cline runner not registered")
	}
	env.noteStoredAccount("cline")
	if len(mgr.Get().Config.Providers) != 1 {
		t.Fatalf("second account wrote another card: %+v", mgr.Get().Config.Providers)
	}
}

func TestStoredAccountLeavesExistingLoginProvider(t *testing.T) {
	env, mgr := testEnv(t, config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"codex": {Wire: config.WireCodex, DefaultModel: "gpt-5.3"},
		},
	})
	env.noteStoredAccount("codex")
	got := mgr.Get().Config.Providers["codex"]
	if got.DefaultModel != "gpt-5.3" || got.Wire != config.WireCodex {
		t.Fatalf("existing card rewritten: %+v", got)
	}
}

func TestStoredAccountIgnoresCustomProvider(t *testing.T) {
	env, mgr := testEnv(t, config.Document{Version: config.SchemaVersion})
	env.noteStoredAccount("edge")
	if len(mgr.Get().Config.Providers) != 0 {
		t.Fatalf("custom provider seeded: %+v", mgr.Get().Config.Providers)
	}
}
