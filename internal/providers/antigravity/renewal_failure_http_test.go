package antigravity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestRenewalFailureKeepsRefusalAndCredentialClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		class provider.ErrorClass
		kind  provider.RunErrorKind
	}{
		{"invalid grant", account.ErrNeedsReauth, provider.ClassUnauthorized, provider.TerminalOmitted},
		{"token unavailable", account.ErrRefreshTransient, provider.ClassTransport, provider.Retryable},
		{"unknown renewal failure", errors.New("credential store unavailable"), provider.ClassTransport, provider.Retryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			const detail = `{"message":"access refused","code":"access_rejected","detail":{"request":"edge"}}`
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":`+detail+`}`)
			}))
			defer up.Close()
			creds := &renewingCreds{stubCreds: stubCreds{pair: CredentialPair{AccessToken: "old-token"}}, err: tc.err}
			runner, err := NewRunner(creds, up.Client(), up.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(t.Context(), testRequest(), &recordingSink{})
			var re provider.RunError
			if !errors.As(err, &re) || re.Class != tc.class || re.Kind != tc.kind || !re.ReplaySafe || !errors.Is(err, tc.err) {
				t.Fatalf("renewal result=%v, want class=%v kind=%v cause=%v", err, tc.class, tc.kind, tc.err)
			}
			if re.Reported == nil || re.Reported.Status != http.StatusUnauthorized || string(re.Reported.Error) != detail {
				t.Fatalf("lost inference refusal: report=%+v retryAfter=%v", re.Reported, re.RetryAfter)
			}
			if calls.Load() != 1 || len(creds.rejected) != 1 || creds.rejected[0] != "old-token" {
				t.Fatalf("requests=%d rejected=%v", calls.Load(), creds.rejected)
			}
		})
	}
}

func TestIncompleteErrorBodyKeepsKnownHTTPFailure(t *testing.T) {
	for _, tc := range []struct {
		status int
		class  provider.ErrorClass
	}{
		{http.StatusUnauthorized, provider.ClassUnauthorized},
		{http.StatusTooManyRequests, provider.ClassRateLimited},
		{http.StatusServiceUnavailable, provider.ClassServer},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			const partial = `{"error":{"message":"refused`
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "11")
				w.Header().Set("Content-Length", "200")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, partial)
			}))
			defer up.Close()
			runner, err := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture"}}, up.Client(), up.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(t.Context(), testRequest(), &recordingSink{})
			var re provider.RunError
			if !errors.As(err, &re) || re.Class != tc.class || re.Reported == nil || re.Reported.Status != tc.status {
				t.Fatalf("incomplete response replaced HTTP failure: %v report=%+v", err, re.Reported)
			}
			var message string
			if json.Unmarshal(re.Reported.Error, &message) != nil || message != partial {
				t.Fatalf("partial report=%s", re.Reported.Error)
			}
			if calls.Load() != 1 {
				t.Fatalf("incomplete body changed retry policy: calls=%d", calls.Load())
			}
		})
	}
}
