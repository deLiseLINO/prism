package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
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

func quotaSummaryURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/v1internal:retrieveUserQuotaSummary"
}

type QuotaSummaryBucket struct {
	BucketID          string
	DisplayName       string
	Window            string
	RemainingFraction float64
	HasFraction       bool
	ResetTime         time.Time
	Disabled          bool
}

type QuotaSummaryGroup struct {
	DisplayName string
	Buckets     []QuotaSummaryBucket
}

type QuotaSummary struct {
	Groups []QuotaSummaryGroup
}

type quotaSummaryResponse struct {
	Groups []struct {
		DisplayName string `json:"displayName"`
		Buckets     []struct {
			BucketID          string  `json:"bucketId"`
			DisplayName       string  `json:"displayName"`
			Window            string  `json:"window"`
			RemainingFraction float64 `json:"remainingFraction"`
			ResetTime         string  `json:"resetTime"`
			Disabled          bool    `json:"disabled"`
		} `json:"buckets"`
	} `json:"groups"`
}

func FetchQuotaSummary(ctx context.Context, client *http.Client, baseURL string, cred CredentialPair) (QuotaSummary, error) {
	if client == nil {
		return QuotaSummary{}, fmt.Errorf("antigravity: quota summary fetch needs an http client")
	}
	if cred.AccessToken == "" {
		return QuotaSummary{}, fmt.Errorf("antigravity: quota summary fetch needs an access token")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, quotaSummaryURL(baseURL), strings.NewReader(`{"project":""}`))
	if err != nil {
		return QuotaSummary{}, fmt.Errorf("antigravity: quota summary request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("User-Agent", RequestUserAgent())
	res, err := client.Do(req)
	if err != nil {
		return QuotaSummary{}, fmt.Errorf("antigravity: quota summary fetch: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return QuotaSummary{}, fmt.Errorf("antigravity: quota summary fetch: status %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, quotaMaxBytes))
	if err != nil {
		return QuotaSummary{}, fmt.Errorf("antigravity: quota summary read: %w", err)
	}
	return decodeQuotaSummary(raw)
}

func decodeQuotaSummary(data []byte) (QuotaSummary, error) {
	var payload quotaSummaryResponse
	if err := json.Unmarshal(data, &payload); err != nil {
		return QuotaSummary{}, fmt.Errorf("antigravity: quota summary decode: %w", err)
	}
	if payload.Groups == nil {
		return QuotaSummary{}, fmt.Errorf("antigravity: quota summary payload has no groups")
	}
	out := QuotaSummary{}
	for _, group := range payload.Groups {
		parsed := QuotaSummaryGroup{DisplayName: group.DisplayName}
		for _, bucket := range group.Buckets {
			entry := QuotaSummaryBucket{
				BucketID:          bucket.BucketID,
				DisplayName:       bucket.DisplayName,
				Window:            bucket.Window,
				RemainingFraction: bucket.RemainingFraction,
				Disabled:          bucket.Disabled,
			}
			if reset, ok := parseResetTime(bucket.ResetTime); ok {
				entry.ResetTime = reset
			}
			parsed.Buckets = append(parsed.Buckets, entry)
		}
		out.Groups = append(out.Groups, parsed)
	}
	return out, nil
}

func (s QuotaSummary) Windows() []quota.Window {
	var out []quota.Window
	for _, group := range s.Groups {
		for _, bucket := range group.Buckets {
			if bucket.Disabled || bucket.RemainingFraction < 0 || bucket.RemainingFraction > 1 {
				continue
			}
			limit := int64(10000)
			w := quota.Window{
				Label: summaryBucketLabel(group, bucket),
				Used:  int64(math.Round((1 - bucket.RemainingFraction) * 10000)),
				Limit: &limit,
			}
			if !bucket.ResetTime.IsZero() {
				w.WindowEnd = bucket.ResetTime
			}
			out = append(out, w)
		}
	}
	return out
}

func summaryBucketLabel(group QuotaSummaryGroup, bucket QuotaSummaryBucket) string {
	family := "Antigravity"
	if strings.Contains(strings.ToLower(group.DisplayName), "gemini") {
		family = "Gemini"
	} else if strings.Contains(strings.ToLower(group.DisplayName), "claude") {
		family = "Claude"
	}
	haystack := strings.ToLower(bucket.BucketID + " " + bucket.Window + " " + bucket.DisplayName)
	switch {
	case strings.Contains(haystack, "5h") || strings.Contains(haystack, "5 h") || strings.Contains(haystack, "five hour"):
		return family + " 5 hour"
	case strings.Contains(haystack, "week"):
		return family + " Weekly"
	}
	name := bucket.DisplayName
	if name == "" {
		name = bucket.Window
	}
	if name == "" {
		name = bucket.BucketID
	}
	return family + " " + name
}

func (w QuotaWindows) QuotaWindows() []quota.Window {
	out := make([]quota.Window, 0, 2)
	for _, entry := range []struct {
		label string
		snap  *quota.Snapshot
	}{
		{label: "Gemini 5 hour", snap: w.Gem},
		{label: "Claude 5 hour", snap: w.Cla},
	} {
		if entry.snap == nil {
			continue
		}
		limit := int64(10000)
		window := quota.Window{
			Label: entry.label,
			Used:  entry.snap.Used,
			Limit: &limit,
		}
		if !entry.snap.WindowEnd.IsZero() {
			window.WindowEnd = entry.snap.WindowEnd
		}
		out = append(out, window)
	}
	return out
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

// DetailedSnapshot returns the governing snapshot enriched with per-family
// windows: "5 hour Gemini", "Weekly Gemini", "5 hour Claude", "Weekly Claude".
func (w QuotaWindows) DetailedSnapshot() (quota.Snapshot, bool) {
	governing, ok := w.GoverningSnapshot()
	if !ok {
		return quota.Snapshot{}, false
	}
	out := governing
	out.Windows = nil
	for _, family := range []struct {
		label string
		snap  *quota.Snapshot
	}{
		{"Gemini", w.Gem},
		{"Claude", w.Cla},
	} {
		if family.snap == nil {
			continue
		}
		out.Windows = append(out.Windows, quota.Window{
			Label:     "5 hour " + family.label,
			Used:      family.snap.Used,
			Limit:     family.snap.Limit,
			WindowEnd: family.snap.WindowEnd,
		})
		out.Windows = append(out.Windows, quota.Window{
			Label:     "Weekly " + family.label,
			Used:      family.snap.Used,
			Limit:     family.snap.Limit,
			WindowEnd: family.snap.WindowEnd,
		})
	}
	return out, true
}

func usedPercent(s *quota.Snapshot) float64 {
	limit := 10000.0
	if s.Limit != nil && *s.Limit > 0 {
		limit = float64(*s.Limit)
	}
	return float64(s.Used) / limit * 100
}
