package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/integrations"
	"github.com/deLiseLINO/prism/internal/management"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/server"
	"github.com/deLiseLINO/prism/internal/store"
)

func credentialRuntime(t *testing.T, upstream string) (*daemonEnv, *config.Manager, http.Handler, http.Handler) {
	t.Helper()
	env, mgr := testEnv(t, config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"edge": {Wire: config.WireOpenAIChat, BaseURL: upstream + "/v1", Models: []string{"model"}, APIKeyRef: "edge:default"},
	}})
	if err := env.creds.file.Put(context.Background(), "edge", "edge:default", 1, []byte("old-key")); err != nil {
		t.Fatal(err)
	}
	env.reconcileOnce(context.Background())
	env.pool.(interface {
		SetPolicyWriter(func(account.Account) error)
	}).SetPolicyWriter(func(a account.Account) error { return env.repos[a.Provider].SavePolicy(a) })
	mgmt := management.New(env.pool, mgr, nil, nil, env.creds, nil, integrations.NewRegistry(), nil, nil).Handler()
	infer := server.New(server.Options{Planner: server.NewConfigPlanner(mgr), Registry: env.registry, Pool: env.pool, Config: mgr}).Handler()
	return env, mgr, mgmt, infer
}

func credentialPut(mgmt http.Handler, generation uint64, secret string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mgmt.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/providers/edge", strings.NewReader(fmt.Sprintf(`{"expectedGeneration":%d,"credential":%q,"defaultModel":"model"}`, generation, secret))))
	return rec
}

func credentialInfer(h http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"edge/model","messages":[{"role":"user","content":"hello"}]}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

func TestCredentialPublicationFailureKeepsActivePair(t *testing.T) {
	for _, failure := range []string{"staging", "config-write", "stale"} {
		t.Run(failure, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer old-key" {
					t.Errorf("unpublished key dispatched")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer up.Close()
			env, mgr, mgmt, infer := credentialRuntime(t, up.URL)
			before, policy := mgr.Get(), env.pool.Snapshot()
			path := filepath.Join(filepath.Dir(env.credentialPath), "prism.json")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			gen, want := before.Generation, http.StatusInternalServerError
			readPath := path
			switch failure {
			case "staging":
				blocked := filepath.Join(t.TempDir(), "not-directory")
				if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
				env.creds.file = store.NewFileCredentialStore(blocked)
				mgmt = management.New(env.pool, mgr, nil, nil, env.creds, nil, integrations.NewRegistry(), nil, nil).Handler()
			case "config-write":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				readPath += ".saved"
			case "stale":
				gen--
				want = http.StatusConflict
			}
			out := credentialPut(mgmt, gen, "new-key")
			if out.Code != want {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			if !reflect.DeepEqual(mgr.Get(), before) || !reflect.DeepEqual(env.pool.Snapshot(), policy) {
				t.Fatal("failed publication changed active state")
			}
			got, err := os.ReadFile(readPath)
			if err != nil || string(got) != string(original) {
				t.Fatalf("disk config changed: %v", err)
			}
			if failure == "config-write" {
				if out := credentialInfer(infer); out.Code == http.StatusOK || out.Code == http.StatusUnauthorized {
					t.Fatalf("unreadable config dispatched: %d %s", out.Code, out.Body.String())
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".saved", path); err != nil {
					t.Fatal(err)
				}
			}
			if out := credentialInfer(infer); out.Code != http.StatusOK {
				t.Fatalf("old key stopped working: %d %s", out.Code, out.Body.String())
			}
		})
	}
}

func TestOldKeyRejectionCannotParkReplacement(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer old-key" {
			close(started)
			<-release
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"rejected"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-key" {
			t.Errorf("wrong replacement key")
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	env, mgr, mgmt, infer := credentialRuntime(t, up.URL)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- credentialInfer(infer) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("old request did not reach upstream")
	}
	out := credentialPut(mgmt, mgr.Get().Generation, "new-key")
	close(release)
	if out.Code != http.StatusOK {
		t.Fatalf("rotation=%d %s", out.Code, out.Body.String())
	}
	<-done
	a := env.pool.Snapshot().Accounts[0]
	if a.State != account.Active || a.CredGen != 2 || a.InFlight != 0 {
		t.Fatalf("replacement parked: %+v", a)
	}
	if out := credentialInfer(infer); out.Code != http.StatusOK {
		t.Fatalf("replacement request=%d %s", out.Code, out.Body.String())
	}
}

