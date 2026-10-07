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

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestRejectedCustomCredentialRecoverySurvivesRestart(t *testing.T) {
	env, mgr, mgmt, _ := credentialRuntime(t, "http://127.0.0.1:9")
	ctx := context.Background()
	lease, err := env.pool.Acquire(ctx, account.AcquireRequest{Provider: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.pool.Record(ctx, lease, account.AuthRejected{}); err != nil {
		t.Fatal(err)
	}
	a := env.pool.Snapshot().Accounts[0]
	if err := env.pool.UpdatePriority(ctx, a.ID, 17, a.Version); err != nil {
		t.Fatal(err)
	}
	if out := credentialPut(mgmt, mgr.Get().Generation, "recovered-key"); out.Code != http.StatusOK {
		t.Fatalf("replacement=%d %s", out.Code, out.Body)
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
	a = pool.Snapshot().Accounts[0]
	if a.State != account.Active || a.Priority != 17 || a.CredGen != 2 {
		t.Fatalf("restarted account=%+v", a)
	}
	lease, err = pool.Acquire(ctx, account.AcquireRequest{Provider: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := cs.resolve(ctx, provider.Target{Provider: "edge", APIKeyRef: reopened.Get().Config.Providers["edge"].APIKeyRef}, lease)
	if err != nil || string(key) != "recovered-key" {
		t.Fatalf("key=%q error=%v", key, err)
	}
}

func TestCredentialReferenceClearCannotStrandLaterReplacement(t *testing.T) {
	env, mgr, mgmt, _ := credentialRuntime(t, "http://127.0.0.1:9")
	for _, secret := range []string{"second-key", "third-key"} {
		if out := credentialPut(mgmt, mgr.Get().Generation, secret); out.Code != http.StatusOK {
			t.Fatalf("replacement=%d %s", out.Code, out.Body)
		}
	}
	before := env.pool.Snapshot().Accounts[0]
	beforeConfig := mgr.Get()
	cleared := httptest.NewRecorder()
	mgmt.ServeHTTP(cleared, httptest.NewRequest(http.MethodPut, "/api/v1/providers/edge", strings.NewReader(fmt.Sprintf(`{"expectedGeneration":%d,"apiKeyRef":""}`, beforeConfig.Generation))))
	if cleared.Code != http.StatusBadRequest || mgr.Get().Generation != beforeConfig.Generation || mgr.Get().Config.Providers["edge"].APIKeyRef != beforeConfig.Config.Providers["edge"].APIKeyRef {
		t.Fatalf("clear=%d %s config=%+v", cleared.Code, cleared.Body, mgr.Get())
	}
	if out := credentialPut(mgmt, mgr.Get().Generation, "fourth-key"); out.Code != http.StatusOK {
		t.Fatalf("replacement=%d %s", out.Code, out.Body)
	}
	lease, err := env.pool.Acquire(context.Background(), account.AcquireRequest{Provider: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := env.creds.resolve(context.Background(), provider.Target{Provider: "edge", APIKeyRef: mgr.Get().Config.Providers["edge"].APIKeyRef}, lease)
	if err != nil || string(key) != "fourth-key" || lease.CredGen <= before.CredGen {
		t.Fatalf("before=%d lease=%+v key=%q error=%v", before.CredGen, lease, key, err)
	}
}

func TestCustomRecoveryPolicyFailureDoesNotPublishRuntime(t *testing.T) {
	env, mgr, mgmt, _ := credentialRuntime(t, "http://127.0.0.1:9")
	ctx := context.Background()
	lease, err := env.pool.Acquire(ctx, account.AcquireRequest{Provider: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.pool.Record(ctx, lease, account.AuthRejected{}); err != nil {
		t.Fatal(err)
	}
	a := env.pool.Snapshot().Accounts[0]
	if err := env.pool.UpdatePriority(ctx, a.ID, 17, a.Version); err != nil {
		t.Fatal(err)
	}
	before := env.pool.Snapshot().Accounts[0]
	beforeConfig := mgr.Get()
	beforeRef := env.creds.publication.refs["edge"]
	path := filepath.Join(env.credentialPath, "accounts", "edge.json")
	saved := path + ".saved"
	if err := os.Rename(path, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	out := credentialPut(mgmt, beforeConfig.Generation, "replacement-key")
	if out.Code != http.StatusInternalServerError || !strings.Contains(out.Body.String(), "credential_publication") {
		t.Fatalf("replacement=%d %s", out.Code, out.Body)
	}
	afterConfig := mgr.Get()
	if afterConfig.Generation != beforeConfig.Generation+1 || afterConfig.Config.Providers["edge"].APIKeyRef == beforeConfig.Config.Providers["edge"].APIKeyRef {
		t.Fatalf("post-CAS config=%+v", afterConfig)
	}
	if !reflect.DeepEqual(env.pool.Snapshot().Accounts[0], before) || env.creds.publication.refs["edge"] != beforeRef {
		t.Fatalf("failed recovery published runtime: %+v", env.pool.Snapshot())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, path); err != nil {
		t.Fatal(err)
	}
	if err := env.creds.Committed(ctx, "edge", beforeConfig.Config.Providers["edge"], afterConfig.Config.Providers["edge"]); err != nil {
		t.Fatal(err)
	}
	a = env.pool.Snapshot().Accounts[0]
	if a.State != account.Active || a.Priority != 17 || a.CredGen != 2 {
		t.Fatalf("retry=%+v", a)
	}
	lease, err = env.pool.Acquire(ctx, account.AcquireRequest{Provider: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := env.creds.resolve(ctx, provider.Target{Provider: "edge", APIKeyRef: afterConfig.Config.Providers["edge"].APIKeyRef}, lease)
	if err != nil || string(key) != "replacement-key" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}
