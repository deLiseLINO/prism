package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/account"
)

type memSink struct {
	mu         sync.Mutex
	persistErr error
	creds      map[string]account.Credential
	gens       map[string]account.CredentialGeneration
	registered []account.Account
}

func newMemSink() *memSink {
	return &memSink{
		creds: map[string]account.Credential{},
		gens:  map[string]account.CredentialGeneration{},
	}
}

func (s *memSink) Persist(_ context.Context, provider account.ProviderID, cred account.Credential) (account.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistErr != nil {
		return account.Account{}, s.persistErr
	}
	if cred.AccountID == "" {
		return account.Account{}, fmt.Errorf("no identity")
	}
	id := string(provider) + ":" + cred.AccountID
	s.gens[id]++
	gen := s.gens[id]
	s.creds[id] = cred
	return account.Account{ID: account.AccountID(id), Provider: provider, State: account.Active, CredGen: gen, Version: 1}, nil
}

func (s *memSink) Register(a account.Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered = append(s.registered, a)
}

func (s *memSink) Registered(provider account.ProviderID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.registered {
		if a.Provider == provider {
			return true
		}
	}
	return false
}

func (s *memSink) rowIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.creds))
	for id := range s.creds {
		out = append(out, id)
	}
	return out
}

type serviceHarness struct {
	svc      *Service
	sink     *memSink
	token    *capturedRequest
	tokenSrv *httptest.Server
	now      time.Time
}

func newHarness(t *testing.T, mutate func(cfg *ProviderConfig), opts Options) *serviceHarness {
	t.Helper()
	h := &serviceHarness{sink: newMemSink(), token: &capturedRequest{}, now: testNow()}
	h.tokenSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := r.PostFormValue("code")
		form := urlValues{}
		for k, v := range r.PostForm {
			form[k] = v[0]
		}
		h.token.add(form)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  makeJWT(map[string]any{"chatgpt_account_id": "acc-" + code, "email": "user@example.com"}),
			"refresh_token": "rt",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(h.tokenSrv.Close)
	cfg := CodexProduction
	cfg.CallbackPort = 0
	cfg.TokenURL = h.tokenSrv.URL
	if mutate != nil {
		mutate(&cfg)
	}
	flow, err := NewCodexFlow(cfg, Options{Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return h.now }
	}
	h.svc, err = New(h.sink, map[account.ProviderID]Flow{"codex": flow}, opts)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	t.Cleanup(func() { h.svc.Close(context.Background()) })
	return h
}

func (h *serviceHarness) start(t *testing.T) AuthStart {
	t.Helper()
	start, err := h.svc.Start(context.Background(), "codex")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return start
}

func (h *serviceHarness) stateFromURL(t *testing.T, rawURL string) string {
	t.Helper()
	marker := "state="
	idx := strings.Index(rawURL, marker)
	if idx < 0 {
		t.Fatalf("no state in %q", rawURL)
	}
	rest := rawURL[idx+len(marker):]
	if end := strings.IndexByte(rest, '&'); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestStartReturnsSessionAndReferenceShapedURL(t *testing.T) {
	h := newHarness(t, nil, Options{})
	start := h.start(t)
	if start.Session == "" {
		t.Fatal("empty session id")
	}
	u := start.URL
	if !strings.HasPrefix(u, "https://auth.openai.com/oauth/authorize?") {
		t.Fatalf("auth url = %q", u)
	}
	for _, want := range []string{
		"response_type=code",
		"client_id=app_EMoamEEZ73f0CkXaXp7hrann",
		"scope=openid+profile+email+offline_access+api.connectors.read+api.connectors.invoke",
		"code_challenge_method=S256",
		"codex_cli_simplified_flow=true",
		"originator=prism",
		"id_token_add_organizations=true",
		"redirect_uri=http%3A%2F%2Flocalhost%3A",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("auth url %q missing %q", u, want)
		}
	}
	if strings.Contains(u, "code_verifier=") {
		t.Fatalf("verifier leaked into auth url: %q", u)
	}
}

func TestStartUnknownProviderRefused(t *testing.T) {
	h := newHarness(t, nil, Options{})
	if _, err := h.svc.Start(context.Background(), "nope"); err != ErrUnknownProvider {
		t.Fatalf("err = %v", err)
	}
}

