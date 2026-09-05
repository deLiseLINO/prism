package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"prism/internal/quota"
)

const quotaTimeout = 8 * time.Second

const quotaMaxBytes = 512 << 10

// FetchQuota reads the account's live quota windows from the Antigravity
// model-discovery endpoint. The Cloud Code Assist backend requires the IDE
// user agent family and answers only for the credential's project.
func FetchQuota(ctx context.Context, client *http.Client, baseURL string, cred CredentialPair) (QuotaWindows, error) {
	if client == nil {
		return QuotaWindows{}, fmt.Errorf("antigravity: quota fetch needs an http client")
	}
	if cred.AccessToken == "" {
		return QuotaWindows{}, fmt.Errorf("antigravity: quota fetch needs an access token")
	}
	if cred.ProjectID == "" {
		return QuotaWindows{}, fmt.Errorf("antigravity: quota fetch needs a project id")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]string{"project": cred.ProjectID})
	if err != nil {
		return QuotaWindows{}, fmt.Errorf("antigravity: quota request encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, quotaURL(baseURL), bytes.NewReader(body))
	if err != nil {
		return QuotaWindows{}, fmt.Errorf("antigravity: quota request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("User-Agent", RequestUserAgent())
	res, err := client.Do(req)
	if err != nil {
		return QuotaWindows{}, fmt.Errorf("antigravity: quota fetch: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return QuotaWindows{}, fmt.Errorf("antigravity: quota fetch: status %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, quotaMaxBytes))
	if err != nil {
		return QuotaWindows{}, fmt.Errorf("antigravity: quota read: %w", err)
	}
	windows, err := DecodeQuota(raw)
	if err != nil {
		return QuotaWindows{}, fmt.Errorf("antigravity: quota decode: %w", err)
	}
	if windows.Gem == nil && windows.Cla == nil {
		return QuotaWindows{}, fmt.Errorf("antigravity: quota payload carries no quota window")
	}
	return windows, nil
}

func quotaURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/v1internal:fetchAvailableModels"
}

// GoverningSnapshot returns the window with the highest used percent, the
// same governing rule the Codex quota path applies across its windows.
func (w QuotaWindows) GoverningSnapshot() (quota.Snapshot, bool) {
	var best *quota.Snapshot
	for _, s := range []*quota.Snapshot{w.Gem, w.Cla} {
		if s == nil {
			continue
		}
		if best == nil || usedPercent(s) > usedPercent(best) {
			best = s
		}
	}
	if best == nil {
		return quota.Snapshot{}, false
	}
	return *best, true
}

func usedPercent(s *quota.Snapshot) float64 {
	limit := 10000.0
	if s.Limit != nil && *s.Limit > 0 {
		limit = float64(*s.Limit)
	}
	return float64(s.Used) / limit * 100
}
