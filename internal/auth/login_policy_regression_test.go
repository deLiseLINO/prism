package auth

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/store"
)

func TestDelayedLoginRegistrationPreservesCompletedUserPolicy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	file := store.NewFileCredentialStore(root)
	repo := account.OpenMeta(filepath.Join(root, "accounts.json"))
	pool := account.New()
	pool.SetPolicyWriter(repo.SavePolicy)
	sink := NewFileSink(file, map[account.ProviderID]*account.Repository{"codex": repo}, pool)
	first, err := sink.Persist(ctx, "codex", testCredential())
	if err != nil {
		t.Fatal(err)
	}
	sink.Register(first)
	saved, err := sink.Persist(ctx, "codex", testCredential())
	if err != nil {
		t.Fatal(err)
	}
	current := pool.Snapshot().Accounts[0]
	if err := pool.Pause(ctx, current.ID, current.Version); err != nil {
		t.Fatal(err)
	}
	current = pool.Snapshot().Accounts[0]
	if err := pool.UpdatePriority(ctx, current.ID, 31, current.Version); err != nil {
		t.Fatal(err)
	}
	if err := pool.AdvanceGeneration(current.ID, saved.CredGen); err != nil {
		t.Fatal(err)
	}
	before := pool.Snapshot().Accounts[0]
	sink.Register(saved)
	after := pool.Snapshot().Accounts[0]
	if after.State != account.Paused || after.Priority != 31 || after.Version != before.Version || after.CredGen != saved.CredGen {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}

func TestDelayedLoginRegistrationKeepsNewGenerationRejection(t *testing.T) {
	for _, rejectionBeforeLogin := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejectionBeforeLogin=%t", rejectionBeforeLogin), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			file := store.NewFileCredentialStore(root)
			repo := account.OpenMeta(filepath.Join(root, "accounts.json"))
			pool := account.New()
			pool.SetPolicyWriter(repo.SavePolicy)
			sink := NewFileSink(file, map[account.ProviderID]*account.Repository{"codex": repo}, pool)
			first, err := sink.Persist(ctx, "codex", testCredential())
			if err != nil {
				t.Fatal(err)
			}
			sink.Register(first)
			reject := func() {
				lease, err := pool.Acquire(ctx, account.AcquireRequest{Provider: "codex"})
				if err != nil {
					t.Fatal(err)
				}
				if err := pool.Record(ctx, lease, account.AuthRejected{}); err != nil {
					t.Fatal(err)
				}
			}
			if rejectionBeforeLogin {
				reject()
			}
			saved, err := sink.Persist(ctx, "codex", testCredential())
			if err != nil {
				t.Fatal(err)
			}
			if err := pool.AdvanceGeneration(first.ID, saved.CredGen); err != nil {
				t.Fatal(err)
			}
			if !rejectionBeforeLogin {
				reject()
			}
			before := pool.Snapshot().Accounts[0]
			sink.Register(saved)
			after := pool.Snapshot().Accounts[0]
			wantState := account.NeedsReauth
			if rejectionBeforeLogin {
				wantState = account.Active
			}
			if after.State != wantState || after.Version != before.Version || after.CredGen != saved.CredGen || after.InFlight != 0 {
				t.Fatalf("before=%+v after=%+v wantState=%v", before, after, wantState)
			}
		})
	}
}
