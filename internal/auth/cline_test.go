package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
)

func clineFixtureTokenSrv(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/refresh" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClineRefreshRotatesAndPrefixes(t *testing.T) {
	srv := clineFixtureTokenSrv(t, http.StatusOK, `{"success":true,"data":{"accessToken":"bare-jwt","refreshToken":"rt-2","expiresAt":"2030-01-01T00:00:00Z","userInfo":{"clineUserId":"u-1"}}}`)
	flow := NewClineFlow(Options{})
	original := clineDefaultBaseURL
	clineDefaultBaseURL = srv.URL
	t.Cleanup(func() { clineDefaultBaseURL = original })
	prev := account.Credential{Access: "workos:old", Refresh: "rt-1", AccountID: "u-1"}
	next, err := flow.Refresh(context.Background(), prev)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if next.Access != "workos:bare-jwt" {
		t.Fatalf("access = %q, want workos-prefixed", next.Access)
	}
	if next.Refresh != "rt-2" {
		t.Fatalf("refresh grant = %q, want rotated", next.Refresh)
	}
	if next.AccountID != "u-1" {
		t.Fatalf("account id = %q, want u-1", next.AccountID)
	}
	want, _ := time.Parse(time.RFC3339, "2030-01-01T00:00:00Z")
	if !next.ExpiresAt.Equal(want) {
		t.Fatalf("expiresAt = %v, want %v", next.ExpiresAt, want)
	}
}

func TestClineRefreshKeepsGrantWhenOmitted(t *testing.T) {
	srv := clineFixtureTokenSrv(t, http.StatusOK, `{"success":true,"data":{"accessToken":"bare-jwt","expiresAt":"2030-01-01T00:00:00Z"}}`)
	flow := NewClineFlow(Options{})
	original := clineDefaultBaseURL
	clineDefaultBaseURL = srv.URL
	t.Cleanup(func() { clineDefaultBaseURL = original })
	prev := account.Credential{Access: "workos:old", Refresh: "rt-1"}
	next, err := flow.Refresh(context.Background(), prev)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if next.Refresh != "rt-1" {
		t.Fatalf("refresh grant = %q, want kept", next.Refresh)
	}
}

func TestClineRefreshRequiresGrant(t *testing.T) {
	flow := NewClineFlow(Options{})
	_, err := flow.Refresh(context.Background(), account.Credential{Access: "workos:old"})
	if err == nil || !strings.Contains(err.Error(), "refresh grant") {
		t.Fatalf("err = %v, want missing grant error", err)
	}
}

func TestClineRefreshNeverEchoesBody(t *testing.T) {
	srv := clineFixtureTokenSrv(t, http.StatusForbidden, `{"error":{"message":"token-secret-material"}}`)
	flow := NewClineFlow(Options{})
	original := clineDefaultBaseURL
	clineDefaultBaseURL = srv.URL
	t.Cleanup(func() { clineDefaultBaseURL = original })
	_, err := flow.Refresh(context.Background(), account.Credential{Access: "workos:old", Refresh: "rt-1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "token-secret-material") {
		t.Fatalf("error leaks response body: %v", err)
	}
}

func TestClineDeviceLoginRegistersTokens(t *testing.T) {
	var polls int
	workos := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user_management/authorize/device":
			fmt.Fprint(w, `{"device_code":"dev-1","user_code":"ABCD-EFGH","verification_uri":"https://auth.example/device","verification_uri_complete":"https://auth.example/device?user_code=ABCD-EFGH","expires_in":60,"interval":1}`)
		case "/user_management/authenticate":
			polls++
			if polls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"authorization_pending"}`)
				return
			}
			fmt.Fprint(w, `{"access_token":"workos-access","refresh_token":"workos-refresh"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(workos.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/register" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"success":true,"data":{"accessToken":"bare-jwt","refreshToken":"rt-9","expiresAt":1893456000000,"userInfo":{"clineUserId":"u-9","email":"a@example.com"}}}`)
	}))
	t.Cleanup(api.Close)
	originalWorkOS := workOSAPIBase
	workOSAPIBase = workos.URL
	t.Cleanup(func() { workOSAPIBase = originalWorkOS })
	flow := NewClineFlow(Options{Now: time.Now})
	flow.baseURL = api.URL
	started, err := flow.BeginDevice(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if started.UserCode != "ABCD-EFGH" || started.URL != "https://auth.example/device?user_code=ABCD-EFGH" {
		t.Fatalf("start = %+v", started)
	}
	if !started.ExpiresAt.After(time.Now().Add(30 * time.Second)) {
		t.Fatalf("expiresAt = %v, code lifetime was not kept", started.ExpiresAt)
	}
	cred, err := flow.ExchangeDevice(context.Background(), started)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if cred.Access != "workos:bare-jwt" || cred.Refresh != "rt-9" || cred.AccountID != "u-9" || cred.Email != "a@example.com" {
		t.Fatalf("cred = %+v", cred)
	}
}
