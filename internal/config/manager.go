package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/deLiseLINO/prism/internal/integrations"
	"github.com/deLiseLINO/prism/internal/store"
)

type Snapshot struct {
	Config     Document
	Generation uint64
}

type Manager struct {
	mu          sync.Mutex
	path        string
	snap        Snapshot
	configJSON  []byte
	subscribers []chan struct{}
	// diskProblem is the last disk read failure Current logged, so a broken
	// file is reported once instead of on every request.
	diskProblem string
}

type fileFormat struct {
	Version    int      `json:"version"`
	Generation uint64   `json:"generation"`
	Config     Document `json:"config"`
}

func Open(path string) (*Manager, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path = absolute
	m := &Manager{path: path}
	snap, err := loadSnapshot(path)
	if err != nil {
		return nil, err
	}
	m.snap = snap
	m.configJSON, err = json.Marshal(snap.Config)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func loadSnapshot(path string) (Snapshot, error) {
	snap, err := readSnapshot(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{Config: Document{Version: SchemaVersion}}, nil
	}
	return snap, err
}

// readSnapshot parses the file at path. Read failures come back as the
// *fs.PathError from the read; parse and validation failures do not.
func readSnapshot(path string) (Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var f fileFormat
	if err := json.Unmarshal(b, &f); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if previous, ok := f.Config.Integrations["opencode2"]; ok {
		f.Config.Integrations[string(integrations.Opencode)] = previous
		delete(f.Config.Integrations, "opencode2")
	}
	if err := f.Config.validate(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Config: f.Config, Generation: f.Generation}, nil
}

func (m *Manager) Get() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneSnapshot(m.snap)
}

// Current returns the config published on disk. A valid file that differs
// from memory, such as one edited outside prism, is adopted and announced to
// subscribers, so callers check credential references against the config
// that is actually published. A file that is missing, does not parse or
// fails validation keeps the last good snapshot. A file that cannot be read
// fails closed. Each disk problem is logged once.
func (m *Manager) Current() (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	disk, err := readSnapshot(m.path)
	if err != nil {
		missing := errors.Is(err, fs.ErrNotExist)
		var pathErr *fs.PathError
		if !missing && errors.As(err, &pathErr) {
			m.reportDiskLocked("refusing requests until it can be read", err)
			return Snapshot{}, err
		}
		if !missing || m.snap.Generation != 0 {
			m.reportDiskLocked("keeping the last good config", err)
		}
		return cloneSnapshot(m.snap), nil
	}
	m.diskProblem = ""
	adopted, err := m.adoptLocked(disk)
	if err != nil {
		return Snapshot{}, err
	}
	if adopted {
		log.Printf("prism: config: adopted %s changed outside prism (generation %d)", m.path, disk.Generation)
		return cloneSnapshot(m.snap), nil
	}
	disk.Config.catalog = m.snap.Config.catalog
	return disk, nil
}

// adoptLocked makes a valid disk snapshot the current one when it differs
// from memory and reports whether it did.
func (m *Manager) adoptLocked(disk Snapshot) (bool, error) {
	encoded, err := json.Marshal(disk.Config)
	if err != nil {
		return false, err
	}
	if disk.Generation == m.snap.Generation && bytes.Equal(encoded, m.configJSON) {
		return false, nil
	}
	disk.Config.catalog = m.snap.Config.catalog
	m.snap, m.configJSON = disk, encoded
	m.notifyChanged()
	return true, nil
}

func (m *Manager) reportDiskLocked(action string, err error) {
	if problem := err.Error(); problem != m.diskProblem {
		m.diskProblem = problem
		log.Printf("prism: config: %s, %s is unusable: %v", action, m.path, err)
	}
}

func (m *Manager) Update(next Document, expected uint64) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if expected != m.snap.Generation {
		return Snapshot{}, fmt.Errorf("%w: expected %d, current %d", ErrStaleGeneration, expected, m.snap.Generation)
	}
	if err := next.validate(); err != nil {
		return Snapshot{}, err
	}
	lock, err := store.NewFileCredentialStore(filepath.Dir(m.path)).AcquireRefreshLock(context.Background(), []byte("config:"+m.path))
	if err != nil {
		return Snapshot{}, err
	}
	defer lock.Release()
	disk, err := loadSnapshot(m.path)
	if err != nil {
		return Snapshot{}, err
	}
	adopted, err := m.adoptLocked(disk)
	if err != nil {
		return Snapshot{}, err
	}
	if adopted {
		return Snapshot{}, fmt.Errorf("%w: file changed at generation %d", ErrStaleGeneration, disk.Generation)
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return Snapshot{}, err
	}
	gen := m.snap.Generation + 1
	if err := writeAtomic(m.path, fileFormat{Version: SchemaVersion, Generation: gen, Config: next}); err != nil {
		return Snapshot{}, err
	}
	stored := cloneDocument(next)
	stored.catalog = m.snap.Config.catalog
	m.snap = Snapshot{Config: stored, Generation: gen}
	m.configJSON = encoded
	m.notifyChanged()
	return cloneSnapshot(m.snap), nil
}

