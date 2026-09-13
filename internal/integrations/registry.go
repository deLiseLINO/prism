package integrations

import "sync"

const UnregisteredDetail = "integration is not registered yet"

type Module interface {
	ID() ID
	Apply() ApplyResult
	Status() Status
	Rollback() ApplyResult
}

// ForcedModule is implemented by modules whose user-owned conflicts can be
// taken over after an explicit confirmation: fail-closed by default, the
// displaced bytes are journaled and restored by rollback.
type ForcedModule interface {
	Module
	ApplyForced() ApplyResult
}

type Registry struct {
	mu      sync.Mutex
	modules map[ID]Module
	enabled func() map[ID]bool
}

func NewRegistry() *Registry {
	return &Registry{modules: make(map[ID]Module)}
}

func (r *Registry) SetEnabledSource(src func() map[ID]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled = src
}

func (r *Registry) Register(module Module) error {
	if _, exists := r.modules[module.ID()]; exists {
		return &duplicateIntegrationError{id: module.ID()}
	}
	r.modules[module.ID()] = module
	return nil
}

type duplicateIntegrationError struct{ id ID }

func (e *duplicateIntegrationError) Error() string {
	return "prism: integration " + string(e.id) + " is already registered"
}

func (r *Registry) unregistered(id ID) ApplyResult {
	return ApplyResult{OK: false, ID: id, Reason: UnregisteredDetail}
}

// Apply serializes mutations across modules: overlapping apply/rollback calls
// for the same client take the registry lock for the whole staged write, so
// the sibling stage file never has two writers.
func (r *Registry) Apply(id ID) ApplyResult {
	return r.ApplyForced(id, false)
}

func (r *Registry) ApplyForced(id ID, force bool) ApplyResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	module, ok := r.modules[id]
	if !ok {
		return r.unregistered(id)
	}
	if force {
		if forced, ok := module.(ForcedModule); ok {
			return forced.ApplyForced()
		}
	}
	return module.Apply()
}

func (r *Registry) Rollback(id ID) ApplyResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	module, ok := r.modules[id]
	if !ok {
		return r.unregistered(id)
	}
	return module.Rollback()
}

// ApplyEnabled applies every enabled registered module under the registry
// mutex, mirroring Apply's serialization: overlapping callers never race a
// staged write. Returns only the non-OK results.
func (r *Registry) ApplyEnabled() []ApplyResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enabled == nil {
		return nil
	}
	enabled := r.enabled()
	var refused []ApplyResult
	for _, id := range IDs {
		if !enabled[id] {
			continue
		}
		module, ok := r.modules[id]
		if !ok {
			continue
		}
		if result := module.Apply(); !result.OK {
			refused = append(refused, result)
		}
	}
	return refused
}

func (r *Registry) Status() []Status {
	out := make([]Status, 0, len(IDs))
	for _, id := range IDs {
		out = append(out, r.StatusOf(id))
	}
	return out
}

func (r *Registry) StatusOf(id ID) Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	module, ok := r.modules[id]
	if !ok {
		return Status{ID: id, Installed: false, Managed: false, Enabled: false, TargetPath: nil, Endpoint: nil, Drift: false, Detail: UnregisteredDetail}
	}
	st := module.Status()
	if r.enabled != nil && r.enabled()[id] {
		st.Enabled = true
	}
	return st
}
