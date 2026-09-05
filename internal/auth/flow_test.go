package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/account"
)

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func makeJWT(claims map[string]any) string {
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return b64url([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + b64url(payload) + ".signature"
}

func testNow() time.Time { return time.Unix(1_700_000_000, 0) }

type capturedRequest struct {
	mu     sync.Mutex
	bodies []urlValues
	r      *http.Request
}

type urlValues map[string]string

func (c *capturedRequest) add(form urlValues) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, form)
}

func (c *capturedRequest) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func codexTokenServer(t *testing.T, captured *capturedRequest, resp any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		form := urlValues{}
		for k, v := range r.PostForm {
			form[k] = v[0]
		}
		captured.add(form)
		jsonResponse(w, resp)
	}))
}

func TestCodexProductionConfigMatchesReference(t *testing.T) {
	cfg := CodexProduction
	if cfg.ClientID != "app_EMoamEEZ73f0CkXaXp7hrann" {
		t.Fatalf("client id = %q", cfg.ClientID)
	}
	if cfg.AuthURL != "https://auth.openai.com/oauth/authorize" || cfg.TokenURL != "https://auth.openai.com/oauth/token" {
		t.Fatalf("codex endpoints = %q %q", cfg.AuthURL, cfg.TokenURL)
	}
	wantScopes := "openid profile email offline_access api.connectors.read api.connectors.invoke"
	if strings.Join(cfg.Scopes, " ") != wantScopes {
		t.Fatalf("scopes = %q", strings.Join(cfg.Scopes, " "))
	}
	if cfg.CallbackHost != "localhost" || cfg.BindHost != "127.0.0.1" || cfg.CallbackPort != 1455 || cfg.CallbackPath != "/auth/callback" {
		t.Fatalf("codex callback = %s:%d%s bind %s", cfg.CallbackHost, cfg.CallbackPort, cfg.CallbackPath, cfg.BindHost)
	}
	if cfg.RedirectURI(1455) != "http://localhost:1455/auth/callback" {
		t.Fatalf("redirect uri = %q", cfg.RedirectURI(1455))
	}
}

func TestAntigravityProductionConfigMatchesReference(t *testing.T) {
	cfg := AntigravityProduction
	if cfg.ClientID != "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com" {
		t.Fatalf("client id = %q", cfg.ClientID)
	}
	if cfg.ClientSecret == "" {
		t.Fatal("missing embedded google client secret")
	}
	if cfg.AuthURL != "https://accounts.google.com/o/oauth2/v2/auth" || cfg.TokenURL != "https://oauth2.googleapis.com/token" {
		t.Fatalf("google endpoints = %q %q", cfg.AuthURL, cfg.TokenURL)
	}
	if cfg.UserInfoURL != "https://www.googleapis.com/oauth2/v2/userinfo" {
		t.Fatalf("userinfo = %q", cfg.UserInfoURL)
	}
	if cfg.ProjectAPI != "https://cloudcode-pa.googleapis.com" || cfg.OnboardAPI != "https://daily-cloudcode-pa.googleapis.com" || cfg.APIVersion != "v1internal" {
		t.Fatalf("cloud code apis = %q %q %q", cfg.ProjectAPI, cfg.OnboardAPI, cfg.APIVersion)
	}
	wantScopes := strings.Join([]string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/cclog",
		"https://www.googleapis.com/auth/experimentsandconfigs",
	}, " ")
	if strings.Join(cfg.Scopes, " ") != wantScopes {
		t.Fatalf("scopes = %q", strings.Join(cfg.Scopes, " "))
	}
	if cfg.RedirectURI(51121) != "http://127.0.0.1:51121/callback" {
		t.Fatalf("redirect uri = %q", cfg.RedirectURI(51121))
	}
	if !strings.HasPrefix(cfg.UserAgent, "antigravity/ide/2.5.5 (os_type=windows; arch=amd64; aidev_client; auth_method=oauth)") {
		t.Fatalf("user agent = %q", cfg.UserAgent)
	}
}

func TestPKCES256ChallengeMatchesRFC7636Vector(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := s256Challenge(verifier); got != want {
		t.Fatalf("challenge = %q, want %q", got, want)
	}
}

