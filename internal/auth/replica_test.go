package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
)

func (h *harness) seedReplica(cred account.Credential) {
	h.t.Helper()
	gen, changed, err := PublishReplica(context.Background(), h.file, h.repo, Replica{Provider: h.provider, ID: h.acc, State: account.Active, Credential: &cred})
	if err != nil || gen != 1 || !changed {
		h.t.Fatalf("publish replica: gen %d changed %t err %v", gen, changed, err)
	}
	h.pool.Register(account.Account{ID: h.acc, Provider: h.provider, State: account.Active, Origin: account.OriginMain, CredGen: 1, Version: 1})
	h.lease = account.Lease{Provider: h.provider, Account: h.acc, CredGen: 1, Version: 1}
}

func TestAccessOnlyReplicaWaitsForTheMainMachine(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	cred := expiringCodex()
	cred.Refresh = ""
	cred.ExpiresAt = testNow().Add(time.Hour)
	h.seedReplica(cred)
	var reported []string
	h.ref.rejected = func(_ account.ProviderID, _ account.AccountID, access string) { reported = append(reported, access) }

	if _, err := h.ref.RefreshRejected(context.Background(), h.lease, "someone-else"); err != nil {
		t.Fatalf("a refusal of another token must return the current one: %v", err)
	}
	_, err := h.ref.RefreshRejected(context.Background(), h.lease, cred.Access)
	if !errors.Is(err, account.ErrAwaitingMain) || errors.Is(err, account.ErrNeedsReauth) {
		t.Fatalf("rejected replica err = %v, want ErrAwaitingMain", err)
	}
	if len(reported) != 1 || reported[0] != cred.Access {
		t.Fatalf("reported %v, want the refused token once", reported)
	}
	h.clock.Advance(2 * time.Hour)
	if _, err := h.ref.Credential(context.Background(), h.lease); !errors.Is(err, account.ErrAwaitingMain) {
		t.Fatalf("expired replica err = %v, want ErrAwaitingMain", err)
	}
	if h.tokens.requests() != 0 {
		t.Fatal("the host called the token endpoint for a replica")
	}
	if _, ok := h.blobAt(1); !ok {
		t.Fatal("the replica credential was deleted")
	}
	rows, err := h.repo.Load(h.provider)
	if err != nil || len(rows) != 1 || rows[0].State != account.Active || rows[0].Origin != account.OriginMain {
		t.Fatalf("replica row = %+v err %v", rows, err)
	}
}

func TestReplicaWithRefreshGrantRenewsLocallyButIsNeverTornDown(t *testing.T) {
	h := newRefreshHarness(t, "antigravity")
	h.seedReplica(expiringAntigravity())
	h.tokens.respond(http.StatusBadRequest, `{"error":"invalid_grant"}`)
	_, err := h.ref.Credential(context.Background(), h.lease)
	if !errors.Is(err, account.ErrAwaitingMain) {
		t.Fatalf("err = %v, want ErrAwaitingMain", err)
	}
	if h.tokens.requests() != 1 {
		t.Fatalf("token requests = %d, want the local renewal attempt", h.tokens.requests())
	}
	if _, ok := h.blobAt(1); !ok {
		t.Fatal("an invalid grant deleted the replica credential")
	}
	if rows, _ := h.repo.Load(h.provider); len(rows) != 1 || rows[0].State != account.Active {
		t.Fatalf("an invalid grant marked the replica for re-login: %+v", rows)
	}
	if pa := h.poolAccount(); pa.State != account.Active {
		t.Fatalf("pool state = %v, want active", pa.State)
	}
}

func TestPublishReplicaComparesCredentialContent(t *testing.T) {
	h := newRefreshHarness(t, "antigravity")
	cred := expiringAntigravity()
	h.seedReplica(cred)
	ctx := context.Background()
	publish := func(c account.Credential) (account.CredentialGeneration, bool) {
		t.Helper()
		gen, changed, err := PublishReplica(ctx, h.file, h.repo, Replica{Provider: h.provider, ID: h.acc, State: account.Active, Credential: &c})
		if err != nil {
			t.Fatal(err)
		}
		return gen, changed
	}
	if gen, changed := publish(cred); gen != 1 || changed {
		t.Fatalf("same credential: gen %d changed %t", gen, changed)
	}
	renewedHere := cred
	renewedHere.Access, renewedHere.ExpiresAt = "renewed-here", cred.ExpiresAt.Add(time.Hour)
	if gen, changed := publish(renewedHere); gen != 2 || !changed {
		t.Fatalf("newer credential: gen %d changed %t", gen, changed)
	}
	if gen, changed := publish(cred); gen != 2 || changed {
		t.Fatalf("an older copy of the same grant replaced a local renewal: gen %d changed %t", gen, changed)
	}
	relogin := account.Credential{Access: "relogin", Refresh: "new-grant", ExpiresAt: cred.ExpiresAt, AccountID: cred.AccountID}
	if gen, changed := publish(relogin); gen != 3 || !changed {
		t.Fatalf("a new login: gen %d changed %t", gen, changed)
	}
	if got, _ := h.blobAt(3); got.Access != "relogin" {
		t.Fatalf("stored %+v", got)
	}
}

func TestLocalLoginTakesTheReplicaOverAndSyncLeavesItAlone(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	replica := expiringCodex()
	replica.Refresh = ""
	h.seedReplica(replica)
	if _, err := h.sink.Persist(context.Background(), h.provider, expiringCodex()); err != nil {
		t.Fatal(err)
	}
	if replicaRow, _ := h.repo.IsReplica(h.provider, h.acc); replicaRow {
		t.Fatal("a local login left the account marked as a replica")
	}
	cred := replica
	cred.Access = "from-main"
	if _, _, err := PublishReplica(context.Background(), h.file, h.repo, Replica{Provider: h.provider, ID: h.acc, State: account.Active, Credential: &cred}); !errors.Is(err, account.ErrLocalAccount) {
		t.Fatalf("publish over a local login err = %v, want ErrLocalAccount", err)
	}
	if err := DeleteReplica(context.Background(), h.file, h.repo, h.provider, h.acc); !errors.Is(err, account.ErrLocalAccount) {
		t.Fatalf("delete of a local login err = %v, want ErrLocalAccount", err)
	}
	if got, _ := h.blobAt(2); got.Refresh != "old-refresh-grant" {
		t.Fatalf("local login credential = %+v", got)
	}
}
