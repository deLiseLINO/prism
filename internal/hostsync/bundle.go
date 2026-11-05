// Package hostsync keeps remote prism daemons on the main machine's config.
// The main daemon builds a Bundle from its document and API keys and pushes
// it to every installed host; the host merges it over its own document.
package hostsync

import (
	"context"
	"fmt"

	"github.com/deLiseLINO/prism/internal/config"
)

// ConfigPath is the host-side route that accepts a pushed Bundle.
const ConfigPath = "/api/v1/sync/config"

// Bundle is the portable part of the main machine's config. Document carries
// no credential references and no machine-local sections; Secrets maps a
// custom provider id to its API key.
type Bundle struct {
	Document config.Document   `json:"document"`
	Secrets  map[string]string `json:"secrets,omitempty"`
}

// ApplyResponse is the host's answer to a pushed Bundle.
type ApplyResponse struct {
	Changed    bool   `json:"changed"`
	Generation uint64 `json:"generation"`
}

// SecretSource reads a custom provider's API key by its credential reference.
type SecretSource func(ctx context.Context, provider, ref string) (secret []byte, present bool, err error)

// Portable builds the Bundle for doc. Hosts and the daemon listen address
// describe this machine only, so they stay behind. Login providers carry no
// API key: their accounts are signed in on each machine separately.
func Portable(ctx context.Context, doc config.Document, secrets SecretSource) (Bundle, error) {
	out := Bundle{Document: doc, Secrets: map[string]string{}}
	out.Document.Hosts = nil
	out.Document.Daemon = config.Daemon{}
	providers := make(map[string]config.Provider, len(doc.Providers))
	for id, p := range doc.Providers {
		if p.Wire.Custom() && p.APIKeyRef != "" {
			secret, present, err := secrets(ctx, id, p.APIKeyRef)
			if err != nil {
				return Bundle{}, fmt.Errorf("read API key of %s: %w", id, err)
			}
			if present {
				out.Secrets[id] = string(secret)
			}
		}
		p.APIKeyRef = ""
		providers[id] = p
	}
	out.Document.Providers = providers
	return out, nil
}

// Merge lays incoming over local. The host keeps its own daemon section and
// host list, and everything tied to its own signed-in accounts: login
// providers keep their model catalogs and pinned account, and a login
// provider that exists only on the host stays. Custom providers keep the
// host's credential reference; the caller replaces it when the bundle carries
// a key. Every other section comes from incoming.
func Merge(local, incoming config.Document) config.Document {
	out := incoming
	out.Daemon = local.Daemon
	out.Hosts = local.Hosts
	out.Providers = make(map[string]config.Provider, len(incoming.Providers))
	for id, p := range incoming.Providers {
		mine, had := local.Providers[id]
		if !had || mine.Wire != p.Wire {
			mine = config.Provider{}
		}
		if p.Wire.Custom() {
			p.APIKeyRef = mine.APIKeyRef
		} else {
			p.APIKeyRef = ""
			p.ModelCatalogs = mine.ModelCatalogs
		}
		p.Pool = mergePool(p.Pool, mine.Pool, !p.Wire.Custom())
		out.Providers[id] = p
	}
	for id, mine := range local.Providers {
		if _, synced := out.Providers[id]; !synced && !mine.Wire.Custom() {
			out.Providers[id] = mine
		}
	}
	return out
}

func mergePool(incoming, local *config.PoolSettings, login bool) *config.PoolSettings {
	var next config.PoolSettings
	if incoming != nil {
		next = *incoming
	}
	next.AccountsPath = ""
	if login {
		next.PinnedAccount = ""
	}
	if local != nil {
		next.AccountsPath = local.AccountsPath
		if login {
			next.PinnedAccount = local.PinnedAccount
		}
	}
	if next == (config.PoolSettings{}) {
		return nil
	}
	return &next
}