func TestCodexExchangeDerivesIdentityFromTokenClaims(t *testing.T) {
	captured := &capturedRequest{}
	idToken := makeJWT(map[string]any{
		"chatgpt_account_id": "acc-77",
		"email":              "User@Example.COM",
	})
	tokenSrv := codexTokenServer(t, captured, map[string]any{
		"access_token":  makeJWT(map[string]any{"chatgpt_account_id": "ignored"}),
		"refresh_token": "refresh-1",
		"expires_in":    7200,
		"id_token":      idToken,
	})
	defer tokenSrv.Close()

	cfg := CodexProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = tokenSrv.URL
	flow, err := NewCodexFlow(cfg, Options{Now: testNow})
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	cred, err := flow.Exchange(context.Background(), "code-1", "verifier-1", "http://localhost:0/auth/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if cred.AccountID != "acc-77" {
		t.Fatalf("account id = %q", cred.AccountID)
	}
	if cred.Email != "user@example.com" {
		t.Fatalf("email = %q", cred.Email)
	}
	if cred.Access == "" || cred.Refresh != "refresh-1" {
		t.Fatalf("tokens = %q %q", cred.Access, cred.Refresh)
	}
	if !cred.ExpiresAt.Equal(testNow().Add(7200 * time.Second)) {
		t.Fatalf("expires = %v", cred.ExpiresAt)
	}
	if captured.count() != 1 {
		t.Fatalf("token requests = %d", captured.count())
	}
	form := captured.bodies[0]
	for key, want := range map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "app_EMoamEEZ73f0CkXaXp7hrann",
		"code":          "code-1",
		"code_verifier": "verifier-1",
		"redirect_uri":  "http://localhost:0/auth/callback",
	} {
		if form[key] != want {
			t.Fatalf("form[%q] = %q, want %q", key, form[key], want)
		}
	}
}

func TestCodexExchangeAcceptsNamespacedAccountClaim(t *testing.T) {
	captured := &capturedRequest{}
	tokenSrv := codexTokenServer(t, captured, map[string]any{
		"access_token": makeJWT(map[string]any{
			"email":                       "a@b.co",
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc-ns"},
		}),
	})
	defer tokenSrv.Close()
	cfg := CodexProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = tokenSrv.URL
	flow, _ := NewCodexFlow(cfg, Options{})
	cred, err := flow.Exchange(context.Background(), "c", "v", "http://localhost:0/auth/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if cred.AccountID != "acc-ns" || cred.Email != "a@b.co" {
		t.Fatalf("identity = %q %q", cred.AccountID, cred.Email)
	}
}

func TestCodexExchangeFailsClosedWithoutIdentity(t *testing.T) {
	captured := &capturedRequest{}
	tokenSrv := codexTokenServer(t, captured, map[string]any{
		"access_token": makeJWT(map[string]any{"sub": "someone"}),
	})
	defer tokenSrv.Close()
	cfg := CodexProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = tokenSrv.URL
	flow, _ := NewCodexFlow(cfg, Options{})
	_, err := flow.Exchange(context.Background(), "c", "v", "http://localhost:0/auth/callback")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "identity") {
		t.Fatalf("error = %v", err)
	}

	tokenSrv2 := codexTokenServer(t, captured, map[string]any{
		"access_token": "not-a-jwt",
		"id_token":     makeJWT(map[string]any{"chatgpt_account_id": "acc-1"}),
	})
	defer tokenSrv2.Close()
	cfg.TokenURL = tokenSrv2.URL
	flow2, _ := NewCodexFlow(cfg, Options{})
	_, err = flow2.Exchange(context.Background(), "c", "v", "http://localhost:0/auth/callback")
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("missing email must fail closed, got %v", err)
	}
}

func TestAntigravityExchangeIdentityAndProject(t *testing.T) {
	captured := &capturedRequest{}
	tokenSrv := codexTokenServer(t, captured, map[string]any{
		"access_token":  "at-1",
		"refresh_token": "rt-1",
		"expires_in":    3600,
	})
	defer tokenSrv.Close()

	var userinfoAuth string
	userinfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userinfoAuth = r.Header.Get("Authorization")
		jsonResponse(w, map[string]any{"email": "Dev@Example.com", "id": "g-42"})
	}))
	defer userinfoSrv.Close()

	var loadAssistUA string
	projectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loadAssistUA = r.Header.Get("User-Agent")
		if !strings.HasSuffix(r.URL.Path, ":loadCodeAssist") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		jsonResponse(w, map[string]any{"cloudaicompanionProject": "proj-9"})
	}))
	defer projectSrv.Close()

	cfg := AntigravityProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = tokenSrv.URL
	cfg.UserInfoURL = userinfoSrv.URL
	cfg.ProjectAPI = projectSrv.URL
	cfg.OnboardAPI = projectSrv.URL
	flow, err := NewAntigravityFlow(cfg, Options{Now: testNow})
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	cred, err := flow.Exchange(context.Background(), "code-2", "verifier-2", "http://127.0.0.1:0/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if cred.AccountID != "g-42" || cred.Email != "dev@example.com" || cred.ProjectID != "proj-9" {
		t.Fatalf("identity = %q %q %q", cred.AccountID, cred.Email, cred.ProjectID)
	}
	if userinfoAuth != "Bearer at-1" {
		t.Fatalf("userinfo auth = %q", userinfoAuth)
	}
	if !cred.ExpiresAt.Equal(testNow().Add(3600*time.Second - refreshSkew)) {
		t.Fatalf("expires = %v", cred.ExpiresAt)
	}
	form := captured.bodies[0]
	if form["client_secret"] != AntigravityProduction.ClientSecret {
		t.Fatalf("client secret missing from token request")
	}
	if form["code_verifier"] != "verifier-2" {
		t.Fatalf("code verifier = %q", form["code_verifier"])
	}
	if loadAssistUA != AntigravityProduction.UserAgent {
		t.Fatalf("loadCodeAssist UA = %q", loadAssistUA)
	}
}

