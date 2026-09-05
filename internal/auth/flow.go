package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"prism/internal/account"
)

const (
	tokenExpiryFallback   = 3600 * time.Second
	refreshSkew           = 5 * time.Minute
	onboardAttempts       = 5
	defaultOnboardPoll    = 2 * time.Second
	antigravityIDEVersion = "2.5.5"
)

type flowBase struct {
	cfg  ProviderConfig
	http *http.Client
	now  func() time.Time
}

func newFlowBase(cfg ProviderConfig, opts Options, needGoogleAPI bool) (flowBase, error) {
	if err := cfg.validate(needGoogleAPI); err != nil {
		return flowBase{}, err
	}
	o := opts.withDefaults()
	return flowBase{cfg: cfg, http: o.HTTP, now: o.Now}, nil
}

// TokenError reports a rejected token-endpoint call. The upstream error code
// is kept for classification only; the message carries the HTTP status and
// never the response body, which can echo token material.
type TokenError struct {
	Status int
	Code   string
}

func (e *TokenError) Error() string {
	return fmt.Sprintf("token endpoint returned status %d", e.Status)
}

const codeInvalidGrant = "invalid_grant"

type tokenResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    *float64 `json:"expires_in"`
	IDToken      string   `json:"id_token"`
}

func (f flowBase) postToken(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.http.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		var structured struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &structured)
		return tokenResponse{}, &TokenError{Status: resp.StatusCode, Code: structured.Error}
	}
	var out tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return tokenResponse{}, fmt.Errorf("token endpoint returned invalid JSON")
	}
	return out, nil
}

func (f flowBase) expiresAt(resp tokenResponse) time.Time {
	seconds := tokenExpiryFallback
	if resp.ExpiresIn != nil && *resp.ExpiresIn >= 0 {
		seconds = time.Duration(*resp.ExpiresIn * float64(time.Second))
	}
	return f.now().Add(seconds)
}

func (t tokenResponse) expiresIn() float64 {
	if t.ExpiresIn != nil {
		return *t.ExpiresIn
	}
	return tokenExpiryFallback.Seconds()
}

// jwtPayload decodes the unverified claims of a JWT. It is used only to read
// identity claims bound to a token that the provider just issued against our
// authorization code, never to authenticate a caller.
func jwtPayload(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func stringClaim(payload map[string]any, key string) (string, bool) {
	v, ok := payload[key].(string)
	return v, ok && v != ""
}

// RefreshFlow is implemented by provider flows able to renew an expiring
// credential from its stored refresh grant. Implementations must never log
// token material and must keep error messages free of response bodies.
type RefreshFlow interface {
	Refresh(ctx context.Context, prev account.Credential) (account.Credential, error)
	RefreshSkew() time.Duration
}

// codexRefreshSkew widens the expiry check: a Codex credential expiring
// within one minute is refreshed rather than dispatched.
const codexRefreshSkew = time.Minute

type codexFlow struct {
	flowBase
}

func NewCodexFlow(cfg ProviderConfig, opts Options) (Flow, error) {
	base, err := newFlowBase(cfg, opts, false)
	if err != nil {
		return nil, err
	}
	return &codexFlow{flowBase: base}, nil
}

func (f *codexFlow) Config() ProviderConfig { return f.cfg }

func (f *codexFlow) AuthURL(state, verifier, redirectURI string) string {
	params := baseParams(f.cfg, state, verifier, redirectURI)
	params = append(params, f.cfg.ExtraParams...)
	return f.cfg.AuthURL + "?" + encodeParams(params)
}

func (f *codexFlow) Exchange(ctx context.Context, code, verifier, redirectURI string) (account.Credential, error) {
	resp, err := f.postToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {f.cfg.ClientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
	if err != nil {
		return account.Credential{}, err
	}
	if resp.AccessToken == "" {
		return account.Credential{}, fmt.Errorf("token response did not include an access token")
	}
	id, email, ok := codexIdentity(resp.IDToken, resp.AccessToken)
	if !ok {
		return account.Credential{}, fmt.Errorf("token claims did not include a codex account identity")
	}
	return account.Credential{
		Access:    resp.AccessToken,
		Refresh:   resp.RefreshToken,
		ExpiresAt: f.expiresAt(resp),
		AccountID: id,
		Email:     email,
	}, nil
}

func (f *codexFlow) RefreshSkew() time.Duration { return codexRefreshSkew }

// Refresh renews the credential from the stored grant. A response that omits
// a refresh token keeps the previous grant; a readable identity token that
// names a different account fails closed.
func (f *codexFlow) Refresh(ctx context.Context, prev account.Credential) (account.Credential, error) {
	if prev.Refresh == "" {
		return account.Credential{}, fmt.Errorf("codex credential has no refresh grant")
	}
	resp, err := f.postToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {f.cfg.ClientID},
		"refresh_token": {prev.Refresh},
	})
	if err != nil {
		return account.Credential{}, err
	}
	if resp.AccessToken == "" {
		return account.Credential{}, fmt.Errorf("token response did not include an access token")
	}
	id := prev.AccountID
	if resp.IDToken != "" {
		payload, ok := jwtPayload(resp.IDToken)
		if !ok {
			return account.Credential{}, fmt.Errorf("refresh response included an unreadable identity token")
		}
		claimed := codexAccountID(payload)
		if claimed == "" {
			return account.Credential{}, fmt.Errorf("refresh identity token did not include a codex account identity")
		}
		if id != "" && claimed != id {
			return account.Credential{}, fmt.Errorf("refresh response identifies a different codex account")
		}
		id = claimed
	}
	refresh := resp.RefreshToken
	if refresh == "" {
		refresh = prev.Refresh
	}
	return account.Credential{
		Access:    resp.AccessToken,
		Refresh:   refresh,
		ExpiresAt: f.expiresAt(resp),
		AccountID: id,
		Email:     prev.Email,
	}, nil
}

