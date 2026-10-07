package daemon

import (
	"context"
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

func TestClineRejectedCredentialRenewsOnceAndReportsActualGeneration(t *testing.T) {
	for _, replacementRejected := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement-rejected=%t", replacementRejected), func(t *testing.T) {
			var inferenceCalls, tokenCalls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/token" {
					tokenCalls.Add(1)
					fmt.Fprint(w, `{"access_token":"renewed-token","refresh_token":"renewed-grant","expires_in":3600}`)
					return
				}
				inferenceCalls.Add(1)
				if r.Header.Get("Authorization") == "Bearer old-token" || replacementRejected {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, `{"error":{"message":"rejected"}}`)
					return
				}
				if r.Header.Get("Authorization") != "Bearer renewed-token" {
					t.Error("wrong renewed credential")
				}
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer up.Close()
			env, _ := testEnv(t, config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{"edge": {Wire: config.WireCline, BaseURL: up.URL, Models: []string{"model"}}}})
			cfg := auth.CodexProduction
			cfg.TokenURL = up.URL + "/token"
			flow, err := auth.NewCodexFlow(cfg, auth.Options{HTTP: up.Client()})
			if err != nil {
				t.Fatal(err)
			}
			repo := env.ensureRepo("edge", nil)
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
			lease, err := env.pool.Acquire(context.Background(), account.AcquireRequest{Provider: "edge"})
			if err != nil {
				t.Fatal(err)
			}
			var generations []account.CredentialGeneration
			runner := clineRunner(env, config.Provider{Wire: config.WireCline, BaseURL: up.URL})
			err = runner.Run(context.Background(), provider.RunRequest{Lease: lease, Target: provider.Target{Provider: "edge", Wire: provider.WireChat, BaseURL: up.URL}, Request: canon.Request{Model: "model"}, CredentialObserver: func(g account.CredentialGeneration) { generations = append(generations, g) }}, &countSink{})
			if replacementRejected && err == nil || !replacementRejected && err != nil {
				t.Fatalf("result=%v", err)
			}
			gen, genErr := repo.CurrentGeneration("edge", a.ID)
			if tokenCalls.Load() != 1 || inferenceCalls.Load() != 2 || genErr != nil || gen != 2 || len(generations) != 2 || generations[0] != 1 || generations[1] != 2 {
				t.Fatalf("bounded lifecycle tokens=%d requests=%d gen=%d observed=%v err=%v", tokenCalls.Load(), inferenceCalls.Load(), gen, generations, genErr)
			}
		})
	}
}
