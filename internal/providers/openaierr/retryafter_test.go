package openaierr

import (
	"net/http"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/provider"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		headers map[string]string
		want    time.Duration
	}{
		{"seconds", map[string]string{"Retry-After": "7"}, 7 * time.Second},
		{"fractional seconds", map[string]string{"Retry-After": "1.5"}, 1500 * time.Millisecond},
		{"http date", map[string]string{"Retry-After": now.Add(90 * time.Second).Format(http.TimeFormat)}, 90 * time.Second},
		{"past date", map[string]string{"Retry-After": now.Add(-time.Minute).Format(http.TimeFormat)}, 0},
		{"millis wins over seconds", map[string]string{"Retry-After-Ms": "250", "Retry-After": "9"}, 250 * time.Millisecond},
		{"garbage", map[string]string{"Retry-After": "soon"}, 0},
		{"negative", map[string]string{"Retry-After": "-5"}, 0},
		{"absent", nil, 0},
		{"huge is capped", map[string]string{"Retry-After": "99999999"}, provider.MaxRetryAfter},
	}
	for _, tc := range cases {
		h := http.Header{}
		for k, v := range tc.headers {
			h.Set(k, v)
		}
		if got := provider.ParseRetryAfter(h, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHTTPErrorCarriesRetryAfterOnlyForThrottleStatuses(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   time.Duration
	}{
		{429, 12 * time.Second},
		{503, 12 * time.Second},
		{500, 0},
		{400, 0},
	} {
		resp := responseWith(tc.status, `{"error":{"message":"slow"}}`)
		resp.Header.Set("Retry-After", "12")
		re, ok := HTTPError(resp, "test").(provider.RunError)
		if !ok {
			t.Fatalf("status %d: not a RunError", tc.status)
		}
		if re.RetryAfter != tc.want {
			t.Errorf("status %d: RetryAfter = %v, want %v", tc.status, re.RetryAfter, tc.want)
		}
	}
}
