package tui

import (
	"context"
	"prism/internal/config"
	"prism/internal/management"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClientAccounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/accounts" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"accounts":[{"id":"acc-1","provider":"codex","state":"active","quota":{"used":10,"limit":100,"windowEnd":"2026-09-06T12:00:00Z","source":"header"}}]}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "secret", nil)
	out, err := client.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Accounts) != 1 || out.Accounts[0].ID != "acc-1" {
		t.Fatalf("accounts = %+v", out.Accounts)
	}
	if out.Accounts[0].Quota.Limit == nil || *out.Accounts[0].Quota.Limit != 100 {
		t.Fatalf("quota limit = %+v", out.Accounts[0].Quota)
	}
}

func TestHTTPClientErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":"not_found","message":"account missing"}}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "", nil)
	_, err := client.AccountQuota(context.Background(), "acc-404")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "account missing") {
		t.Fatalf("error = %v", err)
	}
}

func TestHTTPClientEscapesAccountID(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Write([]byte(`{"account":"codex:default","quota":{"used":0,"windowEnd":"0001-01-01T00:00:00Z","source":"unknown"}}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "", nil)
	if _, err := client.AccountQuota(context.Background(), "codex:default"); err != nil {
		t.Fatalf("quota: %v", err)
	}
	if gotPath != "/api/v1/accounts/codex%3Adefault/quota" {
		t.Fatalf("path = %s, want /api/v1/accounts/codex%%3Adefault/quota", gotPath)
	}
}

func TestHTTPClientPauseAccountSendsVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}
		if r.URL.Path != "/api/v1/accounts/acc-1/pause" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		if !strings.Contains(readBody(r), `"version":7`) {
			t.Errorf("body = %q", readBody(r))
		}
		w.Write([]byte(`{"id":"acc-1","state":"paused"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "", nil)
	account, err := client.PauseAccount(context.Background(), "acc-1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if account.State != "paused" {
		t.Fatalf("state = %q", account.State)
	}
}

func TestHTTPClientDeleteNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "", nil)
	if err := client.DeleteAccount(context.Background(), "acc-1"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPClientAuthStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/codex/status" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("session") != "sess-1" {
			t.Errorf("session = %q", r.URL.Query().Get("session"))
		}
		w.Write([]byte(`{"provider":"codex","state":"pending"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "", nil)
	status, err := client.AuthStatus(context.Background(), "codex", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "pending" {
		t.Fatalf("state = %q", status.State)
	}
}

func TestURLPathEscapeKeepsSafeChars(t *testing.T) {
	if got := urlPathEscape("acc-1"); got != "acc-1" {
		t.Fatalf("got %q", got)
	}
	if got := urlPathEscape("acc/1"); got != "acc%2F1" {
		t.Fatalf("got %q", got)
	}
}

func TestHTTPClientProvidersList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/providers" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"generation":3,"providers":[{"id":"codex","wire":"responses","pool":{"strategy":"quota","pinnedAccount":"codex:default"}}]}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "", nil)
	out, err := client.ProvidersList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Generation != 3 || len(out.Providers) != 1 || out.Providers[0].ID != "codex" {
		t.Fatalf("providers = %+v", out)
	}
	if out.Providers[0].Pool == nil || out.Providers[0].Pool.PinnedAccount != "codex:default" {
		t.Fatalf("pool = %+v", out.Providers[0].Pool)
	}
}

func TestHTTPClientProvidersReplace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %q", r.Method)
		}
		if r.URL.EscapedPath() != "/api/v1/providers/codex" {
			t.Errorf("path = %q", r.URL.EscapedPath())
		}
		if !strings.Contains(readBody(r), `"pinnedAccount":"codex:default"`) {
			t.Errorf("body = %q", readBody(r))
		}
		w.Write([]byte(`{"generation":8,"provider":{"id":"codex","wire":"responses"}}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "", nil)
	pool := config.PoolSettings{Strategy: config.PoolQuota, PinnedAccount: "codex:default"}
	write := management.ProviderWrite{ID: "codex", Wire: "responses", Pool: &pool, ExpectedGeneration: 7}
	resp, err := client.ProvidersReplace(context.Background(), "codex", write)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Generation != 8 {
		t.Fatalf("generation = %d", resp.Generation)
	}
}

func readBody(r *http.Request) string {
	buf := make([]byte, 256)
	n, _ := r.Body.Read(buf)
	return string(buf[:n])
}