func codexIdentity(idToken, accessToken string) (string, string, bool) {
	var id, email string
	for _, token := range []string{idToken, accessToken} {
		if token == "" {
			continue
		}
		payload, ok := jwtPayload(token)
		if !ok {
			continue
		}
		if id == "" {
			id = codexAccountID(payload)
		}
		if email == "" {
			if claim, ok := stringClaim(payload, "email"); ok {
				email = strings.ToLower(claim)
			}
		}
	}
	return id, email, id != "" && email != ""
}

func codexAccountID(payload map[string]any) string {
	if id, ok := stringClaim(payload, "chatgpt_account_id"); ok {
		return id
	}
	if ns, ok := payload["https://api.openai.com/auth"].(map[string]any); ok {
		if id, ok := stringClaim(ns, "chatgpt_account_id"); ok {
			return id
		}
	}
	orgs, ok := payload["organizations"].([]any)
	if ok && len(orgs) > 0 {
		if org, ok := orgs[0].(map[string]any); ok {
			if id, ok := stringClaim(org, "id"); ok {
				return id
			}
		}
	}
	return ""
}

type antigravityFlow struct {
	flowBase
	onboardPoll time.Duration
}

func NewAntigravityFlow(cfg ProviderConfig, opts Options) (Flow, error) {
	base, err := newFlowBase(cfg, opts, true)
	if err != nil {
		return nil, err
	}
	poll := defaultOnboardPoll
	if opts.OnboardPoll > 0 {
		poll = opts.OnboardPoll
	}
	return &antigravityFlow{flowBase: base, onboardPoll: poll}, nil
}

func (f *antigravityFlow) Config() ProviderConfig { return f.cfg }

func (f *antigravityFlow) AuthURL(state, verifier, redirectURI string) string {
	params := baseParams(f.cfg, state, verifier, redirectURI)
	params = append(params, f.cfg.ExtraParams...)
	return f.cfg.AuthURL + "?" + encodeParams(params)
}

func (f *antigravityFlow) Exchange(ctx context.Context, code, verifier, redirectURI string) (account.Credential, error) {
	resp, err := f.postToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {f.cfg.ClientID},
		"client_secret": {f.cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
	if err != nil {
		return account.Credential{}, err
	}
	if resp.AccessToken == "" {
		return account.Credential{}, fmt.Errorf("token response did not include an access token")
	}
	if resp.RefreshToken == "" {
		return account.Credential{}, fmt.Errorf("token response did not include a refresh token")
	}
	id, email, err := f.userinfoIdentity(ctx, resp.AccessToken)
	if err != nil {
		return account.Credential{}, err
	}
	project, err := f.discoverProject(ctx, resp.AccessToken)
	if err != nil {
		return account.Credential{}, err
	}
	if project == "" {
		return account.Credential{}, fmt.Errorf("no Cloud Code Assist project could be discovered for this account")
	}
	return account.Credential{
		Access:    resp.AccessToken,
		Refresh:   resp.RefreshToken,
		ExpiresAt: f.now().Add(time.Duration(resp.expiresIn()*float64(time.Second)) - refreshSkew),
		AccountID: id,
		Email:     email,
		ProjectID: project,
	}, nil
}

func (f *antigravityFlow) RefreshSkew() time.Duration { return refreshSkew }

// Refresh renews the credential from the stored grant and re-runs project
// discovery against the new token. A transient discovery failure keeps the
// previously valid project ID instead of discarding it.
func (f *antigravityFlow) Refresh(ctx context.Context, prev account.Credential) (account.Credential, error) {
	if prev.Refresh == "" {
		return account.Credential{}, fmt.Errorf("antigravity credential has no refresh grant")
	}
	resp, err := f.postToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {f.cfg.ClientID},
		"client_secret": {f.cfg.ClientSecret},
		"refresh_token": {prev.Refresh},
	})
	if err != nil {
		return account.Credential{}, err
	}
	if resp.AccessToken == "" {
		return account.Credential{}, fmt.Errorf("token response did not include an access token")
	}
	refresh := resp.RefreshToken
	if refresh == "" {
		refresh = prev.Refresh
	}
	project, err := f.refreshProject(ctx, resp.AccessToken, prev.ProjectID)
	if err != nil {
		return account.Credential{}, err
	}
	if project == "" {
		return account.Credential{}, fmt.Errorf("refresh could not discover a Cloud Code Assist project for this account")
	}
	return account.Credential{
		Access:    resp.AccessToken,
		Refresh:   refresh,
		ExpiresAt: f.now().Add(time.Duration(resp.expiresIn() * float64(time.Second))),
		AccountID: prev.AccountID,
		Email:     prev.Email,
		ProjectID: project,
	}, nil
}

