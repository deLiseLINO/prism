package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"prism/internal/management"
)

const requestTimeout = 10 * time.Second

// apiError is the daemon's typed error body plus the HTTP status it arrived
// with. The CLI maps a handful of codes to specific exit codes and messages;
// everything else is surfaced verbatim with a generic failure exit.
type apiError struct {
	status int
	body   management.ErrorBody
	// rawBody is preserved for malformed bodies so the message is never lost.
	rawBody string
}

func (e *apiError) Error() string {
	msg := strings.TrimSpace(e.body.Error.Message)
	if msg == "" {
		msg = strings.TrimSpace(e.rawBody)
	}
	if msg == "" {
		msg = http.StatusText(e.status)
	}
	return fmt.Sprintf("daemon %d %s: %s", e.status, e.body.Error.Code, msg)
}

// classify maps a daemon error onto a typed exit code. Generation/version
// conflicts (3), refusals (4), unreachable (5), malformed body (6).
func classify(err error) error {
	var ae *apiError
	if !asAPIError(err, &ae) {
		return err
	}
	switch {
	case ae.status == http.StatusConflict:
		return exitErr(exitConflict, "%s (configuration changed since your last read; re-run the command to pick up the new generation)", ae.Error())
	case ae.status >= 400 && ae.status < 500 && ae.body.Error.Code == "refused":
		return exitErr(exitRefused, "%s", ae.Error())
	case ae.status >= 500:
		return exitErr(exitFailure, "%s", ae.Error())
	}
	return exitErr(exitFailure, "%s", ae.Error())
}

func asAPIError(err error, target **apiError) bool {
	if ae, ok := err.(*apiError); ok {
		*target = ae
		return true
	}
	return false
}

// client is the thin HTTP layer over the daemon management API. It owns
// nothing but the base URL; every method returns management schema types so
// request/response shapes are shared with the daemon, never duplicated.
type client struct {
	base string
	hc   *http.Client
}

func newClient(base string) *client {
	return &client{
		base: strings.TrimRight(base, "/"),
		hc:   &http.Client{Timeout: requestTimeout},
	}
}

// newClientWithTransport is the test seam: httptest servers and fake
// transports are injected without touching production construction.
func newClientWithTransport(base string, rt http.RoundTripper) *client {
	return &client{
		base: strings.TrimRight(base, "/"),
		hc:   &http.Client{Timeout: requestTimeout, Transport: rt},
	}
}

func (c *client) call(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return exitErr(exitUnreach, "daemon unreachable at %s: %v", c.base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return exitErr(exitFailure, "read response: %v", err)
	}
	if resp.StatusCode >= 400 {
		ae := &apiError{status: resp.StatusCode}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &ae.body); err != nil {
				ae.rawBody = string(raw)
			}
		}
		return classify(ae)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return exitErr(exitMalformed, "malformed response from daemon (status %d): %v", resp.StatusCode, err)
	}
	return nil
}

// Typed endpoint wrappers. Each returns the shared management DTOs.

func (c *client) health(ctx context.Context) (management.HealthResponse, error) {
	var out management.HealthResponse
	err := c.call(ctx, http.MethodGet, "/api/v1/health", nil, &out)
	return out, err
}

func (c *client) models(ctx context.Context) (management.ModelsResponse, error) {
	var out management.ModelsResponse
	err := c.call(ctx, http.MethodGet, "/api/v1/models", nil, &out)
	return out, err
}

func (c *client) providersList(ctx context.Context) (management.ProvidersResponse, error) {
	var out management.ProvidersResponse
	err := c.call(ctx, http.MethodGet, "/api/v1/providers", nil, &out)
	return out, err
}

func (c *client) providersCreate(ctx context.Context, w management.ProviderWrite) (management.ProviderMutationResponse, error) {
	var out management.ProviderMutationResponse
	err := c.call(ctx, http.MethodPost, "/api/v1/providers", w, &out)
	return out, err
}

func (c *client) providersReplace(ctx context.Context, id string, w management.ProviderWrite) (management.ProviderMutationResponse, error) {
	var out management.ProviderMutationResponse
	err := c.call(ctx, http.MethodPut, "/api/v1/providers/"+escapePath(id), w, &out)
	return out, err
}

func (c *client) providersDelete(ctx context.Context, id string, generation uint64) (management.GenerationResponse, error) {
	var out management.GenerationResponse
	path := "/api/v1/providers/" + escapePath(id) + "?expectedGeneration=" + strconv.FormatUint(generation, 10)
	err := c.call(ctx, http.MethodDelete, path, nil, &out)
	return out, err
}

func (c *client) accountsList(ctx context.Context) (management.AccountsResponse, error) {
	var out management.AccountsResponse
	err := c.call(ctx, http.MethodGet, "/api/v1/accounts", nil, &out)
	return out, err
}

