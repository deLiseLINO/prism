package integrations

import (
	"sync"
	"testing"
)

type stubModule struct {
	id      ID
	applies int
	refuse  bool
	mu      sync.Mutex
}

func (s *stubModule) ID() ID { return s.id }

func (s *stubModule) Apply() ApplyResult {
	s.mu.Lock()
	s.applies++
	refuse := s.refuse
	s.mu.Unlock()
	if refuse {
		return ApplyResult{OK: false, ID: s.id, Reason: "prism: stub refuses"}
	}
	return ApplyResult{OK: true, ID: s.id}
}

func (s *stubModule) Rollback() ApplyResult { return ApplyResult{OK: true, ID: s.id} }

func (s *stubModule) Status() Status {
	return Status{ID: s.id, Installed: true, Managed: true, Detail: "stub"}
}

func (s *stubModule) applyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applies
}

func stubRegistry(ids ...ID) *Registry {
	r := NewRegistry()
	for _, id := range ids {
		if err := r.Register(&stubModule{id: id}); err != nil {
			panic(err)
		}
	}
	return r
}

func TestApplyEnabledAppliesOnlyEnabledRegisteredModules(t *testing.T) {
	r := stubRegistry(Codex, Grok, Claude)
	r.SetEnabledSource(func() map[ID]bool {
		return map[ID]bool{Codex: true, Grok: true, Pi: true}
	})
	if refused := r.ApplyEnabled(); len(refused) != 0 {
		t.Fatalf("refused results: %+v", refused)
	}
	for _, status := range r.Status() {
		module := status.ID
		want := module == Codex || module == Grok
		got := status.Enabled
		if got != want {
			t.Errorf("%s enabled = %v, want %v", module, got, want)
		}
	}
}

func TestApplyEnabledCollectsRefusalsAndAppliesTheRest(t *testing.T) {
	r := NewRegistry()
	codex := &stubModule{id: Codex}
	grok := &stubModule{id: Grok, refuse: true}
	if err := r.Register(codex); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(grok); err != nil {
		t.Fatal(err)
	}
	r.SetEnabledSource(func() map[ID]bool { return map[ID]bool{Codex: true, Grok: true} })
	refused := r.ApplyEnabled()
	if len(refused) != 1 || refused[0].ID != Grok || refused[0].Reason != "prism: stub refuses" {
		t.Fatalf("refused: %+v", refused)
	}
	if codex.applyCount() != 1 || grok.applyCount() != 1 {
		t.Fatalf("apply counts: codex=%d grok=%d", codex.applyCount(), grok.applyCount())
	}
}

func TestApplyEnabledWithoutSourceNoOps(t *testing.T) {
	r := stubRegistry(Codex)
	if refused := r.ApplyEnabled(); refused != nil {
		t.Fatalf("nil source applied something: %+v", refused)
	}
	for _, status := range r.Status() {
		if status.Enabled {
			t.Fatalf("%s reported enabled without a source", status.ID)
		}
	}
}

func TestStatusOfOverlaysEnabledFromSource(t *testing.T) {
	r := stubRegistry(Codex, Grok, Claude)
	r.SetEnabledSource(func() map[ID]bool { return map[ID]bool{Codex: true} })
	if !r.StatusOf(Codex).Enabled {
		t.Fatal("codex not overlaid as enabled")
	}
	if r.StatusOf(Grok).Enabled {
		t.Fatal("grok overlaid as enabled without config")
	}
	if r.StatusOf(Pi).Enabled {
		t.Fatal("unregistered pi reported enabled")
	}
	if r.StatusOf(Pi).Detail != UnregisteredDetail {
		t.Fatalf("unregistered detail: %q", r.StatusOf(Pi).Detail)
	}
}

func TestStatusOfWithoutSourceReportsFalse(t *testing.T) {
	r := stubRegistry(Codex)
	st := r.StatusOf(Codex)
	if st.Enabled || !st.Installed || !st.Managed {
		t.Fatalf("status without source: %+v", st)
	}
}