func TestAntigravityOnboardFallback(t *testing.T) {
	captured := &capturedRequest{}
	tokenSrv := codexTokenServer(t, captured, map[string]any{"access_token": "at", "refresh_token": "rt"})
	defer tokenSrv.Close()
	userinfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]any{"email": "x@y.z", "id": "g-1"})
	}))
	defer userinfoSrv.Close()

	var onboardBodies []map[string]any
	onboardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		onboardBodies = append(onboardBodies, body)
		jsonResponse(w, map[string]any{"done": true, "response": map[string]any{"projectId": "proj-onboard"}})
	}))
	defer onboardSrv.Close()

	cfg := AntigravityProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = tokenSrv.URL
	cfg.UserInfoURL = userinfoSrv.URL
	cfg.ProjectAPI = "http://127.0.0.1:1"
	cfg.OnboardAPI = onboardSrv.URL
	flow, _ := NewAntigravityFlow(cfg, Options{OnboardPoll: time.Millisecond})
	cred, err := flow.Exchange(context.Background(), "c", "v", "http://127.0.0.1:0/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if cred.ProjectID != "proj-onboard" {
		t.Fatalf("project = %q", cred.ProjectID)
	}
	if len(onboardBodies) != 1 {
		t.Fatalf("onboard calls = %d", len(onboardBodies))
	}
	meta, _ := onboardBodies[0]["metadata"].(map[string]any)
	if onboardBodies[0]["tier_id"] != "free-tier" || meta["ide_type"] != "ANTIGRAVITY" || meta["ide_name"] != "antigravity" || meta["ide_version"] != "2.5.5" {
		t.Fatalf("onboard body = %+v", onboardBodies[0])
	}
}

func TestAntigravityOnboardRetriesTransientThenSucceeds(t *testing.T) {
	captured := &capturedRequest{}
	tokenSrv := codexTokenServer(t, captured, map[string]any{"access_token": "at", "refresh_token": "rt"})
	defer tokenSrv.Close()
	userinfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]any{"email": "x@y.z", "id": "g-1"})
	}))
	defer userinfoSrv.Close()
	calls := 0
	onboardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{"done": true, "response": map[string]any{"cloudaicompanionProject": "proj-3"}})
	}))
	defer onboardSrv.Close()

	cfg := AntigravityProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = tokenSrv.URL
	cfg.UserInfoURL = userinfoSrv.URL
	cfg.ProjectAPI = "http://127.0.0.1:1"
	cfg.OnboardAPI = onboardSrv.URL
	flow, _ := NewAntigravityFlow(cfg, Options{OnboardPoll: time.Millisecond})
	cred, err := flow.Exchange(context.Background(), "c", "v", "http://127.0.0.1:0/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if cred.ProjectID != "proj-3" || calls != 3 {
		t.Fatalf("project = %q calls = %d", cred.ProjectID, calls)
	}
}

func TestAntigravityOnboardStopsOnHardClientError(t *testing.T) {
	captured := &capturedRequest{}
	tokenSrv := codexTokenServer(t, captured, map[string]any{"access_token": "at", "refresh_token": "rt"})
	defer tokenSrv.Close()
	userinfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]any{"email": "x@y.z", "id": "g-1"})
	}))
	defer userinfoSrv.Close()
	calls := 0
	onboardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer onboardSrv.Close()

	cfg := AntigravityProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = tokenSrv.URL
	cfg.UserInfoURL = userinfoSrv.URL
	cfg.ProjectAPI = "http://127.0.0.1:1"
	cfg.OnboardAPI = onboardSrv.URL
	flow, _ := NewAntigravityFlow(cfg, Options{OnboardPoll: time.Millisecond})
	_, err := flow.Exchange(context.Background(), "c", "v", "http://127.0.0.1:0/callback")
	if err == nil {
		t.Fatal("expected refusal without project")
	}
	if calls != 1 {
		t.Fatalf("onboard calls = %d, want 1 (hard 4xx stops)", calls)
	}
}

