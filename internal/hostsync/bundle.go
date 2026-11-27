// Package hostsync keeps remote prism daemons on the main machine's config.
// The main daemon builds a Bundle from its document and API keys and pushes
// it to every installed host; the host merges it over its own document.
package hostsync

import (
	"context"
	"fmt"
	"slices"

	"github.com/deLiseLINO/prism/internal/config"
)

// ConfigPath is the host-side route that accepts a pushed Bundle.
const ConfigPath = "/api/v1/sync/config"

// Bundle is the portable part of the main machine's config. Document carries
// no credential references and no machine-local sections; Secrets maps a
// custom provider id to its API key. Accounts lists a replica of every login
// account the main machine signed in; a host older than the field ignores it.
type Bundle struct {
	Document config.Document   `json:"document"`
	Secrets  map[string]string `json:"secrets,omitempty"`
	Accounts []Replica         `json:"accounts,omitempty"`
}

// ApplyResponse is the host's answer to a pushed Bundle.
type ApplyResponse struct {
	Changed    bool   `json:"changed"`
	Generation uint64 `json:"generation"`
}

// SecretSource reads a custom provider's API key by its credential reference.
type SecretSource func(ctx context.Context, provider, ref string) (secret []byte, present bool, err error)

// Portable builds the Bundle for doc. Hosts, the daemon listen address and
// the experimental flags describe this machine only, so they stay behind.
// Login providers carry no API key; their accounts travel as replicas, which
// the caller adds.
func Portable(ctx context.Context, doc config.Document, secrets SecretSource) (Bundle, error) {
	out := Bundle{Document: doc, Secrets: map[string]string{}}
	out.Document.Hosts = nil
	out.Document.Daemon = config.Daemon{}
	out.Document.Experimental = nil
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

// Owned lists the accounts of a login provider signed in on the host itself,
// as opposed to the replicas the main machine pushes.
type Owned func(provider string) []string

// Merge lays incoming over local. The host keeps its own daemon section,
// host list and experimental flags, and a login provider that exists only on
// the host stays. A login provider takes the main machine's pinned account
// and the model catalogs of its replicas, since the main machine owns those
// accounts; the host keeps its own pin when it names an account signed in
// there, and its own catalogs for such accounts. On a single-account wire a
// local login replaces the replica, so the host keeps its pin there too.
// Custom providers keep the host's credential reference; the caller replaces
// it when the bundle carries a key. The rename ledger is the union of both,
// the host's records first: each machine keeps the time it applied a rename
// at. Every other section comes from incoming. A nil owned means the host
// signed nothing in itself.
func Merge(local, incoming config.Document, owned Owned) config.Document {
	out := incoming
	out.Daemon = local.Daemon
	out.Hosts = local.Hosts
	out.Experimental = local.Experimental
	out.ProviderRenames = mergeLedger(local.ProviderRenames, incoming.ProviderRenames)
	out.Providers = make(map[string]config.Provider, len(incoming.Providers))
	for id, p := range incoming.Providers {
		var own []string
		if owned != nil && !p.Wire.Custom() {
			own = owned(id)
		}
		out.Providers[id] = adoptPushed(p, local.Providers[id], own)
	}
	for id, mine := range local.Providers {
		if _, synced := out.Providers[id]; !synced && !mine.Wire.Custom() {
			out.Providers[id] = mine
		}
	}
	return out
}

// adoptPushed takes p from the main machine onto the host, where mine is the
// host's entry and owned the accounts the host signed in itself.
func adoptPushed(p, mine config.Provider, owned []string) config.Provider {
	if p.Wire.Custom() {
		return adopt(p, mine)
	}
	if mine.Wire != p.Wire {
		mine = config.Provider{}
	}
	localPin := ""
	if mine.Pool != nil {
		localPin = mine.Pool.PinnedAccount
	}
	keepPin := slices.Contains(owned, localPin) || p.Wire.SingleAccount() && len(owned) > 0
	var catalogs []config.ModelCatalog
	for _, c := range p.ModelCatalogs {
		if !slices.Contains(owned, c.Account) {
			catalogs = append(catalogs, c)
		}
	}
	for _, c := range mine.ModelCatalogs {
		if slices.Contains(owned, c.Account) {
			catalogs = append(catalogs, c)
		}
	}
	p.APIKeyRef = ""
	p.ModelCatalogs = catalogs
	p.Pool = mergePool(p.Pool, mine.Pool, true, keepPin)
	return p
}

// adopt takes p from another machine and keeps mine's machine-local parts:
// the key reference of a custom provider, the catalogs and pinned account of
// a login provider, and the accounts path.
func adopt(p, mine config.Provider) config.Provider {
	if mine.Wire != p.Wire {
		mine = config.Provider{}
	}
	if p.Wire.Custom() {
		p.APIKeyRef = mine.APIKeyRef
	} else {
		p.APIKeyRef = ""
		p.ModelCatalogs = mine.ModelCatalogs
	}
	p.Pool = mergePool(p.Pool, mine.Pool, !p.Wire.Custom(), true)
	return p
}

func mergeLedger(local, incoming []config.ProviderRename) []config.ProviderRename {
	out := slices.Clone(local)
	for _, r := range incoming {
		if !slices.ContainsFunc(local, func(x config.ProviderRename) bool { return x.ID == r.ID }) {
			out = append(out, r)
		}
	}
	return out
}

// mergePool keeps local's accounts path, and for a login provider whose pin
// stays local, local's pinned account.
func mergePool(incoming, local *config.PoolSettings, login, localPin bool) *config.PoolSettings {
	var next config.PoolSettings
	if incoming != nil {
		next = *incoming
	}
	next.AccountsPath = ""
	if login && localPin {
		next.PinnedAccount = ""
	}
	if local != nil {
		next.AccountsPath = local.AccountsPath
		if login && localPin {
			next.PinnedAccount = local.PinnedAccount
		}
	}
	if next == (config.PoolSettings{}) {
		return nil
	}
	return &next
}
