package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/config"
	"prism/internal/quota"
)

func boolPtr(v bool) *bool { return &v }

func TestCatalogExcludesDisabledProvidersAndModels(t *testing.T) {
	mgr, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	doc := config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"p1": {Wire: config.WireCodex, Models: []string{"m1", "m2"}, DisabledModels: []string{"m2"}},
			"p2": {Wire: config.WireCodex, Models: []string{"m3"}, Enabled: boolPtr(false)},
			"p3": {Wire: config.WireCodex, Models: []string{"m4"}},
		},
	}
	if _, err := mgr.Update(doc, 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	models, err := catalog{cfg: mgr}.Models(context.Background())
	if err != nil {
		t.Fatalf("catalog models: %v", err)
	}
	got := map[string]bool{}
	for _, m := range models {
		got[string(m.ID)] = true
	}
	want := map[string]bool{"p1/m1": true, "p3/m4": true}
	for id := range want {
		if !got[id] {
			t.Errorf("enabled model %q missing from catalog", id)
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("disabled target %q listed in catalog", id)
		}
	}
}

func TestQuotaTableMirrorsPoolAndSkipsWarningOnlySnapshots(t *testing.T) {
	pool := account.New([]byte("secret"), func() time.Time { return time.Unix(0, 0).UTC() })
	pool.Register(account.Account{ID: "codex:a", Provider: "codex", State: account.Active, CredGen: 1, Version: 1})
	table := newQuotaTable(pool, nil, nil)

	limit := int64(10000)
	snap := quota.Snapshot{Used: 4200, Limit: &limit, WindowEnd: time.Unix(1_800_000_000, 0), Source: quota.SourceHeader}
	table.record("codex:a", snap, nil)
	snap.Used = 9999
	limit = 1

	got, err := table.Quota(context.Background(), "codex:a")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if got.Used != 4200 || got.Limit == nil || *got.Limit != 10000 {
		t.Fatalf("recorded quota not deep-copied into the pool: %+v", got)
	}
	if got.WindowEnd.Unix() != 1_800_000_000 || got.Source != quota.SourceHeader {
		t.Fatalf("quota fields not mirrored: %+v", got)
	}

	table.record("codex:a", quota.Snapshot{Source: quota.SourceHeader}, []string{"unparseable header"})
	got, err = table.Quota(context.Background(), "codex:a")
	if err != nil {
		t.Fatalf("quota after warning-only record: %v", err)
	}
	if got.Used != 4200 || got.Limit == nil || *got.Limit != 10000 {
		t.Fatalf("warning-only snapshot clobbered stored quota: %+v", got)
	}
}

func TestQuotaTableUnknownAccountIsNotFound(t *testing.T) {
	pool := account.New([]byte("secret"), func() time.Time { return time.Unix(0, 0).UTC() })
	pool.Register(account.Account{ID: "codex:a", Provider: "codex", State: account.Active, CredGen: 1, Version: 1})
	table := newQuotaTable(pool, nil, nil)

	if _, err := table.Quota(context.Background(), "ghost"); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("ghost quota: got %v, want ErrNotFound", err)
	}

	table.record("codex:a", quota.Snapshot{Used: 5, Limit: func() *int64 { v := int64(10); return &v }(), Source: quota.SourceHeader}, nil)
	if err := pool.DeleteAccount(context.Background(), "codex:a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := table.Quota(context.Background(), "codex:a"); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("quota after delete: got %v, want ErrNotFound", err)
	}
}

type fakeRefresher struct {
	cred account.Credential
}

func (f fakeRefresher) Credential(context.Context, account.Lease) (account.Credential, error) {
	return f.cred, nil
}