func TestAntigravityExchangeRefusesWithoutProject(t *testing.T) {
	captured := &capturedRequest{}
	tokenSrv := codexTokenServer(t, captured, map[string]any{"access_token": "at", "refresh_token": "rt"})
	defer tokenSrv.Close()
	userinfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]any{"email": "x@y.z", "id": "g-1"})
	}))
	defer userinfoSrv.Close()
	projectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]any{"unrelated": true})
	}))
	defer projectSrv.Close()

	cfg := AntigravityProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = tokenSrv.URL
	cfg.UserInfoURL = userinfoSrv.URL
	cfg.ProjectAPI = projectSrv.URL
	cfg.OnboardAPI = projectSrv.URL
	flow, _ := NewAntigravityFlow(cfg, Options{OnboardPoll: time.Millisecond})
	_, err := flow.Exchange(context.Background(), "c", "v", "http://127.0.0.1:0/callback")
	if err == nil {
		t.Fatal("expected refusal without project")
	}
}

func TestAntigravityExchangeRequiresRefreshToken(t *testing.T) {
	captured := &capturedRequest{}
	tokenSrv := codexTokenServer(t, captured, map[string]any{"access_token": "at"})
	defer tokenSrv.Close()
	cfg := AntigravityProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = tokenSrv.URL
	flow, _ := NewAntigravityFlow(cfg, Options{})
	_, err := flow.Exchange(context.Background(), "c", "v", "http://127.0.0.1:0/callback")
	if err == nil || !strings.Contains(err.Error(), "refresh token") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexExchangeTokenEndpointErrorIsSanitized(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"leaked token abc"}`)
	}))
	defer tokenSrv.Close()
	cfg := CodexProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = tokenSrv.URL
	flow, _ := NewCodexFlow(cfg, Options{})
	_, err := flow.Exchange(context.Background(), "c", "v", "http://localhost:0/auth/callback")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "leaked token") || strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error leaks upstream body: %v", err)
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("error = %v", err)
	}
}

func TestFlowConfigValidationRejectsMissingEndpoints(t *testing.T) {
	if _, err := NewCodexFlow(ProviderConfig{}, Options{}); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := NewAntigravityFlow(ProviderConfig{}, Options{}); err == nil {
		t.Fatal("expected validation error")
	}
	bad := AntigravityProduction
	bad.UserInfoURL = ""
	if _, err := NewAntigravityFlow(bad, Options{}); err == nil {
		t.Fatal("expected missing userinfo error")
	}
}

func TestAntigravityRefreshPreservesProjectOnTransientDiscovery(t *testing.T) {
	onboardCalled := false
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]any{"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 3600})
	}))
	defer tokenSrv.Close()
	projectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":onboard") {
			onboardCalled = true
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer projectSrv.Close()
	cfg := AntigravityProduction
	cfg.TokenURL = tokenSrv.URL
	cfg.UserInfoURL = tokenSrv.URL
	cfg.ProjectAPI = projectSrv.URL
	cfg.OnboardAPI = projectSrv.URL
	flow, err := NewAntigravityFlow(cfg, Options{Now: testNow})
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	rf, ok := flow.(RefreshFlow)
	if !ok {
		t.Fatalf("antigravity flow does not implement RefreshFlow")
	}
	cred, err := rf.Refresh(context.Background(), account.Credential{
		Refresh: "rt-1", AccountID: "g-42", ProjectID: "prior-project",
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if cred.ProjectID != "prior-project" {
		t.Fatalf("project = %q, want prior-project kept on transient discovery failure", cred.ProjectID)
	}
	if onboardCalled {
		t.Fatalf("onboard must not run when discovery is transiently unavailable")
	}
}

func TestAntigravityRefreshAdoptsNewlyDiscoveredProject(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":loadCodeAssist") {
			jsonResponse(w, map[string]any{"cloudaicompanionProject": "proj-new"})
			return
		}
		jsonResponse(w, map[string]any{"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 3600})
	}))
	defer tokenSrv.Close()
	userinfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]any{"email": "Dev@Example.com", "id": "g-42"})
	}))
	defer userinfoSrv.Close()
	cfg := AntigravityProduction
	cfg.TokenURL = tokenSrv.URL
	cfg.UserInfoURL = userinfoSrv.URL
	cfg.ProjectAPI = tokenSrv.URL
	cfg.OnboardAPI = tokenSrv.URL
	flow, err := NewAntigravityFlow(cfg, Options{Now: testNow})
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	rf, ok := flow.(RefreshFlow)
	if !ok {
		t.Fatalf("antigravity flow does not implement RefreshFlow")
	}
	cred, err := rf.Refresh(context.Background(), account.Credential{
		Refresh: "rt-1", AccountID: "g-42", ProjectID: "prior-project",
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if cred.ProjectID != "proj-new" {
		t.Fatalf("project = %q, want proj-new adopted when discovery succeeds", cred.ProjectID)
	}
}
