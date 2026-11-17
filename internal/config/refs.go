package config

import "strings"

// RefKind names one kind of document field that holds a provider id. The
// Providers map key is not a RefKind: callers that move or drop a provider
// handle the entry itself.
type RefKind int

const (
	RefComboTarget    RefKind = iota // Combos[*].Targets[*].Provider, a bare id
	RefRouteKey                      // Routes key: "provider/model" or "claude-provider--model"
	RefRouteValue                    // Routes value: "provider/model"
	RefAliasKey                      // Aliases key, same shapes as RefRouteKey
	RefAliasValue                    // Aliases value: "provider/model"
	RefVisionTarget                  // VisionSidecar.Target: "provider/model"
	RefPinnedAccount                 // Providers[*].Pool.PinnedAccount: "provider:identity"
	RefCatalogAccount                // Providers[*].ModelCatalogs[*].Account: "provider:identity"
	RefAPIKeyRef                     // Providers[*].APIKeyRef: legacy "provider:default"
)

// walkProviderRefs is the one list of document fields that name a provider
// id. visit returns the field's new value and whether the field stays; a
// dropped route or alias loses its entry, a dropped combo target leaves its
// combo (and an emptied combo is removed), any other dropped field is
// cleared. A field added to Document that can name a provider belongs here;
// the sentinel test in rename_test.go fails until it is classified.
func walkProviderRefs(d *Document, visit func(kind RefKind, value string) (string, bool)) {
	for id, combo := range d.Combos {
		kept := make([]Target, 0, len(combo.Targets))
		for _, t := range combo.Targets {
			next, keep := visit(RefComboTarget, t.Provider)
			if !keep {
				continue
			}
			t.Provider = next
			kept = append(kept, t)
		}
		if len(kept) == 0 {
			delete(d.Combos, id)
			continue
		}
		combo.Targets = kept
		d.Combos[id] = combo
	}
	d.Routes = walkTargetMap(d.Routes, RefRouteKey, RefRouteValue, visit)
	d.Aliases = walkTargetMap(d.Aliases, RefAliasKey, RefAliasValue, visit)
	if d.VisionSidecar.Target != "" {
		next, keep := visit(RefVisionTarget, d.VisionSidecar.Target)
		if !keep {
			next = ""
		}
		d.VisionSidecar.Target = next
	}
	for id, p := range d.Providers {
		if p.APIKeyRef != "" {
			next, keep := visit(RefAPIKeyRef, p.APIKeyRef)
			if !keep {
				next = ""
			}
			p.APIKeyRef = next
		}
		if p.Pool != nil && p.Pool.PinnedAccount != "" {
			next, keep := visit(RefPinnedAccount, p.Pool.PinnedAccount)
			if !keep {
				next = ""
			}
			pool := *p.Pool
			pool.PinnedAccount = next
			p.Pool = &pool
		}
		if len(p.ModelCatalogs) > 0 {
			catalogs := make([]ModelCatalog, 0, len(p.ModelCatalogs))
			for _, c := range p.ModelCatalogs {
				next, keep := visit(RefCatalogAccount, c.Account)
				if !keep {
					continue
				}
				c.Account = next
				catalogs = append(catalogs, c)
			}
			p.ModelCatalogs = catalogs
		}
		d.Providers[id] = p
	}
}

func walkTargetMap(m map[string]string, keyKind, valueKind RefKind, visit func(RefKind, string) (string, bool)) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		nextKey, keepKey := visit(keyKind, k)
		nextValue, keepValue := visit(valueKind, v)
		if keepKey && keepValue {
			out[nextKey] = nextValue
		}
	}
	return out
}

// renameRef rewrites value from provider from to provider to when value of
// this kind names from. Only exact id boundaries match: "gw" never matches
// inside "gw2/model".
func renameRef(kind RefKind, value, from, to string) (string, bool) {
	switch kind {
	case RefComboTarget:
		if value == from {
			return to, true
		}
	case RefRouteKey, RefAliasKey:
		if rest, ok := strings.CutPrefix(value, from+"/"); ok {
			return to + "/" + rest, true
		}
		if rest, ok := strings.CutPrefix(value, claudeAliasStem(from)); ok && claudeAliasStem(from) != "" {
			return claudeAliasStem(to) + rest, true
		}
	case RefRouteValue, RefAliasValue, RefVisionTarget:
		if rest, ok := strings.CutPrefix(value, from+"/"); ok {
			return to + "/" + rest, true
		}
	case RefPinnedAccount, RefCatalogAccount:
		if rest, ok := strings.CutPrefix(value, from+":"); ok {
			return to + ":" + rest, true
		}
	case RefAPIKeyRef:
		if value == from+":default" {
			return to + ":default", true
		}
	}
	return value, false
}

// claudeAliasStem is the "claude-<provider>--" prefix the messages ingress
// uses for a provider's models, or "" when the id sanitizes to nothing.
func claudeAliasStem(provider string) string {
	p := Sanitize(provider)
	if p == "" {
		return ""
	}
	return ClaudeAliasPrefix + p + ClaudeSeparator
}

func refNames(kind RefKind, value, provider string) bool {
	_, ok := renameRef(kind, value, provider, provider)
	return ok
}
