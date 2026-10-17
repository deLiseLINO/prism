package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/auth"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestClineRenewalFailurePreservesInferenceRefusalAndAccountLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		class     provider.ErrorClass
		kind      provider.RunErrorKind
		needsAuth bool
	}{
		{"invalid grant", http.StatusBadRequest, `{"error":"invalid_grant"}`, provider.ClassUnauthorized, provider.TerminalOmitted, true},
		{"token unavailable", http.StatusServiceUnavailable, `{"error":"server_error"}`, provider.ClassTransport, provider.Retryable, false},
		{"token network failure", 0, "", provider.ClassTransport, provider.Retryable, false},
		{"unrecognized refusal", http.StatusBadRequest, `{"error":"unknown_error"}`, provider.ClassTransport, provider.Retryable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var inferenceCalls, tokenCalls atomic.Int32
			const detail = `{"message":"access refused","code":"access_rejected","detail":{"request":"edge"}}`
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/token" {
					tokenCalls.Add(1)
					if tc.status == 0 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						conn.Close()
						return
					}
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
					return
				}
				inferenceCalls.Add(1)
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":`+detail+`}`)
			}))
			defer up.Close()
			env, _ := testEnv(t, config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{"edge": {Wire: config.WireCline, BaseURL: up.URL, Models: []string{"model"}}}})
			cfg := auth.CodexProduction
			cfg.TokenURL = up.URL + "/token"
			flow, err := auth.NewCodexFlow(cfg, auth.Options{HTTP: up.Client()})
			if err != nil {
				t.Fatal(err)
			}
			env.ensureRepo("edge", nil)
			sink := auth.NewFileSink(env.creds.file, env.repos, env.pool)
			a, err := sink.Persist(context.Background(), "edge", account.Credential{AccountID: "identity", Access: "old-token", Refresh: "old-grant", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			sink.Register(a)
			ref, err := auth.NewRefresher(auth.RefresherOptions{File: env.creds.file, Repos: env.repos, Pool: env.pool.(auth.RefreshPool), Flows: map[account.ProviderID]auth.Flow{"edge": flow}})
			if err != nil {
				t.Fatal(err)
			}
			env.refresher = ref
			lease, err := env.pool.Acquire(t.Context(), account.AcquireRequest{Provider: "edge"})
			if err != nil {
				t.Fatal(err)
			}
			runner := clineRunner(env, config.Provider{Wire: config.WireCline, BaseURL: up.URL})
			err = runner.Run(t.Context(), provider.RunRequest{Lease: lease, Target: provider.Target{Provider: "edge", Wire: provider.WireChat, BaseURL: up.URL}, Request: canon.Request{Model: "model"}}, &countSink{})
			var re provider.RunError
			if !errors.As(err, &re) || re.Class != tc.class || re.Kind != tc.kind || !re.ReplaySafe {
				t.Fatalf("renewal result=%v, want class=%v kind=%v", err, tc.class, tc.kind)
			}
			if re.Reported == nil || re.Reported.Status != http.StatusUnauthorized || string(re.Reported.Error) != detail {
				t.Fatalf("inference refusal replaced: report=%+v retryAfter=%v", re.Reported, re.RetryAfter)
			}
			if inferenceCalls.Load() != 1 || tokenCalls.Load() != 1 {
				t.Fatalf("requests=%d renewals=%d", inferenceCalls.Load(), tokenCalls.Load())
			}
			_, acquireErr := env.pool.Acquire(t.Context(), account.AcquireRequest{Provider: "edge"})
			if tc.needsAuth {
				var blocked *account.AcquireError
				if !errors.As(acquireErr, &blocked) || blocked.Reason != account.AccountNeedsReauth {
					t.Fatalf("hard renewal failure did not block account: %v", acquireErr)
				}
			} else if acquireErr != nil {
				t.Fatalf("transient or unknown renewal failure blocked account: %v", acquireErr)
			}
		})
	}
}
