package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"prism/internal/integrations"
	"prism/internal/management"
)

type Client interface {
	Usage(ctx context.Context) (management.UsageResponse, error)
	Accounts(ctx context.Context) (management.AccountsResponse, error)
	AccountQuota(ctx context.Context, id string) (management.QuotaResponse, error)
	PauseAccount(ctx context.Context, id string, version uint64) (management.Account, error)
	ResumeAccount(ctx context.Context, id string, version uint64) (management.Account, error)
	DeleteAccount(ctx context.Context, id string) error
	AuthStart(ctx context.Context, provider string) (management.AuthStartResponse, error)
	AuthStatus(ctx context.Context, provider, session string) (management.AuthStatusResponse, error)
	IntegrationsList(ctx context.Context) ([]integrations.Status, error)
	IntegrationApply(ctx context.Context, id string) (integrations.ApplyResult, error)
	ProvidersList(ctx context.Context) (management.ProvidersResponse, error)
	ProvidersReplace(ctx context.Context, id string, w management.ProviderWrite) (management.ProviderMutationResponse, error)
	Stats(ctx context.Context, statsRange string) (management.StatsResponse, error)
}

type HTTPClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewHTTPClient(baseURL, token string, transport http.RoundTripper) *HTTPClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if transport != nil {
		client.Transport = transport
	}
	return &HTTPClient{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: client}
}

func (c *HTTPClient) Usage(ctx context.Context) (management.UsageResponse, error) {
	var out management.UsageResponse
	err := c.get(ctx, "/api/v1/usage", &out)
	return out, err
}

func (c *HTTPClient) Accounts(ctx context.Context) (management.AccountsResponse, error) {
	var out management.AccountsResponse
	err := c.get(ctx, "/api/v1/accounts", &out)
	return out, err
}

func (c *HTTPClient) AccountQuota(ctx context.Context, id string) (management.QuotaResponse, error) {
	var out management.QuotaResponse
	err := c.get(ctx, "/api/v1/accounts/"+urlPathEscape(id)+"/quota", &out)
	return out, err
}

func (c *HTTPClient) PauseAccount(ctx context.Context, id string, version uint64) (management.Account, error) {
	return c.accountMutation(ctx, id, "/pause", version)
}

func (c *HTTPClient) ResumeAccount(ctx context.Context, id string, version uint64) (management.Account, error) {
	return c.accountMutation(ctx, id, "/resume", version)
}

func (c *HTTPClient) accountMutation(ctx context.Context, id, action string, version uint64) (management.Account, error) {
	var out management.Account
	body, err := json.Marshal(management.VersionWrite{Version: version})
	if err != nil {
		return out, err
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/accounts/"+urlPathEscape(id)+action, body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *HTTPClient) DeleteAccount(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/accounts/"+urlPathEscape(id), nil, nil)
}

func (c *HTTPClient) AuthStart(ctx context.Context, provider string) (management.AuthStartResponse, error) {
	var out management.AuthStartResponse
	err := c.do(ctx, http.MethodPost, "/api/v1/auth/"+urlPathEscape(provider)+"/start", nil, &out)
	return out, err
}

func (c *HTTPClient) AuthStatus(ctx context.Context, provider, session string) (management.AuthStatusResponse, error) {
	var out management.AuthStatusResponse
	err := c.get(ctx, "/api/v1/auth/"+urlPathEscape(provider)+"/status?session="+urlQueryEscape(session), &out)
	return out, err
}

func (c *HTTPClient) IntegrationsList(ctx context.Context) ([]integrations.Status, error) {
	var out management.IntegrationsResponse
	if err := c.get(ctx, "/api/v1/integrations", &out); err != nil {
		return nil, err
	}
	return out.Integrations, nil
}

func (c *HTTPClient) IntegrationApply(ctx context.Context, id string) (integrations.ApplyResult, error) {
	var out integrations.ApplyResult
	err := c.do(ctx, http.MethodPost, "/api/v1/integrations/"+urlPathEscape(id)+"/apply", nil, &out)
	return out, err
}

func (c *HTTPClient) ProvidersList(ctx context.Context) (management.ProvidersResponse, error) {
	var out management.ProvidersResponse
	err := c.get(ctx, "/api/v1/providers", &out)
	return out, err
}

func (c *HTTPClient) Stats(ctx context.Context, statsRange string) (management.StatsResponse, error) {
	var out management.StatsResponse
	err := c.get(ctx, "/api/v1/stats?range="+urlQueryEscape(statsRange), &out)
	return out, err
}

func (c *HTTPClient) ProvidersReplace(ctx context.Context, id string, w management.ProviderWrite) (management.ProviderMutationResponse, error) {
	var out management.ProviderMutationResponse
	body, err := json.Marshal(w)
	if err != nil {
		return out, err
	}
	if err := c.do(ctx, http.MethodPut, "/api/v1/providers/"+urlPathEscape(id), body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *HTTPClient) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *HTTPClient) do(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || out == nil {
		if resp.StatusCode >= 400 {
			return errorFromBody(resp)
		}
		return nil
	}
	if resp.StatusCode >= 400 {
		return errorFromBody(resp)
	}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	return nil
}

func errorFromBody(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var envelope management.ErrorBody
	if err := json.Unmarshal(bytes.TrimSpace(body), &envelope); err == nil && envelope.Error.Message != "" {
		return fmt.Errorf("request failed with status %d: %s", resp.StatusCode, envelope.Error.Message)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Errorf("request failed with status %d", resp.StatusCode)
	}
	return fmt.Errorf("request failed with status %d: %s", resp.StatusCode, text)
}

func urlPathEscape(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

func urlQueryEscape(value string) string {
	replacer := strings.NewReplacer(" ", "%20", "&", "%26", "=", "%3D", "+", "%2B", "?", "%3F", "#", "%23")
	return replacer.Replace(value)
}
