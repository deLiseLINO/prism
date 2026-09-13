package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

type Snapshot struct {
	Config     Document
	Generation uint64
}

type Manager struct {
	mu     sync.Mutex
	path   string
	snap   Snapshot
	notify chan struct{}
}

type fileFormat struct {
	Version    int      `json:"version"`
	Generation uint64   `json:"generation"`
	Config     Document `json:"config"`
}

func Open(path string) (*Manager, error) {
	m := &Manager{path: path, notify: make(chan struct{}, 1)}
	snap, err := loadSnapshot(path)
	if err != nil {
		return nil, err
	}
	m.snap = snap
	return m, nil
}

func loadSnapshot(path string) (Snapshot, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{Config: Document{Version: SchemaVersion}}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	var f fileFormat
	if err := json.Unmarshal(b, &f); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if err := f.Config.validate(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Config: cloneDocument(f.Config), Generation: f.Generation}, nil
}

func readGeneration(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var f fileFormat
	if err := json.Unmarshal(b, &f); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return f.Generation, nil
}

func (m *Manager) Get() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneSnapshot(m.snap)
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
	if disk, err := readGeneration(m.path); err == nil && disk != m.snap.Generation {
		if snap, err := loadSnapshot(m.path); err == nil {
			m.snap = snap
			m.notifyChanged()
		}
		return Snapshot{}, fmt.Errorf("%w: file advanced to %d", ErrStaleGeneration, disk)
	}
	gen := m.snap.Generation + 1
	if err := writeAtomic(m.path, fileFormat{Version: SchemaVersion, Generation: gen, Config: next}); err != nil {
		return Snapshot{}, err
	}
	m.snap = Snapshot{Config: cloneDocument(next), Generation: gen}
	m.notifyChanged()
	return cloneSnapshot(m.snap), nil
}

func (m *Manager) notifyChanged() {
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

func (m *Manager) Changes() <-chan struct{} {
	return m.notify
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

func cloneSnapshot(s Snapshot) Snapshot {
	return Snapshot{Config: cloneDocument(s.Config), Generation: s.Generation}
}

func cloneDocument(d Document) Document {
	out := Document{
		Version:       d.Version,
		Daemon:        d.Daemon,
		ContextWindow: d.ContextWindow,
		Providers:     cloneMap(d.Providers),
		Combos:        cloneMap(d.Combos),
		Routes:        cloneMap(d.Routes),
		Aliases:       cloneMap(d.Aliases),
		Hosts:         cloneMap(d.Hosts),
		Integrations:  cloneMap(d.Integrations),
		VisionSidecar: d.VisionSidecar,
	}
	for id, p := range d.Providers {
		p.Models = append([]string(nil), p.Models...)
		p.DisabledModels = append([]string(nil), p.DisabledModels...)
		p.ModelSettings = cloneModelSettings(p.ModelSettings)
		if p.Enabled != nil {
			v := *p.Enabled
			p.Enabled = &v
		}
		if p.Pool != nil {
			pool := *p.Pool
			if pool.AutoSwitch != nil {
				v := *pool.AutoSwitch
				pool.AutoSwitch = &v
			}
			p.Pool = &pool
		}
		out.Providers[id] = p
	}
	for id, c := range d.Combos {
		c.Targets = append([]Target(nil), c.Targets...)
		out.Combos[id] = c
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
		out[k] = v
	}
	return out
}