func TestQuotaTableProbesAntigravityAccountLive(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:fetchAvailableModels" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"gemini-3.7-flash":{"quotaInfo":{"remainingPercentage":25}}}}`))
	}))
	server.Start()
	t.Cleanup(server.Close)

	mgr, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	if _, err := mgr.Update(config.Document{
		Version:   config.SchemaVersion,
		Providers: map[string]config.Provider{"ag": {Wire: config.WireAntigravity, BaseURL: server.URL, Models: []string{"gemini-3.7-flash"}}},
	}, 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	pool := account.New([]byte("secret"), func() time.Time { return time.Unix(0, 0).UTC() })
	pool.Register(account.Account{ID: "ag:default", Provider: "ag", State: account.Active, CredGen: 1, Version: 1})
	table := newQuotaTable(pool, mgr, server.Client())
	table.refresher = fakeRefresher{cred: account.Credential{Access: "tok", ProjectID: "proj-1"}}

	snap, err := table.Quota(context.Background(), "ag:default")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if snap.Used != 7500 || snap.Limit == nil || *snap.Limit != 10000 || snap.Source != quota.SourceEndpoint {
		t.Fatalf("probed quota = %+v, want 7500/10000 endpoint basis points", snap)
	}
	stored := pool.Snapshot().Accounts[0].Quota
	if stored.Used != 7500 || stored.Source != quota.SourceEndpoint {
		t.Fatalf("probe did not record into the pool: %+v", stored)
	}

	// The second read inside the TTL window must not re-probe: the server
	// answers with a different number and the stored snapshot must win.
	snapAgain, err := table.Quota(context.Background(), "ag:default")
	if err != nil {
		t.Fatalf("second quota: %v", err)
	}
	if snapAgain.Used != 7500 {
		t.Fatalf("second read = %+v, want the TTL-cached snapshot", snapAgain)
	}
}

func TestQuotaTableProbeFailureKeepsStoredSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	mgr, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	if _, err := mgr.Update(config.Document{
		Version:   config.SchemaVersion,
		Providers: map[string]config.Provider{"ag": {Wire: config.WireAntigravity, BaseURL: server.URL, Models: []string{"m"}}},
	}, 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	pool := account.New([]byte("secret"), func() time.Time { return time.Unix(0, 0).UTC() })
	pool.Register(account.Account{ID: "ag:default", Provider: "ag", State: account.Active, CredGen: 1, Version: 1})
	limit := int64(10000)
	if err := pool.UpdateQuota("ag:default", quota.Snapshot{Used: 1234, Limit: &limit, Source: quota.SourceHeader}); err != nil {
		t.Fatalf("seed stored quota: %v", err)
	}
	table := newQuotaTable(pool, mgr, server.Client())
	table.refresher = fakeRefresher{cred: account.Credential{Access: "tok", ProjectID: "p"}}

	snap, err := table.Quota(context.Background(), "ag:default")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if snap.Used != 1234 || snap.Source != quota.SourceHeader {
		t.Fatalf("failed probe must keep the stored snapshot: %+v", snap)
	}
}

func TestIntegrationModelsResolveContextWindows(t *testing.T) {
	dir := t.TempDir()
	m, err := config.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(config.Document{
		Version:       config.SchemaVersion,
		ContextWindow: 400000,
		Providers: map[string]config.Provider{
			"codex": {
				Wire:   config.WireCodex,
				Models: []string{"gpt-5.2", "gpt-5.2-codex"},
				ModelSettings: map[string]config.ModelSettings{
					"gpt-5.2": {ContextWindow: 200000, ImageInput: true},
				},
			},
			"ag": {Wire: config.WireAntigravity, Models: []string{"gemini-3-pro"}},
		},
	}, 0); err != nil {
		t.Fatal(err)
	}
	got := integrationModels(m)()
	want := map[string]int{
		"codex/gpt-5.2":       200000,
		"codex/gpt-5.2-codex": 400000,
		"ag/gemini-3-pro":     400000,
	}
	if len(got) != len(want) {
		t.Fatalf("models = %d, want %d", len(got), len(want))
	}
	imageWant := map[string]bool{"codex/gpt-5.2": true}
	for _, m := range got {
		if want[m.ID] != m.ContextWindow {
			t.Fatalf("%s contextWindow = %d, want %d", m.ID, m.ContextWindow, want[m.ID])
		}
		if m.ImageInput != imageWant[m.ID] {
			t.Fatalf("%s imageInput = %v", m.ID, m.ImageInput)
		}
	}
}
