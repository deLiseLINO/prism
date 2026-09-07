package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"prism/internal/quota"
)

var usagePath = "https://chatgpt.com/backend-api/wham/usage"

const usageTimeout = 8 * time.Second

const usageMaxBytes = 512 << 10

// FetchUsage reads the account's live usage snapshot from the Codex WHAM
// usage endpoint. The credential must carry a ChatGPT account id: the
// endpoint answers per-account only when that header is present.
func FetchUsage(ctx context.Context, client *http.Client, cred Credential) (QuotaResult, error) {
	if client == nil {
		return QuotaResult{}, fmt.Errorf("codex: usage fetch needs an http client")
	}
	if cred.AccessToken == "" {
		return QuotaResult{}, fmt.Errorf("codex: usage fetch needs an access token")
	}
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usagePath, nil)
	if err != nil {
		return QuotaResult{}, fmt.Errorf("codex: usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	if cred.ChatGPTAccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", cred.ChatGPTAccountID)
	}
	res, err := client.Do(req)
	if err != nil {
		return QuotaResult{}, fmt.Errorf("codex: usage fetch: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return QuotaResult{}, fmt.Errorf("codex: usage fetch: status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, usageMaxBytes))
	if err != nil {
		return QuotaResult{}, fmt.Errorf("codex: usage read: %w", err)
	}
	return parseUsage(body)
}

type usageWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	ResetAt            *float64 `json:"reset_at"`
	LimitWindowSeconds *float64 `json:"limit_window_seconds"`
}

type usageRateLimit struct {
	PrimaryWindow   *usageWindow `json:"primary_window"`
	SecondaryWindow *usageWindow `json:"secondary_window"`
	TertiaryWindow  *usageWindow `json:"tertiary_window"`
}

type usageResponse struct {
	PlanType  string          `json:"plan_type"`
	RateLimit *usageRateLimit `json:"rate_limit"`
}

type UsageWindow struct {
	Label     string
	Used      int64
	Limit     *int64
	WindowEnd time.Time
}

// parseUsage classifies WHAM windows the same way ParseQuotaHeaders
// classifies header windows: by declared window length, never by plan name.
// The governing snapshot is the window with the highest used percent; every
// readable window is kept alongside so callers can render each limit slot.
func parseUsage(body []byte) (QuotaResult, error) {
	var payload usageResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return QuotaResult{}, fmt.Errorf("codex: usage decode: %w", err)
	}
	if payload.RateLimit == nil {
		return QuotaResult{}, fmt.Errorf("codex: usage payload has no rate_limit")
	}
	primary := usageReading(payload.RateLimit.PrimaryWindow)
	secondary := usageReading(payload.RateLimit.SecondaryWindow)
	tertiary := usageReading(payload.RateLimit.TertiaryWindow)

	primaryMonthly := primary.minutesSet && primary.minutes >= monthlyWindowMinMinutes
	primaryShort := primary.minutesSet && primary.minutes > 0 && primary.minutes < weeklyWindowMinMinutes

	var windows []windowReading
	switch {
	case primaryMonthly:
		windows = append(windows, primary, secondary)
	case primaryShort:
		windows = append(windows, secondary, primary)
	default:
		if primary.percentSet {
			windows = append(windows, primary)
		} else if secondary.percentSet {
			windows = append(windows, secondary)
		}
		if tertiary.percentSet {
			windows = append(windows, tertiary)
		}
	}

	governing, ok := governingWindow(windows)
	if !ok {
		return QuotaResult{}, fmt.Errorf("codex: usage payload carries no quota window")
	}
	snapshot := snapshotFromReading(governing, quota.SourceEndpoint)
	snapshot.Windows = windowsFromReadings(windows)
	return QuotaResult{Snapshot: snapshot, OK: true}, nil
}

func snapshotFromReading(r windowReading, src quota.Source) quota.Snapshot {
	s := quota.Snapshot{
		Used:   int64(math.Round(r.percent * 100)),
		Limit:  newInt64(10000),
		Source: src,
	}
	if r.resetPresent {
		s.WindowEnd = resetTime(r.resetAt)
	}
	return s
}

func windowsFromReadings(readings []windowReading) []quota.Window {
	var out []quota.Window
	for _, r := range readings {
		if !r.percentSet {
			continue
		}
		w := quota.Window{
			Label: windowLabel(r.minutes),
			Used:  int64(math.Round(r.percent * 100)),
			Limit: newInt64(10000),
		}
		if r.resetPresent {
			w.WindowEnd = resetTime(r.resetAt)
		}
		out = append(out, w)
	}
	return out
}

func windowLabel(minutes float64) string {
	if !math.IsNaN(minutes) && minutes > 0 {
		if minutes >= monthlyWindowMinMinutes {
			return "Monthly usage limit"
		}
		if minutes >= weeklyWindowMinMinutes {
			return "Weekly usage limit"
		}
		if minutes >= 60 {
			return fmt.Sprintf("%d hour usage limit", int(math.Round(minutes/60)))
		}
		return fmt.Sprintf("%d minute usage limit", int(math.Round(minutes)))
	}
	return "Usage limit"
}

func usageReading(w *usageWindow) windowReading {
	var r windowReading
	if w == nil {
		return r
	}
	if w.UsedPercent != nil {
		pct := *w.UsedPercent
		if !math.IsNaN(pct) && !math.IsInf(pct, 0) {
			r.percent = math.Max(0, math.Min(100, pct))
			r.percentSet = true
		}
	}
	if w.ResetAt != nil {
		reset := *w.ResetAt
		if !math.IsNaN(reset) && !math.IsInf(reset, 0) && reset >= 0 {
			r.resetAt = reset
			r.resetPresent = true
		}
	}
	if w.LimitWindowSeconds != nil {
		secs := *w.LimitWindowSeconds
		if !math.IsNaN(secs) && !math.IsInf(secs, 0) && secs > 0 {
			r.minutes = secs / 60
			r.minutesSet = true
		}
	}
	return r
}
