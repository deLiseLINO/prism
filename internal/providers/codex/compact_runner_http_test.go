package codex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/provider"
)

func TestCompactUnauthorizedRefreshesCredentialAndReplaysOnce(t *testing.T) {
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer new-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary"}]}]}`))
	}))
	defer up.Close()
	creds := &rotatingCreds{current: "old-token", next: "new-token"}
	runner := &Runner{Creds: creds, Client: up.Client()}
	res, err := runner.Compact(context.Background(), provider.CompactRequest{Target: provider.Target{BaseURL: up.URL}})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(seen) != 2 || seen[1] != "Bearer new-token" {
		t.Fatalf("upstream saw %v, want one rejected attempt then one with the renewed token", seen)
	}
	if len(creds.rejected) != 1 || creds.rejected[0] != "old-token" {
		t.Fatalf("refresh asked to replace %v, want [old-token]", creds.rejected)
	}
	if len(res.Summary.Content) == 0 {
		t.Fatalf("empty compact result: %+v", res)
	}
}

func TestCompactUnauthorizedAfterRefreshIsReportedOnce(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer up.Close()
	runner := &Runner{Creds: &rotatingCreds{current: "old-token", next: "new-token"}, Client: up.Client()}
	_, err := runner.Compact(context.Background(), provider.CompactRequest{Target: provider.Target{BaseURL: up.URL}})
	var re provider.RunError
	if !errors.As(err, &re) || re.Class != provider.ClassUnauthorized {
		t.Fatalf("err = %v, want unauthorized RunError", err)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want exactly 2", calls)
	}
}