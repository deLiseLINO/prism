package auth

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/store"
)

type regPool struct {
	account.Pool
	mu         sync.Mutex
	registered []account.Account
}

func (p *regPool) Register(a account.Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registered = append(p.registered, a)
}

func (p *regPool) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.registered)
}

func testCredential() account.Credential {
	return account.Credential{
		Access:    "at",
		Refresh:   "rt",
		ExpiresAt: time.Unix(1_800_000_000, 0),
		AccountID: "acc-9",
		Email:     "u@e.co",
	}
}

func newTestSink(t *testing.T, provider account.ProviderID) (*FileSink, *store.FileCredentialStore, *account.Repository, *regPool, string) {
	t.Helper()
	root := t.TempDir()
	file := store.NewFileCredentialStore(root)
	repoPath := filepath.Join(root, "meta", string(provider)+".json")
	repo := account.OpenMeta(repoPath)
	pool := &regPool{Pool: account.New([]byte("0123456789abcdef0123456789abcdef"), time.Now)}
	sink := NewFileSink(file, map[account.ProviderID]*account.Repository{provider: repo}, pool)
	return sink, file, repo, pool, repoPath
}

func TestFileSinkPersistsCredentialAndMetadata(t *testing.T) {
	provider := account.ProviderID("codex")
	sink, file, repo, pool, repoPath := newTestSink(t, provider)
	cred := testCredential()
	acct, err := sink.Persist(context.Background(), provider, cred)
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	if acct.ID != "codex:acc-9" || acct.CredGen != 1 || acct.State != account.Active {
		t.Fatalf("account = %+v", acct)
	}
	if pool.count() != 0 {
		t.Fatal("pool registered before sink.Register")
	}
	sink.Register(acct)
	if pool.count() != 1 {
		t.Fatalf("registered = %d", pool.count())
	}
	blob, ok, err := file.Get(context.Background(), provider, acct.ID, 1)
	if err != nil || !ok {
		t.Fatalf("blob get: ok=%v err=%v", ok, err)
	}
	parsed, err := account.ParseCredential(blob)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Access != "at" || parsed.Refresh != "rt" || parsed.AccountID != "acc-9" || parsed.Email != "u@e.co" {
		t.Fatalf("parsed = %+v", parsed)
	}
	reloaded := account.OpenMeta(repoPath)
	rows, err := reloaded.Load(provider)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != acct.ID || rows[0].CredGen != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	gen, err := repo.CurrentGeneration(provider, acct.ID)
	if err != nil || gen != 1 {
		t.Fatalf("gen = %d err = %v", gen, err)
	}
}

func TestFileSinkAdvancesGenerationOnRelogin(t *testing.T) {
	provider := account.ProviderID("codex")
	sink, file, _, _, _ := newTestSink(t, provider)
	ctx := context.Background()
	first, err := sink.Persist(ctx, provider, testCredential())
	if err != nil {
		t.Fatalf("first persist: %v", err)
	}
	second := testCredential()
	second.Access = "at-2"
	second.ExpiresAt = time.Unix(1_900_000_000, 0)
	relogged, err := sink.Persist(ctx, provider, second)
	if err != nil {
		t.Fatalf("second persist: %v", err)
	}
	if first.ID != relogged.ID {
		t.Fatalf("row ids differ: %q vs %q", first.ID, relogged.ID)
	}
	if first.CredGen != 1 || relogged.CredGen != 2 {
		t.Fatalf("gens = %d then %d", first.CredGen, relogged.CredGen)
	}
	blob, ok, err := file.Get(ctx, provider, relogged.ID, 2)
	if err != nil || !ok {
		t.Fatalf("gen2 blob: ok=%v err=%v", ok, err)
	}
	parsed, _ := account.ParseCredential(blob)
	if parsed.Access != "at-2" {
		t.Fatalf("gen2 access = %q", parsed.Access)
	}
}

func TestFileSinkRefusesCredentialWithoutIdentity(t *testing.T) {
	provider := account.ProviderID("codex")
	sink, _, repo, pool, _ := newTestSink(t, provider)
	cred := testCredential()
	cred.AccountID = ""
	if _, err := sink.Persist(context.Background(), provider, cred); err == nil {
		t.Fatal("expected identity refusal")
	}
	if pool.count() != 0 {
		t.Fatal("pool registered despite refusal")
	}
	if _, err := repo.Load(provider); err != nil {
		t.Fatalf("repo load: %v", err)
	}
}

func TestFileSinkRequiresRepositoryForProvider(t *testing.T) {
	_, file, _, pool, _ := newTestSink(t, "codex")
	sink := NewFileSink(file, map[account.ProviderID]*account.Repository{}, pool)
	if _, err := sink.Persist(context.Background(), "antigravity", testCredential()); err == nil {
		t.Fatal("expected missing repository error")
	}
}

func TestFileSinkRegisteredRequiresDurableRow(t *testing.T) {
	provider := account.ProviderID("codex")
	sink, _, repo, pool, _ := newTestSink(t, provider)
	pool.Register(account.Account{ID: "codex:default", Provider: provider, State: account.Active})
	if sink.Registered(provider) {
		t.Fatal("runtime placeholder row must not count as authorized")
	}
	cred := testCredential()
	if _, err := sink.Persist(context.Background(), provider, cred); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if !sink.Registered(provider) {
		t.Fatal("durable metadata row not reported")
	}
	if sink.Registered("antigravity") {
		t.Fatal("wrong provider reported registered")
	}
	gen, err := repo.CurrentGeneration(provider, "codex:acc-9")
	if err != nil || gen != 1 {
		t.Fatalf("gen = %d err = %v", gen, err)
	}
}
