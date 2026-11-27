package hostsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/deLiseLINO/prism/internal/config"
)

// EditsPath is the host-side route that reports the host's own edits.
const EditsPath = "/api/v1/sync/edits"

// Base is the last bundle a host applied, with every API key replaced by its
// hash. A host measures its own edits against it. Account replicas are never
// edits, so the base leaves them out and no token reaches its file.
type Base struct {
	Document config.Document   `json:"document"`
	Secrets  map[string]string `json:"secrets,omitempty"`
}

// NewBase records bundle as the host's base.
func NewBase(bundle Bundle) Base {
	out := Base{Document: bundle.Document, Secrets: map[string]string{}}
	for id, secret := range bundle.Secrets {
		out.Secrets[id] = hashSecret([]byte(secret))
	}
	return out
}

// Edits are the shared entries a host changed since its base. A nil entry is
// one the host removed. Secrets carries the API key of every custom provider
// whose key the host set or changed. RejectedTokens is not a config edit: it
// reports replica access tokens an upstream refused on the host, so the main
// machine renews them.
type Edits struct {
	Providers      map[string]*config.Provider            `json:"providers,omitempty"`
	Combos         map[string]*config.Combo               `json:"combos,omitempty"`
	Routes         map[string]*string                     `json:"routes,omitempty"`
	Aliases        map[string]*string                     `json:"aliases,omitempty"`
	Integrations   map[string]*config.IntegrationSettings `json:"integrations,omitempty"`
	ContextWindow  *int                                   `json:"contextWindow,omitempty"`
	VisionSidecar  *config.VisionSidecarSettings          `json:"visionSidecar,omitempty"`
	Secrets        map[string]string                      `json:"secrets,omitempty"`
	RejectedTokens []RejectedToken                        `json:"rejectedTokens,omitempty"`
}

// Empty reports whether e changes no config entry.
func (e Edits) Empty() bool {
	return len(e.Providers) == 0 && len(e.Combos) == 0 && len(e.Routes) == 0 && len(e.Aliases) == 0 &&
		len(e.Integrations) == 0 && e.ContextWindow == nil && e.VisionSidecar == nil && len(e.Secrets) == 0
}

// Diff lists what local changed against base. Machine-local parts never
// count: a login provider's catalogs and pinned account, account paths, key
// references, and a login provider that exists only on the host. Removing a
// login provider is not an edit either, because the main machine would drop
// its own signed-in accounts with it.
func Diff(ctx context.Context, base Base, local config.Document, secrets SecretSource) (Edits, error) {
	out := Edits{Secrets: map[string]string{}}
	out.Providers = map[string]*config.Provider{}
	for id, mine := range local.Providers {
		was, inBase := base.Document.Providers[id]
		if !inBase && !mine.Wire.Custom() {
			continue
		}
		keyChanged := false
		if mine.Wire.Custom() && mine.APIKeyRef != "" {
			secret, present, err := secrets(ctx, id, mine.APIKeyRef)
			if err != nil {
				return Edits{}, fmt.Errorf("read API key of %s: %w", id, err)
			}
			if present && hashSecret(secret) != base.Secrets[id] {
				keyChanged = true
				out.Secrets[id] = string(secret)
			}
		}
		if inBase && !keyChanged && sameJSON(shared(mine), shared(was)) {
			continue
		}
		v := shared(mine)
		out.Providers[id] = &v
	}
	for id, was := range base.Document.Providers {
		if _, kept := local.Providers[id]; !kept && was.Wire.Custom() {
			out.Providers[id] = nil
		}
	}
	out.Combos = diffEntries(base.Document.Combos, local.Combos)
	out.Routes = diffEntries(base.Document.Routes, local.Routes)
	out.Aliases = diffEntries(base.Document.Aliases, local.Aliases)
	out.Integrations = diffEntries(base.Document.Integrations, local.Integrations)
	if local.ContextWindow != base.Document.ContextWindow {
		out.ContextWindow = &local.ContextWindow
	}
	if local.VisionSidecar != base.Document.VisionSidecar {
		out.VisionSidecar = &local.VisionSidecar
	}
	return out, nil
}

