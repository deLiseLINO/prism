package codex

import (
	"context"
	"errors"
	"github.com/deLiseLINO/prism/internal/canon"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/provider"
)

func runAgainst(t *testing.T, status int, body string) provider.RunError {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer up.Close()
	runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "tok"}}, Client: up.Client()}
	req := provider.RunRequest{Target: provider.Target{BaseURL: up.URL}}
	err := runner.Run(context.Background(), req, &captureSink{})
	var re provider.RunError
	if !errors.As(err, &re) {
		t.Fatalf("expected RunError, got %T %v", err, err)
	}
	return re
}

// A 403 means this request was refused (plan, model entitlement, policy), not
// that the stored grant is dead; it must not take the account out of rotation.
func TestUpstreamForbiddenKeepsAccountUsable(t *testing.T) {
	re := runAgainst(t, http.StatusForbidden, `{"error":{"message":"model not available for your plan"}}`)
	if re.Class != provider.ClassForbidden {
		t.Fatalf("class = %d, want ClassForbidden", re.Class)
	}
}

func TestUpstreamContextLengthIsForwardedWithItsCode(t *testing.T) {
	re := runAgainst(t, http.StatusBadRequest, `{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window"}}`)
	if re.Class != provider.ClassContextLength {
		t.Fatalf("class = %d, want ClassContextLength", re.Class)
	}
	if re.Reported == nil || !strings.Contains(string(re.Reported.Error), "context_length_exceeded") {
		t.Fatalf("provider error not forwarded: %+v", re.Reported)
	}
}

type rotatingCreds struct {
	current  string
	next     string
	rejected []string
}

func (c *rotatingCreds) Credential(context.Context, account.Lease) (Credential, error) {
	return Credential{AccessToken: c.current}, nil
}

func (c *rotatingCreds) RefreshRejected(_ context.Context, _ account.Lease, rejected string) (Credential, error) {
	c.rejected = append(c.rejected, rejected)
	c.current = c.next
	return Credential{AccessToken: c.next}, nil
}

// A 401 on a credential the store still considers fresh (revoked or rotated
// server-side) must renew the credential and replay once before the account
// is reported as rejected.
func TestUnauthorizedRefreshesCredentialAndReplaysOnce(t *testing.T) {
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer new-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{}}\n\n"))
	}))
	defer up.Close()
	creds := &rotatingCreds{current: "old-token", next: "new-token"}
	runner := &Runner{Creds: creds, Client: up.Client()}
	sink := &captureSink{}
	err := runner.Run(context.Background(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}}, sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 2 || seen[1] != "Bearer new-token" {
		t.Fatalf("upstream saw %v, want one rejected attempt then one with the renewed token", seen)
	}
	if len(creds.rejected) != 1 || creds.rejected[0] != "old-token" {
		t.Fatalf("refresh asked to replace %v, want [old-token]", creds.rejected)
	}
}

func TestUnauthorizedAfterRefreshIsReportedOnce(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer up.Close()
	runner := &Runner{Creds: &rotatingCreds{current: "old-token", next: "new-token"}, Client: up.Client()}
	err := runner.Run(context.Background(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}}, &captureSink{})
	var re provider.RunError
	if !errors.As(err, &re) || re.Class != provider.ClassUnauthorized {
		t.Fatalf("err = %v, want unauthorized RunError", err)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want exactly 2 (original + one replay)", calls)
	}
}

func TestRateLimitCarriesRetryAfter(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "13")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer up.Close()
	runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "tok"}}, Client: up.Client()}
	err := runner.Run(context.Background(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}}, &captureSink{})
	var re provider.RunError
	if !errors.As(err, &re) || re.RetryAfter != 13*time.Second {
		t.Fatalf("err = %v, want RunError with RetryAfter 13s", err)
	}
}

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

// Encrypted reasoning is minted per caller. After a rotation to another account
// the upstream refuses the replayed blob; the turn is not lost, it is resent
// once without the reasoning items.
func TestRejectedReasoningBlobIsResentWithoutReasoning(t *testing.T) {
	var bodies []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if strings.Contains(string(b), `"type":"reasoning"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"invalid_encrypted_content","message":"The encrypted content gAAA could not be verified."}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{}}\n\n"))
	}))
	defer up.Close()
	runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "tok"}}, Client: up.Client()}
	req := provider.RunRequest{
		Target: provider.Target{BaseURL: up.URL},
		Request: canon.Request{Model: "gpt-5.2-codex", Input: []canon.Item{
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
			canon.ReasoningItem{ID: "rs_1", State: canon.OpaqueRef{Store: reasoningStoreNative, Key: "blob"}},
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "again"}}},
		}},
	}
	if err := runner.Run(context.Background(), req, &captureSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(bodies) != 2 || strings.Contains(bodies[1], `"type":"reasoning"`) || !strings.Contains(bodies[1], "again") {
		t.Fatalf("upstream saw %d bodies; second must keep the turn and drop reasoning: %v", len(bodies), bodies)
	}
}

func TestRejectedReasoningBlobIsResentOnlyOnce(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"invalid_encrypted_content","message":"x"}}`))
	}))
	defer up.Close()
	runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "tok"}}, Client: up.Client()}
	req := provider.RunRequest{Target: provider.Target{BaseURL: up.URL}, Request: canon.Request{Model: "m", Input: []canon.Item{
		canon.ReasoningItem{ID: "rs_1", State: canon.OpaqueRef{Store: reasoningStoreNative, Key: "blob"}},
	}}}
	if err := runner.Run(context.Background(), req, &captureSink{}); err == nil {
		t.Fatal("expected the second rejection to surface")
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want exactly 2", calls)
	}
}
