package rename

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/store"
	"github.com/deLiseLINO/prism/internal/usage"
)

type fixture struct {
	root   string
	stores Stores
	usage  usage.Store
	doc    config.Document
}

func newFixture(t *testing.T, wire config.Wire) *fixture {
	t.Helper()
	root := t.TempDir()
	u, err := usage.Open(filepath.Join(root, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Close() })
	f := &fixture{root: root, usage: u}
	f.stores = Stores{
		Creds: store.NewFileCredentialStore(root),
		AccountsPath: func(provider string, pool *config.PoolSettings) string {
			if pool != nil && pool.AccountsPath != "" {
				return pool.AccountsPath
			}
			return filepath.Join(root, "accounts", provider+".json")
		},
		Usage: u,
	}
	p := config.Provider{Wire: wire, Models: []string{"m1"}}
	if wire.Custom() {
		p.BaseURL = "https://gw.example.test/v1"
	}
	f.doc = config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{"gw": p, "gw2": p}}
	return f
}

func (f *fixture) seedUsage(t *testing.T, id, model, provider string) {
	t.Helper()
	err := f.usage.Insert(context.Background(), usage.Record{RequestID: id, Timestamp: time.Unix(100, 0), Model: model, Protocol: "chat", Status: "completed", Attempts: []usage.Attempt{{Provider: provider, Model: "m1", Outcome: "completed"}}})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) renamed(t *testing.T) config.Document {
	t.Helper()
	next, _, err := config.RenameProvider(f.doc, "gw", "edge", "r1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func providerTotals(t *testing.T, u usage.Store) map[string]int64 {
	t.Helper()
	rows, err := u.ByProvider(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, r := range rows {
		out[r.Provider] = r.Requests
	}
	return out
}

func modelTotals(t *testing.T, u usage.Store) map[string]int64 {
	t.Helper()
	rows, err := u.ByModel(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, r := range rows {
		out[r.Model] = r.Requests
	}
	return out
}

func TestConvergeMovesCustomProviderDataAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, config.WireOpenAIChat)
	ref, err := f.stores.Creds.StageCustomDefaultSecret(ctx, "gw", "", []byte("sk-gw"))
	if err != nil {
		t.Fatal(err)
	}
	p := f.doc.Providers["gw"]
	p.APIKeyRef = ref
	f.doc.Providers["gw"] = p
	f.seedUsage(t, "r1", "gw/m1", "gw")
	f.seedUsage(t, "r2", "claude-gw--m1", "gw")
	f.seedUsage(t, "r3", "gw2/m1", "gw2")

	plan, conflicts, err := Preflight(ctx, f.doc, "gw", "edge", f.stores)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("preflight: %v %+v", err, conflicts)
	}
	if plan.UsageAttempts != 2 || len(plan.Accounts) != 1 || plan.Accounts[0] != (AccountMove{From: "gw:default", To: "edge:default"}) {
		t.Fatalf("plan = %+v", plan)
	}

	doc := f.renamed(t)
	out := Converge(ctx, doc, f.stores)
	if len(out) != 1 || out[0].Err != nil || !out[0].Moved {
		t.Fatalf("first converge = %+v", out)
	}
	secret, ok, err := f.stores.Creds.GetCustomDefaultSecret(ctx, "edge", ref)
	if err != nil || !ok || string(secret) != "sk-gw" {
		t.Fatalf("key under edge: %q %v %v", secret, ok, err)
	}
	if _, ok, _ := f.stores.Creds.GetCustomDefaultSecret(ctx, "gw", ref); ok {
		t.Fatal("key still under gw: moved keys must not be copies")
	}
	if got := providerTotals(t, f.usage); got["edge"] != 2 || got["gw"] != 0 || got["gw2"] != 1 {
		t.Fatalf("usage by provider = %v", got)
	}
	if got := modelTotals(t, f.usage); got["edge/m1"] != 1 || got["claude-edge--m1"] != 1 || got["gw2/m1"] != 1 || got["gw/m1"] != 0 {
		t.Fatalf("usage by model = %v", got)
	}

	again := Converge(ctx, doc, f.stores)
	if len(again) != 1 || again[0].Err != nil || again[0].Moved {
		t.Fatalf("second converge must be a no-op: %+v", again)
	}
}

