package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/integrations"
	"prism/internal/provider"
	"prism/internal/quota"
	"prism/internal/store"
)

type regPool struct {
	account.Pool
	mu         sync.Mutex
	registered []account.Account
}

func (p *regPool) Register(a account.Account) {
	p.mu.Lock()
	p.registered = append(p.registered, a)
	p.mu.Unlock()
	if concrete, ok := p.Pool.(interface{ Register(account.Account) }); ok {
		concrete.Register(a)
	}
}

func (p *regPool) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.registered)
}

func makeJWTClaims(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

type authFlowEnv struct {
	srv      *Server
	handler  http.Handler
	svc      *auth.Service
	pool     *regPool
	repoPath string
	credsDir string
	cfgPath  string
	verifier func() string
	secrets  []string
}

func newAuthFlowEnv(t *testing.T, accessSecret string) *authFlowEnv {
	t.Helper()
	dir := t.TempDir()
	credsDir := filepath.Join(dir, "creds")
	repoPath := filepath.Join(dir, "accounts", "codex.json")
	cfgPath := filepath.Join(dir, "config.json")
	cfg, err := config.Open(cfgPath)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	var captured struct {
		mu       sync.Mutex
		verifier string
	}
	accessToken := "AT-" + accessSecret
	refreshToken := "RT-" + accessSecret
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		captured.mu.Lock()
		captured.verifier = r.PostFormValue("code_verifier")
		captured.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  accessToken,
			"refresh_token": refreshToken,
			"expires_in":    3600,
			"id_token":      makeJWTClaims(t, map[string]any{"chatgpt_account_id": "acc-e2e", "email": "User@Example.com"}),
		})
	}))
	t.Cleanup(tokenSrv.Close)

	cfgFlow := auth.CodexProduction
	cfgFlow.CallbackPort = 0
	cfgFlow.TokenURL = tokenSrv.URL
	flow, err := auth.NewCodexFlow(cfgFlow, auth.Options{})
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	file := store.NewFileCredentialStore(credsDir)
	repo := account.OpenMeta(repoPath)
	pool := &regPool{Pool: account.New([]byte("0123456789abcdef0123456789abcdef"), time.Now)}
	svc, err := auth.New(auth.NewFileSink(file, map[account.ProviderID]*account.Repository{"codex": repo}, pool),
		map[account.ProviderID]auth.Flow{"codex": flow}, auth.Options{})
	if err != nil {
		t.Fatalf("auth service: %v", err)
	}
	t.Cleanup(func() { svc.Close(context.Background()) })

	credsAdapter := &fakeCreds{store: map[string][]byte{}}
	srv := New(pool, cfg, &fakeCatalog{models: []provider.Model{{ID: "gpt-5.3"}}},
		&fakeQuotaSource{snapshots: map[account.AccountID]quota.Snapshot{}}, credsAdapter, svc, integrations.NewRegistry(), nil)

	return &authFlowEnv{
		srv:      srv,
		handler:  srv.Handler(),
		svc:      svc,
		pool:     pool,
		repoPath: repoPath,
		credsDir: credsDir,
		cfgPath:  cfgPath,
		verifier: func() string {
			captured.mu.Lock()
			defer captured.mu.Unlock()
			return captured.verifier
		},
		secrets: []string{accessSecret, accessToken, refreshToken},
	}
}

func (e *authFlowEnv) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *authFlowEnv) start(t *testing.T) (session, state, loopbackURL string) {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/v1/auth/codex/start", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	var start AuthStartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if start.Session == "" || start.URL == "" {
		t.Fatalf("start = %+v", start)
	}
	idx := strings.Index(start.URL, "state=")
	if idx < 0 {
		t.Fatalf("no state in %q", start.URL)
	}
	state = start.URL[idx+6:]
	if end := strings.IndexByte(state, '&'); end >= 0 {
		state = state[:end]
	}
	marker := "redirect_uri=http%3A%2F%2Flocalhost%3A"
	ridx := strings.Index(start.URL, marker)
	if ridx < 0 {
		t.Fatalf("no redirect in %q", start.URL)
	}
	rest := start.URL[ridx+len(marker):]
	port := rest[:strings.IndexByte(rest, '%')]
	loopbackURL = fmt.Sprintf("http://127.0.0.1:%s/auth/callback", port)
	return start.Session, state, loopbackURL
}

