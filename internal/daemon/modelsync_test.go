package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/management"
	"github.com/deLiseLINO/prism/internal/store"
)

func idsOf(rows []management.ListedModel) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out
}

// stubCreds records the lease the syncer resolved and answers a fixed
// credential, standing in for the refresher.
type stubCreds struct {
	gotLease account.Lease
	cred     account.Credential
	err      error
}

func (s *stubCreds) Credential(ctx context.Context, lease account.Lease) (account.Credential, error) {
	s.gotLease = lease
	return s.cred, s.err
}

func TestModelSyncerUsesOnlySelectedAccount(t *testing.T) {
	pool := account.New()
	pool.Register(account.Account{ID: "codex:a", Provider: "codex", State: account.Active, CredGen: 1, Version: 1, Priority: 5})
	pool.Register(account.Account{ID: "codex:b", Provider: "codex", State: account.Active, CredGen: 2, Version: 1})
	syncer := modelSyncer{pool: pool}

	lease, ok := syncer.leaseFor("codex", config.Provider{Pool: &config.PoolSettings{PinnedAccount: "codex:b"}})
	if !ok || lease.Account != "codex:b" || lease.CredGen != 2 {
		t.Fatalf("lease = %+v ok=%v, want selected codex:b gen 2", lease, ok)
	}
	if lease, ok := syncer.leaseFor("codex", config.Provider{}); ok {
		t.Fatalf("selected account missing but model sync used %+v", lease)
	}
	if lease, ok := syncer.leaseFor("codex", config.Provider{Pool: &config.PoolSettings{PinnedAccount: "codex:missing"}}); ok {
		t.Fatalf("missing selection used %+v", lease)
	}
}

func TestModelSyncerLeaseSkipsInactiveAndForeignAccounts(t *testing.T) {
	pool := account.New()
	pool.Register(account.Account{ID: "codex:paused", Provider: "codex", State: account.Paused, CredGen: 1, Version: 1})
	pool.Register(account.Account{ID: "ag:x", Provider: "ag", State: account.Active, CredGen: 1, Version: 1})
	syncer := modelSyncer{pool: pool}

	if _, ok := syncer.leaseFor("codex", config.Provider{}); ok {
		t.Fatal("paused codex account must not be leased")
	}
	if _, ok := syncer.leaseFor("ag", config.Provider{}); !ok {
		t.Fatal("active antigravity account must be leased for its own provider")
	}
}

func TestModelSyncerListsCodexModelsFromUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.URL.Query().Get("client_version") == "" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("chatgpt-account-id") != "acct-1" {
			t.Errorf("chatgpt-account-id: %q", r.Header.Get("chatgpt-account-id"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-5.6-terra","visibility":"list"},
			{"slug":"codex-auto-review","visibility":"hide"}]}`))
	}))
	t.Cleanup(server.Close)

	pool := account.New()
	pool.Register(account.Account{ID: "codex:a", Provider: "codex", State: account.Active, CredGen: 1, Version: 1})
	creds := &stubCreds{cred: account.Credential{Access: "tok", AccountID: "acct-1"}}
	syncer := modelSyncer{pool: pool, refresher: creds, client: server.Client()}

	models, err := syncer.RemoteModels(context.Background(), "codex", config.Provider{Wire: config.WireCodex, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("RemoteModels: %v", err)
	}
	if ids := idsOf(models); len(ids) != 1 || ids[0] != "gpt-5.6-terra" {
		t.Fatalf("models = %v, want only the visible slug", ids)
	}
	if creds.gotLease.Account != "codex:a" || creds.gotLease.Provider != "codex" {
		t.Fatalf("refresher lease = %+v, want codex/codex:a", creds.gotLease)
	}
}

func TestModelSyncerListsAntigravityModelsFromUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1internal:fetchAvailableModels" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"gemini-3.7-flash":{"quotaInfo":{"remainingPercentage":75}}}}`))
	}))
	t.Cleanup(server.Close)

	pool := account.New()
	pool.Register(account.Account{ID: "ag:a", Provider: "ag", State: account.Active, CredGen: 3, Version: 1})
	creds := &stubCreds{cred: account.Credential{Access: "tok", ProjectID: "proj-1"}}
	syncer := modelSyncer{pool: pool, refresher: creds, client: server.Client()}

	models, err := syncer.RemoteModels(context.Background(), "ag", config.Provider{Wire: config.WireAntigravity, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("RemoteModels: %v", err)
	}
	if ids := idsOf(models); len(ids) != 1 || ids[0] != "gemini-3.7-flash" {
		t.Fatalf("models = %v, want gemini-3.7-flash", ids)
	}
	if models[0].ContextWindow != nil || models[0].Image != nil {
		t.Fatalf("antigravity listing must stay id-only: %+v", models[0])
	}
	if creds.gotLease.Account != "ag:a" || creds.gotLease.CredGen != 3 {
		t.Fatalf("refresher lease = %+v, want ag:a gen 3", creds.gotLease)
	}
}

