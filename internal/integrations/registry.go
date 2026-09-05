package integrations

import "sync"

const UnregisteredDetail = "integration is not registered yet"

type Module interface {
	ID() ID
	Apply() ApplyResult
	Status() Status
	Rollback() ApplyResult
}

type Registry struct {
	mu      sync.Mutex
	modules map[ID]Module
}

func NewRegistry() *Registry {
	return &Registry{modules: make(map[ID]Module)}
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
	r.mu.Lock()
	defer r.mu.Unlock()
	module, ok := r.modules[id]
	if !ok {
		return r.unregistered(id)
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

func (r *Registry) Status() []Status {
	out := make([]Status, 0, len(IDs))
	for _, id := range IDs {
		out = append(out, r.StatusOf(id))
	}
	return out
}

func (r *Registry) StatusOf(id ID) Status {
	module, ok := r.modules[id]
	if !ok {
		return Status{ID: id, Installed: false, Managed: false, TargetPath: nil, Endpoint: nil, Drift: false, Detail: UnregisteredDetail}
	}
	return module.Status()
}