func TestAuthFlowAutomaticLoopbackCallbackEndToEnd(t *testing.T) {
	secret := strings.Repeat("S", 4096)
	e := newAuthFlowEnv(t, secret)
	_, state, loopbackURL := e.start(t)

	resp, err := http.Get(loopbackURL + "?code=auto-code&state=" + state)
	if err != nil {
		t.Fatalf("loopback: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("loopback status = %d", resp.StatusCode)
	}

	rec := e.do(t, http.MethodGet, "/api/v1/auth/codex/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	st := decodeBody[AuthStatusResponse](t, rec)
	if st.State != "authorized" {
		t.Fatalf("state = %q", st.State)
	}

	accounts := e.do(t, http.MethodGet, "/api/v1/accounts", "")
	if accounts.Code != http.StatusOK {
		t.Fatalf("accounts = %d", accounts.Code)
	}
	list := decodeBody[AccountsResponse](t, accounts)
	if len(list.Accounts) != 1 || list.Accounts[0].ID != "codex:acc-e2e" {
		t.Fatalf("accounts = %+v", list.Accounts)
	}
	if e.pool.count() != 1 {
		t.Fatalf("registered = %d", e.pool.count())
	}
	if len(accounts.Body.String()) > 2048 {
		t.Fatalf("accounts response scaled with stored secret: %d bytes", accounts.Body.Len())
	}

	e.assertNoSecretLeak(t, []string{accounts.Body.String(), rec.Body.String(), e.metadataFile(t), e.configFile(t)})
}

func TestAuthFlowManualCallbackAndRelogin(t *testing.T) {
	e := newAuthFlowEnv(t, "first-secret")
	session, state, _ := e.start(t)

	wrong := e.do(t, http.MethodPost, "/api/v1/auth/codex/callback",
		fmt.Sprintf(`{"session":%q,"code":"c","state":"wrong"}`, session))
	assertErrorBody(t, wrong, http.StatusBadRequest, "state_mismatch")

	rec := e.do(t, http.MethodPost, "/api/v1/auth/codex/callback",
		fmt.Sprintf(`{"session":%q,"code":"c","state":%q}`, session, state))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("callback status = %d body=%s", rec.Code, rec.Body.String())
	}

	session2, state2, _ := e.start(t)
	rec2 := e.do(t, http.MethodPost, "/api/v1/auth/codex/callback",
		fmt.Sprintf(`{"session":%q,"code":"c2","state":%q}`, session2, state2))
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("relogin status = %d", rec2.Code)
	}

	repo := account.OpenMeta(e.repoPath)
	rows, err := repo.Load("codex")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].CredGen != 2 {
		t.Fatalf("credGen = %d, want 2", rows[0].CredGen)
	}
	if e.pool.count() != 2 {
		t.Fatalf("registrations = %d, want 2 (one per login)", e.pool.count())
	}

	file := store.NewFileCredentialStore(e.credsDir)
	blob, ok, err := file.Get(context.Background(), "codex", "codex:acc-e2e", 2)
	if err != nil || !ok {
		t.Fatalf("gen2 blob: ok=%v err=%v", ok, err)
	}
	cred, err := account.ParseCredential(blob)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cred.AccountID != "acc-e2e" || cred.Email != "user@example.com" {
		t.Fatalf("cred = %+v", cred)
	}

	e.assertNoSecretLeak(t, []string{e.metadataFile(t), e.configFile(t)})
}

func TestAuthFlowUnknownProviderRejected(t *testing.T) {
	e := newAuthFlowEnv(t, "s")
	rec := e.do(t, http.MethodPost, "/api/v1/auth/grok/start", "")
	assertErrorBody(t, rec, http.StatusBadRequest, "unknown_provider")
}

func TestAuthFlowWireAliasResolvesConfiguredProvider(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg, err := config.Open(cfgPath)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	doc := config.Document{
		Version:   config.SchemaVersion,
		Providers: map[string]config.Provider{"codex-main": {Wire: config.WireCodex, Models: []string{"gpt-5.2"}}},
	}
	if _, err := cfg.Update(doc, 0); err != nil {
		t.Fatalf("update config: %v", err)
	}

	cfgFlow := auth.CodexProduction
	cfgFlow.CallbackPort = 0
	flow, err := auth.NewCodexFlow(cfgFlow, auth.Options{})
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	file := store.NewFileCredentialStore(filepath.Join(dir, "creds"))
	repo := account.OpenMeta(filepath.Join(dir, "accounts", "codex-main.json"))
	pool := &regPool{Pool: account.New([]byte("0123456789abcdef0123456789abcdef"), time.Now)}
	svc, err := auth.New(auth.NewFileSink(file, map[account.ProviderID]*account.Repository{"codex-main": repo}, pool),
		map[account.ProviderID]auth.Flow{"codex-main": flow}, auth.Options{})
	if err != nil {
		t.Fatalf("auth service: %v", err)
	}
	t.Cleanup(func() { svc.Close(context.Background()) })

	srv := New(pool, cfg, &fakeCatalog{models: []provider.Model{{ID: "gpt-5.2"}}},
		&fakeQuotaSource{snapshots: map[account.AccountID]quota.Snapshot{}}, &fakeCreds{store: map[string][]byte{}}, svc, integrations.NewRegistry(), nil)
	h := srv.Handler()

	for _, route := range []string{"codex", "codex-main"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/"+route+"/start", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("route %q: start status = %d body=%s", route, rec.Code, rec.Body.String())
		}
		var start AuthStartResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
			t.Fatalf("route %q: decode: %v", route, err)
		}
		if start.Session == "" || start.URL == "" {
			t.Fatalf("route %q: start = %+v", route, start)
		}
	}
}

func (e *authFlowEnv) metadataFile(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(e.repoPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	return string(raw)
}

func (e *authFlowEnv) configFile(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(e.cfgPath)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(raw)
}

func (e *authFlowEnv) assertNoSecretLeak(t *testing.T, samples []string) {
	t.Helper()
	if v := e.verifier(); v != "" {
		e.secrets = append(e.secrets, v)
	}
	for i, sample := range samples {
		for _, secret := range e.secrets {
			if secret == "" {
				continue
			}
			if strings.Contains(sample, secret) {
				t.Fatalf("sample %d leaks secret bytes (len %d)", i, len(secret))
			}
		}
	}
}