func TestModelSyncerListsClineModelsFromStoredAccount(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"free":[{"id":"cline-free/kimi-k3"}],"clinePass":[{"id":"cline-pass/glm-5.3"}],"recommended":[{"id":"skip"}],"clineCloud":[{"id":"skip-cloud"}]}`))
	}))
	t.Cleanup(server.Close)

	pool := account.New()
	pool.Register(account.Account{ID: "cline:usr-1", Provider: "cline", State: account.Active, CredGen: 4, Version: 1})
	creds := &stubCreds{cred: account.Credential{Access: "tok"}}
	syncer := modelSyncer{pool: pool, refresher: creds, client: server.Client()}

	models, err := syncer.RemoteModels(context.Background(), "cline", config.Provider{Wire: config.WireCline, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("RemoteModels: %v", err)
	}
	if gotPath != "/api/v1/ai/cline/recommended-models" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer workos:tok" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if ids := idsOf(models); len(ids) != 2 || ids[0] != "cline-free/kimi-k3" || ids[1] != "cline-pass/glm-5.3" {
		t.Fatalf("models = %v", ids)
	}
	if creds.gotLease.Account != "cline:usr-1" || creds.gotLease.CredGen != 4 {
		t.Fatalf("refresher lease = %+v", creds.gotLease)
	}
}

func TestModelSyncerErrorsWithoutActiveAccount(t *testing.T) {
	pool := account.New()
	syncer := modelSyncer{pool: pool, refresher: &stubCreds{}}

	_, err := syncer.RemoteModels(context.Background(), "codex", config.Provider{Wire: config.WireCodex})
	if err == nil || !strings.Contains(err.Error(), "no active account") {
		t.Fatalf("err = %v, want a no-active-account error", err)
	}
}

func TestModelSyncerCustomWireStillUsesBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
	}))
	t.Cleanup(server.Close)

	syncer := modelSyncer{creds: credentialStore{file: newSyncFileStore(t, "custom", "key")}, client: server.Client()}

	models, err := syncer.RemoteModels(context.Background(), "custom", config.Provider{Wire: config.WireOpenAIChat, BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatalf("RemoteModels: %v", err)
	}
	if ids := idsOf(models); len(ids) != 2 || ids[0] != "m1" {
		t.Fatalf("models = %v", ids)
	}

	_, err = syncer.RemoteModels(context.Background(), "custom", config.Provider{Wire: config.WireOpenAIChat})
	if err == nil || !strings.Contains(err.Error(), "no baseURL") {
		t.Fatalf("err = %v, want the baseURL error for custom wires", err)
	}
}

func TestModelSyncerCustomWireWorksWithoutCredential(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	t.Cleanup(server.Close)

	syncer := modelSyncer{creds: credentialStore{file: store.NewFileCredentialStore(t.TempDir())}, client: server.Client()}

	models, err := syncer.RemoteModels(context.Background(), "buddy", config.Provider{Wire: config.WireOpenAIChat, BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatalf("RemoteModels: %v", err)
	}
	if ids := idsOf(models); len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("models = %v, want [m1]", ids)
	}
	if auth != "" {
		t.Fatalf("Authorization = %q, want none", auth)
	}
}

// newSyncFileStore builds a credential store holding one custom-provider key.
func newSyncFileStore(t *testing.T, provider, secret string) *store.FileCredentialStore {
	t.Helper()
	file := store.NewFileCredentialStore(t.TempDir())
	ctx := context.Background()
	if err := file.PutIdempotent(ctx, account.ProviderID(provider), account.AccountID(provider+":default"), 1, []byte(secret)); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	return file
}