func TestCredentialPublicationPausedRestartAndPendingFence(t *testing.T) {
	env, mgr, mgmt, _ := credentialRuntime(t, "http://127.0.0.1:9")
	ctx := context.Background()
	lease, err := env.pool.Acquire(ctx, account.AcquireRequest{Provider: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	prev := mgr.Get().Config.Providers["edge"]
	ref, err := env.creds.Stage(ctx, "edge", prev.APIKeyRef, []byte("new-key"))
	if err != nil {
		t.Fatal(err)
	}
	snap := mgr.Get()
	next := prev
	next.APIKeyRef = ref
	snap.Config.Providers["edge"] = next
	if _, err := mgr.Update(snap.Config, snap.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := env.creds.resolve(ctx, provider.Target{Provider: "edge", APIKeyRef: ref}, lease); err == nil {
		t.Fatal("pending credential dispatched with old lease")
	}
	if err := env.creds.Committed(ctx, "edge", prev, next); err != nil {
		t.Fatal(err)
	}
	if err := env.pool.Pause(ctx, lease.Account, env.pool.Snapshot().Accounts[0].Version); err != nil {
		t.Fatal(err)
	}
	if out := credentialPut(mgmt, mgr.Get().Generation, "third-key"); out.Code != http.StatusOK {
		t.Fatalf("rotation=%d %s", out.Code, out.Body.String())
	}
	if env.pool.Snapshot().Accounts[0].State != account.Paused {
		t.Fatal("replacement resumed user pause")
	}
	reopened, err := config.Open(filepath.Join(filepath.Dir(env.credentialPath), "prism.json"))
	if err != nil {
		t.Fatal(err)
	}
	pool := account.New()
	cs := credentialStore{file: env.creds.file, pool: pool, publication: &credentialPublication{cfg: reopened, refs: map[string]string{}}}
	restarted := newDaemonEnv(env.credentialPath, reopened, pool, newQuotaTable(pool, nil, nil), provider.NewRegistry(), cs, http.DefaultClient, http.DefaultClient)
	if err := restarted.ensureProvider(ctx, "edge", reopened.Get().Config.Providers["edge"]); err != nil {
		t.Fatal(err)
	}
	a := pool.Snapshot().Accounts[0]
	if a.State != account.Paused || a.CredGen != 3 {
		t.Fatalf("restart lost policy/generation: %+v", a)
	}
	if err := pool.Resume(ctx, a.ID, a.Version); err != nil {
		t.Fatal(err)
	}
	active, err := pool.Acquire(ctx, account.AcquireRequest{Provider: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := cs.resolve(ctx, provider.Target{Provider: "edge", APIKeyRef: reopened.Get().Config.Providers["edge"].APIKeyRef}, active)
	if err != nil || string(got) != "third-key" {
		t.Fatalf("restart key=%q err=%v", got, err)
	}
}

func TestCustomReplacementRecoversOnlyChangedRejectedCredential(t *testing.T) {
	env, mgr, mgmt, _ := credentialRuntime(t, "http://127.0.0.1:9")
	ctx := context.Background()
	lease, err := env.pool.Acquire(ctx, account.AcquireRequest{Provider: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.pool.Record(ctx, lease, account.AuthRejected{}); err != nil {
		t.Fatal(err)
	}
	before := env.pool.Snapshot().Accounts[0]
	if out := credentialPut(mgmt, mgr.Get().Generation, "old-key"); out.Code != http.StatusOK {
		t.Fatalf("same key=%d %s", out.Code, out.Body.String())
	}
	a := env.pool.Snapshot().Accounts[0]
	if a.State != account.NeedsReauth || a.CredGen != before.CredGen || a.Version != before.Version {
		t.Fatalf("unchanged rejected key recovered: %+v", a)
	}
	if out := credentialPut(mgmt, mgr.Get().Generation, "new-key"); out.Code != http.StatusOK {
		t.Fatalf("new key=%d %s", out.Code, out.Body.String())
	}
	lease, err = env.pool.Acquire(ctx, account.AcquireRequest{Provider: "edge"})
	if err != nil || lease.CredGen != 2 {
		t.Fatalf("replacement remains parked: %+v err=%v", lease, err)
	}
}

func TestManagementCannotPublishUncommittedCredentialReference(t *testing.T) {
	env, mgr, mgmt, _ := credentialRuntime(t, "http://127.0.0.1:9")
	before := mgr.Get()
	ref, err := env.creds.Stage(context.Background(), "edge", before.Config.Providers["edge"].APIKeyRef, []byte("unpublished-key"))
	if err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	mgmt.ServeHTTP(out, httptest.NewRequest(http.MethodPut, "/api/v1/providers/edge", strings.NewReader(fmt.Sprintf(`{"expectedGeneration":%d,"apiKeyRef":%q}`, before.Generation, ref))))
	if out.Code != http.StatusBadRequest {
		t.Fatalf("uncommitted ref publication=%d %s", out.Code, out.Body.String())
	}
	if !reflect.DeepEqual(before, mgr.Get()) {
		t.Fatal("ref rejection mutated config")
	}
	if env.pool.Snapshot().Accounts[0].CredGen != 1 {
		t.Fatal("ref rejection advanced account")
	}
}