func TestConvergeMovesSignedInAccountsWithTheirMetadata(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, config.WireCodex)
	repo := account.OpenMeta(f.stores.AccountsPath("gw", nil))
	for _, id := range []account.AccountID{"gw:alice", "gw:bob"} {
		if err := f.stores.Creds.Put(ctx, "gw", id, 1, []byte("token-"+id)); err != nil {
			t.Fatal(err)
		}
		if err := repo.CASLoginGeneration("gw", id, 0, 1, string(id)+"@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetState("gw", "gw:bob", account.Paused); err != nil {
		t.Fatal(err)
	}
	out := Converge(ctx, f.renamed(t), f.stores)
	if len(out) != 1 || out[0].Err != nil {
		t.Fatalf("converge = %+v", out)
	}
	rows, err := account.OpenMeta(f.stores.AccountsPath("edge", nil)).Load("edge")
	if err != nil || len(rows) != 2 {
		t.Fatalf("edge accounts = %+v, %v", rows, err)
	}
	for _, a := range rows {
		blob, ok, err := f.stores.Creds.Get(ctx, "edge", a.ID, a.CredGen)
		if err != nil || !ok || string(blob) != "token-gw:"+string(a.ID)[len("edge:"):] {
			t.Fatalf("credential of %s = %q %v %v", a.ID, blob, ok, err)
		}
		if a.ID == "edge:bob" && a.State != account.Paused {
			t.Fatalf("bob lost its paused state: %+v", a)
		}
	}
	if _, err := os.Stat(f.stores.AccountsPath("gw", nil)); !os.IsNotExist(err) {
		t.Fatalf("old metadata file kept: %v", err)
	}
}

func TestConvergeFinishesAfterACrashBetweenMetadataWriteAndRemoval(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, config.WireCodex)
	repo := account.OpenMeta(f.stores.AccountsPath("gw", nil))
	if err := f.stores.Creds.Put(ctx, "gw", "gw:alice", 1, []byte("t")); err != nil {
		t.Fatal(err)
	}
	if err := repo.CASLoginGeneration("gw", "gw:alice", 0, 1, ""); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(f.stores.AccountsPath("gw", nil))
	if err != nil {
		t.Fatal(err)
	}
	doc := f.renamed(t)
	if out := Converge(ctx, doc, f.stores); out[0].Err != nil {
		t.Fatal(out[0].Err)
	}
	// Put the source file back, as if the process died before removing it.
	if err := os.WriteFile(f.stores.AccountsPath("gw", nil), src, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := Converge(ctx, doc, f.stores); out[0].Err != nil {
		t.Fatalf("recovery: %v", out[0].Err)
	}
	if _, err := os.Stat(f.stores.AccountsPath("gw", nil)); !os.IsNotExist(err) {
		t.Fatalf("source metadata kept after recovery: %v", err)
	}
}

func TestPreflightReportsLeftoverDataOfTheTargetIDAndConvergeRefusesToMerge(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, config.WireOpenAIChat)
	if _, err := f.stores.Creds.StageCustomDefaultSecret(ctx, "gw", "", []byte("sk-gw")); err != nil {
		t.Fatal(err)
	}
	edgeRef, err := f.stores.Creds.StageCustomDefaultSecret(ctx, "edge", "", []byte("sk-old-edge"))
	if err != nil {
		t.Fatal(err)
	}
	_, conflicts, err := Preflight(ctx, f.doc, "gw", "edge", f.stores)
	if err != nil || len(conflicts) != 1 || conflicts[0].Kind != config.ConflictCredentialsOrphan {
		t.Fatalf("preflight = %+v, %v", conflicts, err)
	}
	out := Converge(ctx, f.renamed(t), f.stores)
	var blocked config.RenameConflicts
	if len(out) != 1 || !errors.As(out[0].Err, &blocked) {
		t.Fatalf("converge over leftover data = %+v", out)
	}
	if secret, _, _ := f.stores.Creds.GetCustomDefaultSecret(ctx, "edge", edgeRef); string(secret) != "sk-old-edge" {
		t.Fatalf("leftover edge key overwritten: %q", secret)
	}
}

func TestPreflightWarnsWhenUsageHistoryMerges(t *testing.T) {
	f := newFixture(t, config.WireOpenAIChat)
	f.seedUsage(t, "r1", "edge/m1", "edge")
	plan, conflicts, err := Preflight(context.Background(), f.doc, "gw", "edge", f.stores)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("%v %+v", err, conflicts)
	}
	kinds := map[WarningKind]bool{}
	for _, w := range plan.Warnings {
		kinds[w.Kind] = true
	}
	if !kinds[WarningUsageMerge] || !kinds[WarningClientIDs] {
		t.Fatalf("warnings = %+v", plan.Warnings)
	}
}

func TestConvergeSkipsRecordsShadowedByALiveProvider(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, config.WireOpenAIChat)
	doc := f.renamed(t)
	doc.Providers["gw"] = config.Provider{Wire: config.WireOpenAIChat, BaseURL: "https://new.example.test/v1"}
	newRef, err := f.stores.Creds.StageCustomDefaultSecret(ctx, "gw", "", []byte("sk-new-gw"))
	if err != nil {
		t.Fatal(err)
	}
	if out := Converge(ctx, doc, f.stores); len(out) != 0 {
		t.Fatalf("shadowed record converged: %+v", out)
	}
	if secret, ok, _ := f.stores.Creds.GetCustomDefaultSecret(ctx, "gw", newRef); !ok || string(secret) != "sk-new-gw" {
		t.Fatal("the new gw lost its key")
	}
}