// refreshProject re-runs project discovery on the new access token but keeps
// the previously valid project whenever discovery fails transiently; a
// definitive empty discovery still falls through to onboarding.
func (f *antigravityFlow) refreshProject(ctx context.Context, accessToken string, previous string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	project, transient, err := f.loadCodeAssist(ctx, accessToken)
	if err != nil {
		if previous != "" {
			return previous, nil
		}
		return "", err
	}
	if transient {
		if previous != "" {
			return previous, nil
		}
		return f.onboardProject(ctx, accessToken)
	}
	if project != "" {
		return project, nil
	}
	onboarded, err := f.onboardProject(ctx, accessToken)
	if err != nil || onboarded == "" {
		if previous != "" {
			return previous, nil
		}
		if err != nil {
			return "", err
		}
		return "", nil
	}
	return onboarded, nil
}

func (f *antigravityFlow) userinfoIdentity(ctx context.Context, accessToken string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.cfg.UserInfoURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := f.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", "", fmt.Errorf("identity endpoint returned status %d", resp.StatusCode)
	}
	var body struct {
		Email string `json:"email"`
		ID    string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("identity endpoint returned invalid JSON")
	}
	if body.Email == "" || body.ID == "" {
		return "", "", fmt.Errorf("identity endpoint did not include account identity")
	}
	return body.ID, strings.ToLower(body.Email), nil
}

func (f *antigravityFlow) discoverProject(ctx context.Context, accessToken string) (string, error) {
	project, transient, _ := f.loadCodeAssist(ctx, accessToken)
	if project != "" {
		return project, nil
	}
	_ = transient
	return f.onboardProject(ctx, accessToken)
}

// loadCodeAssist reports transient=true when the load endpoint failed in a
// way the caller cannot attribute (transport error, non-2xx, unparseable
// body): a transient failure during refresh keeps the previously valid
// project instead of stranding the account.
func (f *antigravityFlow) loadCodeAssist(ctx context.Context, accessToken string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		f.cfg.ProjectAPI+"/"+f.cfg.APIVersion+":loadCodeAssist",
		strings.NewReader(`{"metadata":{"ideType":"ANTIGRAVITY"}}`))
	if err != nil {
		return "", false, err
	}
	f.setGoogleAPIHeaders(req, accessToken)
	resp, err := f.http.Do(req)
	if err != nil {
		return "", true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", true, nil
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", true, nil
	}
	return extractProjectID(body), false, nil
}

func (f *antigravityFlow) onboardProject(ctx context.Context, accessToken string) (string, error) {
	body := fmt.Sprintf(`{"tier_id":"free-tier","metadata":{"ide_type":"ANTIGRAVITY","ide_name":"antigravity","ide_version":%q}}`,
		antigravityIDEVersion)
	for attempt := 0; attempt < onboardAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			f.cfg.OnboardAPI+"/"+f.cfg.APIVersion+":onboardUser",
			strings.NewReader(body))
		if err != nil {
			return "", err
		}
		f.setGoogleAPIHeaders(req, accessToken)
		resp, err := f.http.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(f.onboardPoll):
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return "", nil
		}
		var payload struct {
			Done     bool           `json:"done"`
			Response map[string]any `json:"response"`
		}
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err != nil {
			return "", nil
		}
		if payload.Done {
			return extractProjectID(payload.Response), nil
		}
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(f.onboardPoll):
		}
	}
	return "", nil
}

func (f *antigravityFlow) setGoogleAPIHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", f.cfg.UserAgent)
}

func extractProjectID(data map[string]any) string {
	if data == nil {
		return ""
	}
	for _, key := range []string{"cloudaicompanionProject", "projectId", "project"} {
		v, ok := data[key]
		if !ok {
			continue
		}
		switch value := v.(type) {
		case string:
			if value != "" {
				return value
			}
		case map[string]any:
			if id, ok := value["id"].(string); ok && id != "" {
				return id
			}
		}
	}
	return ""
}
