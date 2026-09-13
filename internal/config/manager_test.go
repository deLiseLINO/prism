package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenMissingPathStartsEmpty(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := m.Get()
	if s.Generation != 0 || s.Config.Version != SchemaVersion {
		t.Fatalf("unexpected snapshot: %+v", s)
	}
}

func TestFreshWriteBumpsGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	next := validDoc()
	got, err := m.Update(next, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != 1 {
		t.Fatalf("generation = %d, want 1", got.Generation)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := again.Get()
	if s.Generation != 1 || s.Config.Daemon.Listen != next.Daemon.Listen {
		t.Fatalf("reloaded snapshot mismatch: %+v", s)
	}
}

func TestStaleWriteReturnsConflictAndLeavesFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(validDoc(), 0); err != nil {
		t.Fatal(err)
	}
	next := validDoc()
	next.Daemon.Listen = "127.0.0.1:9999"
	if _, err := m.Update(next, 0); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("want ErrStaleGeneration, got %v", err)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := reloaded.Get()
	if s.Generation != 1 || s.Config.Daemon.Listen == "127.0.0.1:9999" {
		t.Fatalf("conflicting write mutated state: %+v", s)
	}
}

func TestAtomicPersistenceSurvivesSimulatedCrash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := validDoc()
	if _, err := m.Update(first, 0); err != nil {
		t.Fatal(err)
	}
	crashed := validDoc()
	crashed.Daemon.Listen = "127.0.0.1:1234"
	if _, err := m.Update(crashed, 1); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, "config.json.tmp-simulated-crash")
	if err := os.WriteFile(stray, []byte(`{"version":1,"generation":99,"conf`), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := recovered.Get()
	if s.Generation != 2 || s.Config.Daemon.Listen != crashed.Daemon.Listen {
		t.Fatalf("last complete document not readable: %+v", s)
	}
}

func TestFailedValidationKeepsLastCompleteDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(validDoc(), 0); err != nil {
		t.Fatal(err)
	}
	bad := validDoc()
	bad.Providers["broken"] = Provider{Wire: Wire("nonsense")}
	if _, err := m.Update(bad, 1); !errors.Is(err, ErrUnknownWire) {
		t.Fatalf("want ErrUnknownWire, got %v", err)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s := reloaded.Get(); s.Generation != 1 {
		t.Fatalf("generation = %d, want 1", s.Generation)
	}
}

func TestCorruptFileIsTypedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestGetReturnsIsolatedCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(validDoc(), 0); err != nil {
		t.Fatal(err)
	}
	s := m.Get()
	s.Config.Routes["mutated"] = "x"
	s.Config.Providers["mutated"] = Provider{Wire: WireCodex}
	s2 := m.Get()
	if _, ok := s2.Config.Routes["mutated"]; ok {
		t.Fatal("Get leaked internal routes map")
	}
	if _, ok := s2.Config.Providers["mutated"]; ok {
		t.Fatal("Get leaked internal providers map")
	}
}

func TestUpdatePersistsEverySection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := validDoc()
	if _, err := m.Update(d, 0); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{`"daemon"`, `"providers"`, `"combos"`, `"routes"`, `"aliases"`, `"generation"`} {
		if !strings.Contains(string(b), section) {
			t.Fatalf("persisted document missing %s", section)
		}
	}
}

func TestGetReturnsIsolatedIntegrationsMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := validDoc()
	d.Integrations = map[string]IntegrationSettings{"codex": {Enabled: true}, "grok": {Enabled: false}}
	if _, err := m.Update(d, 0); err != nil {
		t.Fatal(err)
	}
	s := m.Get()
	s.Config.Integrations["codex"] = IntegrationSettings{Enabled: false}
	s.Config.Integrations["intruder"] = IntegrationSettings{Enabled: true}
	s2 := m.Get()
	if got := s2.Config.Integrations["codex"]; !got.Enabled {
		t.Fatalf("cloneDocument leaked integrations map: %+v", s2.Config.Integrations)
	}
	if _, ok := s2.Config.Integrations["intruder"]; ok {
		t.Fatal("cloneDocument leaked added integrations key")
	}
	if got := s2.Config.Integrations["grok"]; got.Enabled {
		t.Fatalf("grok enabled state not cloned: %+v", s2.Config.Integrations)
	}
}

func TestChangesCoalescesBurstAndNeverBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(validDoc(), 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := m.Update(validDoc(), uint64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-m.Changes():
	default:
		t.Fatal("three updates left the channel empty")
	}
	select {
	case <-m.Changes():
		t.Fatal("buffered channel did not coalesce the burst")
	default:
	}
	done := make(chan struct{})
	go func() {
		m.Update(validDoc(), 3)
		m.Update(validDoc(), 4)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Update blocked on a full notify channel")
	}
}

func TestChangesSignalsAfterAdoptedDiskReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(validDoc(), 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.Changes():
	default:
	}

	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	foreign := validDoc()
	foreign.Daemon.Listen = "127.0.0.1:4242"
	if _, err := other.Update(foreign, 1); err != nil {
		t.Fatal(err)
	}

	next := validDoc()
	next.Daemon.Listen = "127.0.0.1:9999"
	if _, err := m.Update(next, 1); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("want ErrStaleGeneration, got %v", err)
	}
	select {
	case <-m.Changes():
	default:
		t.Fatal("adopted disk reload left the change channel empty")
	}
	if got := m.Get().Config.Daemon.Listen; got != "127.0.0.1:4242" {
		t.Fatalf("adopted snapshot: %q", got)
	}
}