func TestCallbackStateAndSessionValidation(t *testing.T) {
	h := newHarness(t, nil, Options{})
	start := h.start(t)
	state := h.stateFromURL(t, start.URL)

	cases := []struct {
		name    string
		session AuthSessionID
		code    string
		state   string
		want    error
	}{
		{"wrong state", start.Session, "code", "bogus", ErrStateMismatch},
		{"missing state", start.Session, "code", "", ErrStateMismatch},
		{"missing code", start.Session, "", state, ErrInvalidCallback},
		{"unknown session", "no-such-session", "code", state, ErrUnknownSession},
	}
	for _, tc := range cases {
		err := h.svc.Complete(context.Background(), "codex", AuthCallback{Session: tc.session, Code: tc.code, State: tc.state})
		if err != tc.want {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if h.token.count() != 0 {
		t.Fatalf("token endpoint hit %d times on invalid callbacks", h.token.count())
	}
	if len(h.sink.rowIDs()) != 0 {
		t.Fatalf("sink rows = %v", h.sink.rowIDs())
	}
}

func TestCrossSessionCallbackRefused(t *testing.T) {
	h := newHarness(t, nil, Options{})
	s1 := h.start(t)
	s2 := h.start(t)
	state1 := h.stateFromURL(t, s1.URL)
	state2 := h.stateFromURL(t, s2.URL)
	if state1 == state2 {
		t.Fatal("states must differ across sessions")
	}
	if err := h.svc.Complete(context.Background(), "codex", AuthCallback{Session: s2.Session, Code: "code", State: state1}); err != ErrStateMismatch {
		t.Fatalf("cross-session state err = %v", err)
	}
	if err := h.svc.Complete(context.Background(), "codex", AuthCallback{Session: s2.Session, Code: "code", State: state2}); err != nil {
		t.Fatalf("own callback err = %v", err)
	}
	if h.token.count() != 1 {
		t.Fatalf("token calls = %d", h.token.count())
	}
}

func TestExpiredSessionRefused(t *testing.T) {
	h := newHarness(t, nil, Options{})
	start := h.start(t)
	state := h.stateFromURL(t, start.URL)
	h.now = h.now.Add(20 * time.Minute)
	err := h.svc.Complete(context.Background(), "codex", AuthCallback{Session: start.Session, Code: "code", State: state})
	if err != ErrSessionExpired {
		t.Fatalf("err = %v", err)
	}
	if h.token.count() != 0 {
		t.Fatalf("expired session reached token endpoint %d times", h.token.count())
	}
}

func TestConcurrentSessionsCompleteIndependently(t *testing.T) {
	h := newHarness(t, nil, Options{})
	var starts []AuthStart
	for i := 0; i < 4; i++ {
		starts = append(starts, h.start(t))
	}
	var wg sync.WaitGroup
	errs := make([]error, len(starts))
	for i, s := range starts {
		wg.Add(1)
		go func(i int, s AuthStart) {
			defer wg.Done()
			errs[i] = h.svc.Complete(context.Background(), "codex", AuthCallback{
				Session: s.Session,
				Code:    fmt.Sprintf("code-%d", i),
				State:   h.stateFromURL(t, s.URL),
			})
		}(i, s)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	if h.token.count() != len(starts) {
		t.Fatalf("token calls = %d", h.token.count())
	}
	if got := len(h.sink.rowIDs()); got != len(starts) {
		t.Fatalf("sink rows = %d", got)
	}
	for _, s := range starts {
		st, err := h.svc.Status(context.Background(), "codex", s.Session)
		if err != nil || st.State != StatusComplete {
			t.Fatalf("status = %+v err = %v", st, err)
		}
	}
}

func TestLoopbackAutomaticCallbackCompletes(t *testing.T) {
	h := newHarness(t, nil, Options{})
	start := h.start(t)
	state := h.stateFromURL(t, start.URL)

	redirectMarker := "redirect_uri=http%3A%2F%2Flocalhost%3A"
	idx := strings.Index(start.URL, redirectMarker)
	if idx < 0 {
		t.Fatalf("no redirect uri in %q", start.URL)
	}
	rest := start.URL[idx+len(redirectMarker):]
	port := rest[:strings.IndexByte(rest, '%')]
	callbackURL := fmt.Sprintf("http://127.0.0.1:%s/auth/callback?code=auto-code&state=%s", port, state)
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("loopback get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("loopback status = %d body = %s", resp.StatusCode, body)
	}
	if h.token.count() != 1 {
		t.Fatalf("token calls = %d", h.token.count())
	}
	if got := h.sink.rowIDs(); len(got) != 1 || got[0] != "codex:acc-auto-code" {
		t.Fatalf("rows = %v", got)
	}
	if !h.sink.Registered("codex") {
		t.Fatal("pool registration missing")
	}
	st, err := h.svc.Status(context.Background(), "codex", start.Session)
	if err != nil || st.State != StatusComplete {
		t.Fatalf("status = %+v err = %v", st, err)
	}
}

func TestLoopbackRejectsUnknownState(t *testing.T) {
	h := newHarness(t, nil, Options{})
	h.start(t)
	resp, err := http.Get("http://127.0.0.1:" + h.loopbackPort(t) + "/auth/callback?code=x&state=wrong")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if h.token.count() != 0 {
		t.Fatal("token endpoint hit for unknown state")
	}
}

func (h *serviceHarness) loopbackPort(t *testing.T) string {
	t.Helper()
	h.svc.mu.Lock()
	defer h.svc.mu.Unlock()
	lb, ok := h.svc.loopbacks["codex"]
	if !ok {
		t.Fatal("no loopback listener")
	}
	uri := lb.redirectURI
	port := uri[strings.LastIndex(uri, ":")+1:]
	if end := strings.IndexByte(port, '/'); end >= 0 {
		port = port[:end]
	}
	return port
}

func TestProviderLevelStatus(t *testing.T) {
	h := newHarness(t, nil, Options{})
	st, err := h.svc.Status(context.Background(), "codex", "")
	if err != nil || st.State != StatusUnauthorized {
		t.Fatalf("status = %+v err = %v", st, err)
	}
	start := h.start(t)
	st, err = h.svc.Status(context.Background(), "codex", start.Session)
	if err != nil || st.State != StatusPending {
		t.Fatalf("status = %+v err = %v", st, err)
	}
	if err := h.svc.Complete(context.Background(), "codex", AuthCallback{
		Session: start.Session,
		Code:    "code",
		State:   h.stateFromURL(t, start.URL),
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	st, err = h.svc.Status(context.Background(), "codex", "")
	if err != nil || st.State != StatusAuthorized {
		t.Fatalf("status = %+v err = %v", st, err)
	}
	if _, err := h.svc.Status(context.Background(), "codex", "ghost"); err != ErrUnknownSession {
		t.Fatalf("ghost session err = %v", err)
	}
	if _, err := h.svc.Status(context.Background(), "nope", ""); err != ErrUnknownProvider {
		t.Fatalf("unknown provider err = %v", err)
	}
}

func TestConsumedSessionCannotBeReplayed(t *testing.T) {
	h := newHarness(t, nil, Options{})
	start := h.start(t)
	state := h.stateFromURL(t, start.URL)
	cb := AuthCallback{Session: start.Session, Code: "code", State: state}
	if err := h.svc.Complete(context.Background(), "codex", cb); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	if err := h.svc.Complete(context.Background(), "codex", cb); err != ErrUnknownSession {
		t.Fatalf("replay err = %v", err)
	}
	if h.token.count() != 1 {
		t.Fatalf("token calls = %d", h.token.count())
	}
}

func TestSinkFailureMarksSessionFailedWithoutRegistration(t *testing.T) {
	h := newHarness(t, nil, Options{})
	h.sink.persistErr = fmt.Errorf("disk on fire")
	start := h.start(t)
	state := h.stateFromURL(t, start.URL)
	err := h.svc.Complete(context.Background(), "codex", AuthCallback{Session: start.Session, Code: "code", State: state})
	if err == nil {
		t.Fatal("expected persistence error")
	}
	if h.token.count() != 1 {
		t.Fatalf("token calls = %d", h.token.count())
	}
	if len(h.sink.registered) != 0 {
		t.Fatal("pool registered despite sink failure")
	}
	st, err2 := h.svc.Status(context.Background(), "codex", start.Session)
	if err2 != nil || st.State != StatusFailed {
		t.Fatalf("status = %+v err = %v", st, err2)
	}
}

func TestCloseStopsLoopbackListeners(t *testing.T) {
	h := newHarness(t, nil, Options{})
	h.start(t)
	port := h.loopbackPort(t)
	if err := h.svc.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	resp, err := http.Get("http://127.0.0.1:" + port + "/auth/callback?code=x&state=y")
	if err == nil {
		resp.Body.Close()
		t.Fatal("listener still accepting connections after Close")
	}
}

func TestTokenResponseCredentialsPersisted(t *testing.T) {
	h := newHarness(t, nil, Options{})
	start := h.start(t)
	if err := h.svc.Complete(context.Background(), "codex", AuthCallback{
		Session: start.Session,
		Code:    "code",
		State:   h.stateFromURL(t, start.URL),
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	cred := h.sink.creds["codex:acc-code"]
	if cred.Email != "user@example.com" || cred.AccountID != "acc-code" || cred.Refresh != "rt" {
		t.Fatalf("cred = %+v", cred)
	}
	if !cred.ExpiresAt.Equal(testNow().Add(3600 * time.Second)) {
		t.Fatalf("expires = %v", cred.ExpiresAt)
	}
	if h.sink.gens["codex:acc-code"] != 1 {
		t.Fatalf("gen = %d", h.sink.gens["codex:acc-code"])
	}
}

func TestStatusResponseJSONHasNoSecretFields(t *testing.T) {
	h := newHarness(t, nil, Options{})
	start := h.start(t)
	st, err := h.svc.Status(context.Background(), "codex", start.Session)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "verifier") || strings.Contains(string(raw), "token") {
		t.Fatalf("status json = %s", raw)
	}
}