func (c *client) accountAction(ctx context.Context, id, action string, body any, out any) error {
	return c.call(ctx, http.MethodPost, "/api/v1/accounts/"+escapePath(id)+"/"+action, body, out)
}

func (c *client) accountQuota(ctx context.Context, id string) (management.QuotaResponse, error) {
	var out management.QuotaResponse
	err := c.call(ctx, http.MethodGet, "/api/v1/accounts/"+escapePath(id)+"/quota", nil, &out)
	return out, err
}

func (c *client) accountsDelete(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/api/v1/accounts/"+escapePath(id), nil, nil)
}

func (c *client) combosList(ctx context.Context) (management.CombosResponse, error) {
	var out management.CombosResponse
	err := c.call(ctx, http.MethodGet, "/api/v1/combos", nil, &out)
	return out, err
}

func (c *client) combosPut(ctx context.Context, id string, w management.ComboWrite) (management.CombosResponse, error) {
	var out management.CombosResponse
	err := c.call(ctx, http.MethodPut, "/api/v1/combos/"+escapePath(id), w, &out)
	return out, err
}

func (c *client) combosDelete(ctx context.Context, id string, generation uint64) (management.CombosResponse, error) {
	var out management.CombosResponse
	path := "/api/v1/combos/" + escapePath(id) + "?expectedGeneration=" + strconv.FormatUint(generation, 10)
	err := c.call(ctx, http.MethodDelete, path, nil, &out)
	return out, err
}

func (c *client) routesList(ctx context.Context) (management.RoutesResponse, error) {
	var out management.RoutesResponse
	err := c.call(ctx, http.MethodGet, "/api/v1/routes", nil, &out)
	return out, err
}

func (c *client) routesPut(ctx context.Context, key string, w management.RouteWrite) (management.RoutesResponse, error) {
	var out management.RoutesResponse
	err := c.call(ctx, http.MethodPut, "/api/v1/routes/"+escapePath(key), w, &out)
	return out, err
}

func (c *client) routesDelete(ctx context.Context, key string, generation uint64) (management.RoutesResponse, error) {
	var out management.RoutesResponse
	path := "/api/v1/routes/" + escapePath(key) + "?expectedGeneration=" + strconv.FormatUint(generation, 10)
	err := c.call(ctx, http.MethodDelete, path, nil, &out)
	return out, err
}

func (c *client) usage(ctx context.Context) (management.UsageResponse, error) {
	var out management.UsageResponse
	err := c.call(ctx, http.MethodGet, "/api/v1/usage", nil, &out)
	return out, err
}

func (c *client) authStart(ctx context.Context, provider string) (management.AuthStartResponse, error) {
	var out management.AuthStartResponse
	err := c.call(ctx, http.MethodPost, "/api/v1/auth/"+escapePath(provider)+"/start", management.AuthStartResponse{}, &out)
	return out, err
}

func (c *client) authStatus(ctx context.Context, provider, session string) (management.AuthStatusResponse, error) {
	var out management.AuthStatusResponse
	path := "/api/v1/auth/" + escapePath(provider) + "/status"
	if session != "" {
		path += "?session=" + urlQueryEscape(session)
	}
	err := c.call(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *client) integrationsList(ctx context.Context) (management.IntegrationsResponse, error) {
	var out management.IntegrationsResponse
	err := c.call(ctx, http.MethodGet, "/api/v1/integrations", nil, &out)
	return out, err
}

func (c *client) integrationGet(ctx context.Context, clientID string) (integrationsStatusJSON, error) {
	var out integrationsStatusJSON
	err := c.call(ctx, http.MethodGet, "/api/v1/integrations/"+escapePath(clientID), nil, &out)
	return out, err
}

func (c *client) integrationApply(ctx context.Context, clientID string, force bool) (integrationsApplyJSON, error) {
	var out integrationsApplyJSON
	path := "/api/v1/integrations/" + escapePath(clientID) + "/apply"
	if force {
		path += "?force=true"
	}
	err := c.call(ctx, http.MethodPost, path, nil, &out)
	return out, err
}

func (c *client) integrationRollback(ctx context.Context, clientID string) (integrationsApplyJSON, error) {
	var out integrationsApplyJSON
	err := c.call(ctx, http.MethodPost, "/api/v1/integrations/"+escapePath(clientID)+"/rollback", nil, &out)
	return out, err
}

// escapePath percent-encodes path segments so provider/ID/route values with
// slashes or reserved characters cannot escape their segment.
func escapePath(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
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

// urlQueryEscape percent-encodes a query value (spaces become %20).
func urlQueryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == ' ':
			b.WriteString("%20")
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}
