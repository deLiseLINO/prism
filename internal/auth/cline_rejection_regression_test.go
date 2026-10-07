package auth

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/store"
)

func TestClineRejectedRefreshPreservesPermanentCredentialFailure(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := clineFixtureTokenSrv(t, status, `{"error":{"message":"synthetic refusal"}}`)
			original := clineDefaultBaseURL
			clineDefaultBaseURL = server.URL
			t.Cleanup(func() { clineDefaultBaseURL = original })
			root := t.TempDir()
			file := store.NewFileCredentialStore(root)
			repo := account.OpenMeta(filepath.Join(root, "accounts.json"))
			pool := account.New()
			sink := NewFileSink(file, map[account.ProviderID]*account.Repository{"cline": repo}, pool)
			credential := account.Credential{Access: "workos:rejected", Refresh: "rejected-grant", ExpiresAt: time.Now().Add(time.Hour), AccountID: "identity"}
			a, err := sink.Persist(context.Background(), "cline", credential)
			if err != nil {
				t.Fatal(err)
			}
			sink.Register(a)
			ref, err := NewRefresher(RefresherOptions{File: file, Repos: map[account.ProviderID]*account.Repository{"cline": repo}, Pool: pool, Flows: map[account.ProviderID]Flow{"cline": NewClineFlow(Options{HTTP: server.Client()})}})
			if err != nil {
				t.Fatal(err)
			}
			lease, err := pool.Acquire(context.Background(), account.AcquireRequest{Provider: "cline"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = ref.RefreshRejected(context.Background(), lease, credential.Access)
			if !errors.Is(err, account.ErrNeedsReauth) {
				t.Fatalf("error=%v, want permanent credential failure", err)
			}
		})
	}
}