// Keep puts the host's pending edits back into merged, the result of laying
// incoming over local. An edit stays only where the main machine left that
// entry as it was in base; where the main machine changed it, its value wins.
// A kept provider keeps the machine-local parts merged chose for it, such as
// a login provider's pin and catalogs. The main machine takes the kept edits
// on its next pull.
func Keep(merged, local config.Document, base Base, incoming config.Document, edits Edits) config.Document {
	for id, edit := range edits.Providers {
		in, inIncoming := incoming.Providers[id]
		was, inBase := base.Document.Providers[id]
		if inIncoming != inBase || (inBase && !sameJSON(shared(in), shared(was))) {
			continue
		}
		if edit == nil {
			delete(merged.Providers, id)
			continue
		}
		if merged.Providers == nil {
			merged.Providers = map[string]config.Provider{}
		}
		mine := local.Providers[id]
		if pushed, ok := merged.Providers[id]; ok && pushed.Wire == mine.Wire {
			mine = adopt(mine, pushed)
		}
		merged.Providers[id] = mine
	}
	merged.Combos = keepEntries(merged.Combos, base.Document.Combos, incoming.Combos, edits.Combos)
	merged.Routes = keepEntries(merged.Routes, base.Document.Routes, incoming.Routes, edits.Routes)
	merged.Aliases = keepEntries(merged.Aliases, base.Document.Aliases, incoming.Aliases, edits.Aliases)
	merged.Integrations = keepEntries(merged.Integrations, base.Document.Integrations, incoming.Integrations, edits.Integrations)
	if edits.ContextWindow != nil && incoming.ContextWindow == base.Document.ContextWindow {
		merged.ContextWindow = *edits.ContextWindow
	}
	if edits.VisionSidecar != nil && incoming.VisionSidecar == base.Document.VisionSidecar {
		merged.VisionSidecar = *edits.VisionSidecar
	}
	return merged
}

// KeepsKey reports whether the host keeps its own API key for provider: the
// main machine pushed the same key it pushed last time, so a different key on
// the host is the host's own edit.
func KeepsKey(base *Base, provider, secret string) bool {
	if base == nil {
		return false
	}
	was, ok := base.Secrets[provider]
	return ok && was == hashSecret([]byte(secret))
}

// Apply lays a host's edits over doc on the main machine. The main machine
// keeps its own machine-local parts of every provider the host changed.
func Apply(doc config.Document, edits Edits) config.Document {
	doc.Providers = maps.Clone(doc.Providers)
	if doc.Providers == nil {
		doc.Providers = map[string]config.Provider{}
	}
	for id, edit := range edits.Providers {
		if edit == nil {
			delete(doc.Providers, id)
			continue
		}
		doc.Providers[id] = adopt(*edit, doc.Providers[id])
	}
	doc.Combos = applyEntries(doc.Combos, edits.Combos)
	doc.Routes = applyEntries(doc.Routes, edits.Routes)
	doc.Aliases = applyEntries(doc.Aliases, edits.Aliases)
	doc.Integrations = applyEntries(doc.Integrations, edits.Integrations)
	if edits.ContextWindow != nil {
		doc.ContextWindow = *edits.ContextWindow
	}
	if edits.VisionSidecar != nil {
		doc.VisionSidecar = *edits.VisionSidecar
	}
	return doc
}

// shared strips the machine-local parts of a provider.
func shared(p config.Provider) config.Provider {
	return adopt(p, config.Provider{Wire: p.Wire})
}

func diffEntries[V any](base, local map[string]V) map[string]*V {
	out := map[string]*V{}
	for id, v := range local {
		if was, ok := base[id]; ok && sameJSON(v, was) {
			continue
		}
		out[id] = &v
	}
	for id := range base {
		if _, ok := local[id]; !ok {
			out[id] = nil
		}
	}
	return out
}

// keepEntries lays over merged the edits whose entry incoming left as it was
// in base.
func keepEntries[V any](merged, base, incoming map[string]V, edits map[string]*V) map[string]V {
	kept := map[string]*V{}
	for id, edit := range edits {
		in, inIncoming := incoming[id]
		was, inBase := base[id]
		if inIncoming == inBase && (!inBase || sameJSON(in, was)) {
			kept[id] = edit
		}
	}
	return applyEntries(merged, kept)
}

// applyEntries returns a copy of doc with edits laid over it.
func applyEntries[V any](doc map[string]V, edits map[string]*V) map[string]V {
	if len(edits) == 0 {
		return doc
	}
	doc = maps.Clone(doc)
	if doc == nil {
		doc = map[string]V{}
	}
	for id, edit := range edits {
		if edit == nil {
			delete(doc, id)
			continue
		}
		doc[id] = *edit
	}
	return doc
}

func sameJSON(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

func hashSecret(secret []byte) string {
	sum := sha256.Sum256(secret)
	return hex.EncodeToString(sum[:])
}

// BaseFile keeps a host's Base in one file next to its config.
type BaseFile struct {
	path string
	mu   sync.Mutex
}

func NewBaseFile(path string) *BaseFile {
	return &BaseFile{path: path}
}

// Load returns the stored base; ok is false when the host never applied a
// bundle.
func (f *BaseFile) Load() (base Base, ok bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return Base{}, false, nil
	}
	if err != nil {
		return Base{}, false, err
	}
	if err := json.Unmarshal(raw, &base); err != nil {
		return Base{}, false, fmt.Errorf("sync base %s: %w", f.path, err)
	}
	return base, true, nil
}

// Save replaces the stored base. An identical base writes nothing.
func (f *BaseFile) Save(base Base) error {
	raw, err := json.Marshal(base)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if old, err := os.ReadFile(f.path); err == nil && bytes.Equal(old, raw) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), filepath.Base(f.path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}
