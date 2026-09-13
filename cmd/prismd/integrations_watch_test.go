package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/integrations"
)

type stubWatchModule struct {
	id      integrations.ID
	refuse  bool
	mu      sync.Mutex
	applies int
}

func (s *stubWatchModule) ID() integrations.ID { return s.id }

func (s *stubWatchModule) Apply() integrations.ApplyResult {
	s.mu.Lock()
	s.applies++
	refuse := s.refuse
	s.mu.Unlock()
	if refuse {
		return integrations.ApplyResult{OK: false, ID: s.id, Reason: "prism: stub refuses"}
	}
	return integrations.ApplyResult{OK: true, ID: s.id}
}

func (s *stubWatchModule) Rollback() integrations.ApplyResult {
	return integrations.ApplyResult{OK: true, ID: s.id}
}

func (s *stubWatchModule) Status() integrations.Status {
	return integrations.Status{ID: s.id, Installed: true, Managed: true, Detail: "stub"}
}

func (s *stubWatchModule) applyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applies
}

func watchEnv(t *testing.T, doc config.Document) (*config.Manager, *integrations.Registry, *stubWatchModule) {
	t.Helper()
	mgr, err := config.Open(filepath.Join(t.TempDir(), "prism.json"))
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	if _, err := mgr.Update(doc, 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	registry := integrations.NewRegistry()
	stub := &stubWatchModule{id: integrations.Codex}
	if err := registry.Register(stub); err != nil {
		t.Fatal(err)
	}
	registry.SetEnabledSource(func() map[integrations.ID]bool {
		out := make(map[integrations.ID]bool)
		for id, settings := range mgr.Get().Config.Integrations {
			if settings.Enabled {
				out[integrations.ID(id)] = true
			}
		}
		return out
	})
	select {
	case <-mgr.Changes():
	default:
	}
	return mgr, registry, stub
}

func TestWatchIntegrationsAppliesConfigChange(t *testing.T) {
	mgr, registry, stub := watchEnv(t, config.Document{
		Version:      config.SchemaVersion,
		Integrations: map[string]config.IntegrationSettings{"codex": {Enabled: true}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchIntegrations(ctx, mgr, registry)
	waitFor(t, time.Second, func() bool { return stub.applyCount() >= 1 })

	doc := mgr.Get().Config
	doc.Integrations["grok"] = config.IntegrationSettings{Enabled: false}
	if _, err := mgr.Update(doc, mgr.Get().Generation); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return stub.applyCount() >= 2 })
}

func TestWatchIntegrationsSkipsDisabledAtStartup(t *testing.T) {
	mgr, registry, stub := watchEnv(t, config.Document{
		Version:      config.SchemaVersion,
		Integrations: map[string]config.IntegrationSettings{"codex": {Enabled: false}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		watchIntegrations(ctx, mgr, registry)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	if got := stub.applyCount(); got != 0 {
		t.Fatalf("disabled integration applied at startup: %d", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not exit after cancel")
	}
}

func TestWatchIntegrationsLogsRefusedApplyAndSurvives(t *testing.T) {
	mgr, registry, stub := watchEnv(t, config.Document{
		Version:      config.SchemaVersion,
		Integrations: map[string]config.IntegrationSettings{"codex": {Enabled: true}},
	})
	stub.refuse = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchIntegrations(ctx, mgr, registry)

	doc := mgr.Get().Config
	if _, err := mgr.Update(doc, mgr.Get().Generation); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return stub.applyCount() >= 2 })

	doc = mgr.Get().Config
	stub.refuse = false
	if _, err := mgr.Update(doc, mgr.Get().Generation); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return stub.applyCount() >= 3 })
}

func TestSaveDocNoChangeFiresNothing(t *testing.T) {
	env, mgr := testEnv(t, config.Document{
		Version:      config.SchemaVersion,
		Integrations: map[string]config.IntegrationSettings{"codex": {Enabled: true}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := integrations.NewRegistry()
	stub := &stubWatchModule{id: integrations.Codex}
	if err := registry.Register(stub); err != nil {
		t.Fatal(err)
	}
	registry.SetEnabledSource(func() map[integrations.ID]bool {
		out := make(map[integrations.ID]bool)
		for id, settings := range mgr.Get().Config.Integrations {
			if settings.Enabled {
				out[integrations.ID(id)] = true
			}
		}
		return out
	})
	select {
	case <-mgr.Changes():
	default:
	}
	go watchIntegrations(ctx, mgr, registry)
	waitFor(t, time.Second, func() bool { return stub.applyCount() >= 1 })

	snap := mgr.Get()
	env.saveDoc("edge", config.Provider{}, snap.Config, snap.Generation)
	select {
	case <-mgr.Changes():
		t.Fatal("no-op saveDoc notified a change")
	default:
	}
	time.Sleep(50 * time.Millisecond)
	if got := stub.applyCount(); got != 1 {
		t.Fatalf("no-op config write fired the watcher: %d applies", got)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within " + timeout.String())
}