func (m *Manager) SetCatalog(c catalogLookup) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap.Config.catalog = c
}

func (m *Manager) notifyChanged() {
	for _, ch := range m.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe returns a channel that receives one coalesced signal after any
// number of config changes. Each consumer subscribes once, so one reader
// never takes another reader's signal.
func (m *Manager) Subscribe() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan struct{}, 1)
	m.subscribers = append(m.subscribers, ch)
	return ch
}

func writeAtomic(path string, f fileFormat) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// Equivalent reports whether two documents persist to the same bytes. It
// compares normalized copies, so an empty map and a missing one are equal.
func Equivalent(a, b Document) bool {
	left, err := json.Marshal(cloneDocument(a))
	if err != nil {
		return false
	}
	right, err := json.Marshal(cloneDocument(b))
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

func cloneSnapshot(s Snapshot) Snapshot {
	return Snapshot{Config: cloneDocument(s.Config), Generation: s.Generation}
}

func cloneDocument(d Document) Document {
	out := Document{
		Version:         d.Version,
		Daemon:          d.Daemon,
		ContextWindow:   d.ContextWindow,
		Providers:       cloneMap(d.Providers),
		Combos:          cloneMap(d.Combos),
		Routes:          cloneMap(d.Routes),
		Aliases:         cloneMap(d.Aliases),
		Hosts:           cloneMap(d.Hosts),
		Integrations:    cloneMap(d.Integrations),
		VisionSidecar:   d.VisionSidecar,
		Experimental:    cloneMap(d.Experimental),
		ProviderRenames: append([]ProviderRename(nil), d.ProviderRenames...),
		catalog:         d.catalog,
	}
	for id, p := range d.Providers {
		p.FallbackBaseURLs = append([]string(nil), p.FallbackBaseURLs...)
		p.Models = append([]string(nil), p.Models...)
		p.DisabledModels = append([]string(nil), p.DisabledModels...)
		p.SyncedModels = append([]string(nil), p.SyncedModels...)
		p.Discovered = cloneDiscovered(p.Discovered)
		p.ModelSettings = cloneModelSettings(p.ModelSettings)
		p.ModelCatalogs = append([]ModelCatalog(nil), p.ModelCatalogs...)
		for i := range p.ModelCatalogs {
			p.ModelCatalogs[i].RawModels = append([]string(nil), p.ModelCatalogs[i].RawModels...)
		}
		if p.Enabled != nil {
			v := *p.Enabled
			p.Enabled = &v
		}
		if p.Pool != nil {
			pool := *p.Pool
			p.Pool = &pool
		}
		p.Wait = p.Wait.clone()
		out.Providers[id] = p
	}
	for id, c := range d.Combos {
		c.Targets = append([]Target(nil), c.Targets...)
		out.Combos[id] = c
	}
	for id, h := range d.Hosts {
		h.AutoSync = cloneBool(h.AutoSync)
		out.Hosts[id] = h
	}
	return out
}

func cloneMap[V any](m map[string]V) map[string]V {
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneModelSettings(m map[string]ModelSettings) map[string]ModelSettings {
	out := make(map[string]ModelSettings, len(m))
	for k, v := range m {
		if v.ReasoningEfforts != nil {
			v.ReasoningEfforts = append([]string(nil), v.ReasoningEfforts...)
		}
		v.ImageInput = cloneBool(v.ImageInput)
		out[k] = v
	}
	return out
}

func cloneDiscovered(m map[string]DiscoveredFacts) map[string]DiscoveredFacts {
	if m == nil {
		return nil
	}
	out := make(map[string]DiscoveredFacts, len(m))
	for k, v := range m {
		v.ContextWindow = cloneInt(v.ContextWindow)
		v.MaxOutput = cloneInt(v.MaxOutput)
		v.Image = cloneBool(v.Image)
		out[k] = v
	}
	return out
}

func cloneBool(v *bool) *bool {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func cloneInt(v *int) *int {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}
