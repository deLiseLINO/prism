package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"prism/internal/quota"
)

func TestFetchUsageParsesGoverningWindow(t *testing.T) {
	var gotPath, gotAuth, gotAccount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"plan_type": "pro",
			"rate_limit": {
				"primary_window": {"used_percent": 12.5, "reset_at": 1800000000, "limit_window_seconds": 300},
				"secondary_window": {"used_percent": 41.6, "reset_at": 1800000600, "limit_window_seconds": 2592000}
			}
		}`))
	}))
	t.Cleanup(server.Close)

	origPath := usagePath
	setUsagePath(t, server.URL+"/wham/usage")

	result, err := FetchUsage(context.Background(), server.Client(), Credential{AccessToken: "tok", ChatGPTAccountID: "acct-1"})
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if gotPath != "/wham/usage" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotAuth != "Bearer tok" || gotAccount != "acct-1" {
		t.Fatalf("auth headers: %q %q", gotAuth, gotAccount)
	}
	if !result.OK {
		t.Fatalf("result not ok: %+v", result)
	}
	// primary is a short (5-hour) window, so secondary (monthly, 41.6%) governs.
	if result.Snapshot.Used != 4160 || result.Snapshot.Limit == nil || *result.Snapshot.Limit != 10000 {
		t.Fatalf("snapshot = %+v, want 4160/10000 basis points", result.Snapshot)
	}
	if result.Snapshot.Source != quota.SourceEndpoint {
		t.Fatalf("source = %v", result.Snapshot.Source)
	}
	if result.Snapshot.WindowEnd != time.Unix(1800000600, 0).UTC() {
		t.Fatalf("window end = %v", result.Snapshot.WindowEnd)
	}
	_ = origPath
}

func TestFetchUsageMonthlyPrimaryGoverns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"plan_type": "go",
			"rate_limit": {
				"primary_window": {"used_percent": 80, "reset_at": 1800000000, "limit_window_seconds": 2592000},
				"secondary_window": {"used_percent": 10, "reset_at": 1800000600, "limit_window_seconds": 604800}
			}
		}`))
	}))
	t.Cleanup(server.Close)
	setUsagePath(t, server.URL+"/wham/usage")

	result, err := FetchUsage(context.Background(), server.Client(), Credential{AccessToken: "tok"})
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if result.Snapshot.Used != 8000 {
		t.Fatalf("used = %d, want monthly 80%% governing", result.Snapshot.Used)
	}
}

func TestFetchUsageKeepsAllWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"plan_type": "pro",
			"rate_limit": {
				"primary_window": {"used_percent": 71, "reset_at": 1800000000, "limit_window_seconds": 18000},
				"secondary_window": {"used_percent": 12.5, "reset_at": 1800100000, "limit_window_seconds": 604800}
			}
		}`))
	}))
	t.Cleanup(server.Close)
	setUsagePath(t, server.URL+"/wham/usage")

	result, err := FetchUsage(context.Background(), server.Client(), Credential{AccessToken: "tok"})
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if len(result.Snapshot.Windows) != 2 {
		t.Fatalf("windows = %+v, want 2", result.Snapshot.Windows)
	}
	weekly, fiveHour := result.Snapshot.Windows[0], result.Snapshot.Windows[1]
	if fiveHour.Label != "5 hour usage limit" || fiveHour.Used != 7100 {
		t.Fatalf("5h window = %+v", fiveHour)
	}
	if weekly.Label != "Weekly usage limit" || weekly.Used != 1250 {
		t.Fatalf("weekly window = %+v", weekly)
	}
	if !weekly.WindowEnd.Equal(time.Unix(1800100000, 0).UTC()) {
		t.Fatalf("weekly windowEnd = %v", weekly.WindowEnd)
	}
}

func TestFetchUsageRejectsEmptyAndBroken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"plan_type":"free","rate_limit":{}}`))
	}))
	t.Cleanup(server.Close)
	setUsagePath(t, server.URL+"/wham/usage")

	if _, err := FetchUsage(context.Background(), server.Client(), Credential{AccessToken: "tok"}); err == nil {
		t.Fatal("payload without quota windows must error")
	}
	if _, err := FetchUsage(context.Background(), server.Client(), Credential{}); err == nil {
		t.Fatal("missing access token must error")
	}
}

func TestFetchUsageRejectsNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	setUsagePath(t, server.URL+"/wham/usage")

	if _, err := FetchUsage(context.Background(), server.Client(), Credential{AccessToken: "tok"}); err == nil {
		t.Fatal("non-200 response must error")
	}
}

func setUsagePath(t *testing.T, url string) {
	t.Helper()
	previous := usagePath
	usagePath = url
	t.Cleanup(func() { usagePath = previous })
}
