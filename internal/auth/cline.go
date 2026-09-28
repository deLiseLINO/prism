package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"prism/internal/account"
	"prism/internal/providers/cline"
)

const clineRefreshSkew = 2 * time.Minute

var clineDefaultBaseURL = cline.DefaultBaseURL

type clineFlow struct {
	http    *http.Client
	now     func() time.Time
	baseURL string
}

func NewClineFlow(opts Options) *clineFlow {
	o := opts.withDefaults()
	return &clineFlow{http: o.HTTP, now: o.Now, baseURL: clineDefaultBaseURL}
}

var (
	clineWorkOSClientID = "client_01K3A541FN8TA3EPPHTD2325AR"
	workOSAPIBase       = "https://api.workos.com"
)

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

func (f *clineFlow) Config() ProviderConfig { return ProviderConfig{} }

func (f *clineFlow) AuthURL(state, verifier, redirectURI string) string { return "" }

func (f *clineFlow) Exchange(ctx context.Context, code, verifier, redirectURI string) (account.Credential, error) {
	return account.Credential{}, fmt.Errorf("cline login uses the device-code flow")
}

func (f *clineFlow) BeginDevice(ctx context.Context) (deviceAuth, error) {
	body := url.Values{"client_id": {clineWorkOSClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workOSAPIBase+"/user_management/authorize/device", strings.NewReader(body.Encode()))
	if err != nil {
		return deviceAuth{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.http.Do(req)
	if err != nil {
		return deviceAuth{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return deviceAuth{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return deviceAuth{}, fmt.Errorf("cline device authorization returned status %d", resp.StatusCode)
	}
	var out struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.DeviceCode == "" || out.UserCode == "" || out.VerificationURI == "" {
		return deviceAuth{}, fmt.Errorf("cline device authorization returned an unreadable response")
	}
	uri := out.VerificationURIComplete
	if uri == "" {
		uri = out.VerificationURI
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 300
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return deviceAuth{
		DeviceCode: out.DeviceCode,
		UserCode:   out.UserCode,
		URL:        uri,
		ExpiresAt:  f.now().Add(time.Duration(out.ExpiresIn) * time.Second),
		Interval:   time.Duration(out.Interval) * time.Second,
	}, nil
}

type deviceAuth struct {
	DeviceCode string
	UserCode   string
	URL        string
	ExpiresAt  time.Time
	Interval   time.Duration
}

func (f *clineFlow) ExchangeDevice(ctx context.Context, started deviceAuth) (account.Credential, error) {
	deadline := started.ExpiresAt
	if deadline.IsZero() {
		deadline = f.now().Add(5 * time.Minute)
	}
	interval := started.Interval
	for {
		if err := ctx.Err(); err != nil {
			return account.Credential{}, err
		}
		if f.now().After(deadline) {
			return account.Credential{}, fmt.Errorf("cline device authorization expired")
		}
		tokens, wait, err := f.pollDevice(ctx, started.DeviceCode)
		if err != nil {
			return account.Credential{}, err
		}
		if wait {
			if err := sleep(ctx, interval); err != nil {
				return account.Credential{}, err
			}
			continue
		}
		return f.registerDevice(ctx, tokens)
	}
}

type workosTokens struct {
	Access  string
	Refresh string
}

func (f *clineFlow) pollDevice(ctx context.Context, deviceCode string) (workosTokens, bool, error) {
	body := url.Values{
		"grant_type":  {deviceGrantType},
		"device_code": {deviceCode},
		"client_id":   {clineWorkOSClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workOSAPIBase+"/user_management/authenticate", strings.NewReader(body.Encode()))
	if err != nil {
		return workosTokens{}, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.http.Do(req)
	if err != nil {
		return workosTokens{}, false, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return workosTokens{}, false, err
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return workosTokens{}, false, fmt.Errorf("cline device poll returned invalid JSON")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out.AccessToken == "" || out.RefreshToken == "" {
			return workosTokens{}, false, fmt.Errorf("cline device poll returned no tokens")
		}
		return workosTokens{Access: out.AccessToken, Refresh: out.RefreshToken}, false, nil
	}
	switch out.Error {
	case "authorization_pending", "slow_down":
		return workosTokens{}, true, nil
	case "":
		return workosTokens{}, false, fmt.Errorf("cline device poll returned status %d", resp.StatusCode)
	default:
		return workosTokens{}, false, fmt.Errorf("cline device authorization %s", out.Error)
	}
}

func (f *clineFlow) registerDevice(ctx context.Context, tokens workosTokens) (account.Credential, error) {
	body, err := json.Marshal(map[string]string{
		"accessToken":  tokens.Access,
		"refreshToken": tokens.Refresh,
	})
	if err != nil {
		return account.Credential{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.baseURL+"/api/v1/auth/register", strings.NewReader(string(body)))
	if err != nil {
		return account.Credential{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range cline.ProductHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return account.Credential{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return account.Credential{}, fmt.Errorf("cline token registration returned status %d", resp.StatusCode)
	}
	var out clineRefreshResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return account.Credential{}, fmt.Errorf("cline token registration returned invalid JSON")
	}
	if !out.Success || out.Data.AccessToken == "" || out.Data.RefreshToken == "" {
		return account.Credential{}, fmt.Errorf("cline token registration returned no tokens")
	}
	expiresAt, err := parseClineExpiry(out.Data.ExpiresAt)
	if err != nil {
		return account.Credential{}, fmt.Errorf("cline token registration returned an unreadable expiry")
	}
	return account.Credential{
		Access:    cline.EnsureWorkosPrefix(out.Data.AccessToken),
		Refresh:   out.Data.RefreshToken,
		ExpiresAt: expiresAt,
		AccountID: clineUserID(out.Data.UserInfo),
		Email:     clineEmail(out.Data.UserInfo),
	}, nil
}

func parseClineExpiry(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, fmt.Errorf("unreadable expiry")
	}
	var ms float64
	if err := json.Unmarshal(raw, &ms); err == nil {
		return time.UnixMilli(int64(ms)), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s == "" {
		return time.Time{}, fmt.Errorf("unreadable expiry")
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("unreadable expiry")
	}
	return t, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (f *clineFlow) RefreshSkew() time.Duration { return clineRefreshSkew }

type clineRefreshResponse struct {
	Success bool `json:"success"`
	Data    struct {
		AccessToken  string          `json:"accessToken"`
		RefreshToken string          `json:"refreshToken"`
		ExpiresAt    json.RawMessage `json:"expiresAt"`
		UserInfo     json.RawMessage `json:"userInfo"`
	} `json:"data"`
}

func (f *clineFlow) Refresh(ctx context.Context, prev account.Credential) (account.Credential, error) {
	if prev.Refresh == "" {
		return account.Credential{}, fmt.Errorf("cline credential has no refresh grant; re-login with the cline CLI")
	}
	body, err := json.Marshal(map[string]string{
		"refreshToken": prev.Refresh,
		"grantType":    "refresh_token",
	})
	if err != nil {
		return account.Credential{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, clineDefaultBaseURL+"/api/v1/auth/refresh", strings.NewReader(string(body)))
	if err != nil {
		return account.Credential{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range cline.ProductHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return account.Credential{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return account.Credential{}, fmt.Errorf("cline token refresh returned status %d", resp.StatusCode)
	}
	var out clineRefreshResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return account.Credential{}, fmt.Errorf("cline token refresh returned invalid JSON")
	}
	if !out.Success || out.Data.AccessToken == "" {
		return account.Credential{}, fmt.Errorf("cline token refresh returned no access token")
	}
	expiresAt, err := parseClineExpiry(out.Data.ExpiresAt)
	if err != nil {
		return account.Credential{}, fmt.Errorf("cline token refresh returned an unreadable expiry")
	}
	refresh := out.Data.RefreshToken
	if refresh == "" {
		refresh = prev.Refresh
	}
	next := account.Credential{
		Access:    cline.EnsureWorkosPrefix(out.Data.AccessToken),
		Refresh:   refresh,
		ExpiresAt: expiresAt,
		AccountID: clineUserID(out.Data.UserInfo),
		Email:     prev.Email,
	}
	if next.AccountID == "" {
		next.AccountID = prev.AccountID
	}
	return next, nil
}

func clineUserID(raw json.RawMessage) string {
	var info struct {
		ClineUserID string `json:"clineUserId"`
	}
	if json.Unmarshal(raw, &info) != nil {
		return ""
	}
	return info.ClineUserID
}

func clineEmail(raw json.RawMessage) string {
	var info struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(raw, &info) != nil {
		return ""
	}
	return info.Email
}
