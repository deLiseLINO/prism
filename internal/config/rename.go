package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/deLiseLINO/prism/internal/store"
)

// ProviderRename is one committed provider rename. The ledger of renames is
// append-only and travels with the document to synced hosts, so every
// machine moves its own local data (credentials, account metadata, usage
// history) to the new id, at startup or on the next reconcile after a crash.
type ProviderRename struct {
	ID   string    `json:"id"`
	From string    `json:"from"`
	To   string    `json:"to"`
	At   time.Time `json:"at"`
}

// ReservedLoginIDs are provider ids that sign-in recreates on demand, so a
// rename away from them would leave a fresh card behind and a rename onto
// them would collide with the next sign-in.
var ReservedLoginIDs = []string{string(WireCodex), string(WireAntigravity), string(WireCline)}

// ValidProviderID is the provider id grammar for new ids: ASCII letters,
// digits, '_' and '-'. ':' separates account ids and '/' separates models,
// and anything else would be rewritten when the id becomes a directory name.
func ValidProviderID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range []byte(id) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

type RenameConflictKind string

const (
	ConflictNotFound          RenameConflictKind = "not_found"
	ConflictInvalidID         RenameConflictKind = "invalid_id"
	ConflictSameID            RenameConflictKind = "same_id"
	ConflictProviderExists    RenameConflictKind = "provider_exists"
	ConflictDirCollision      RenameConflictKind = "dir_collision"
	ConflictReservedLogin     RenameConflictKind = "reserved_login_id"
	ConflictModelShadowed     RenameConflictKind = "model_shadowed"
	ConflictCredentialsOrphan RenameConflictKind = "credentials_orphan"
	ConflictAccountsOrphan    RenameConflictKind = "accounts_orphan"
	ConflictAuthPending       RenameConflictKind = "auth_in_progress"
	ConflictRenameBusy        RenameConflictKind = "rename_in_progress"
)

type RenameConflict struct {
	Kind   RenameConflictKind `json:"kind"`
	Detail string             `json:"detail"`
}

// RenameConflicts is the error a blocked rename reports. Nothing was changed.
type RenameConflicts []RenameConflict

func (c RenameConflicts) Error() string {
	parts := make([]string, len(c))
	for i, x := range c {
		parts[i] = x.Detail
	}
	return "rename blocked: " + strings.Join(parts, "; ")
}

// CheckRename lists the document-level reasons renaming from to to is
// refused. An empty result means the document allows the rename; local
// stores may still object (see package rename).
func CheckRename(d Document, from, to string) RenameConflicts {
	var out RenameConflicts
	add := func(kind RenameConflictKind, format string, args ...any) {
		out = append(out, RenameConflict{Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
	if _, ok := d.Providers[from]; !ok {
		add(ConflictNotFound, "provider %q not found", from)
		return out
	}
	if !ValidProviderID(to) {
		add(ConflictInvalidID, "provider id %q may use only letters, digits, '_' and '-'", to)
		return out
	}
	if strings.EqualFold(from, to) {
		add(ConflictSameID, "%q and %q differ only in letter case", from, to)
		return out
	}
	if slices.Contains(ReservedLoginIDs, from) {
		add(ConflictReservedLogin, "%q is a sign-in provider id and cannot be renamed", from)
	}
	if slices.Contains(ReservedLoginIDs, to) {
		add(ConflictReservedLogin, "%q is reserved for sign-in providers", to)
	}
	if _, ok := d.Providers[to]; ok {
		add(ConflictProviderExists, "provider %q already exists", to)
	} else {
		dir := strings.ToLower(store.SafeProviderDir(to))
		for _, id := range slices.Sorted(maps.Keys(d.Providers)) {
			if id != from && strings.ToLower(store.SafeProviderDir(id)) == dir {
				add(ConflictDirCollision, "provider %q would share the credential directory of %q", to, id)
			}
		}
	}
	shadowed := func(section, key string) {
		if strings.HasPrefix(key, to+"/") || strings.HasPrefix(key, claudeAliasStem(to)) {
			add(ConflictModelShadowed, "%s key %q already claims a model id of %q", section, key, to)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(d.Routes)) {
		shadowed("route", k)
	}
	for _, k := range slices.Sorted(maps.Keys(d.Aliases)) {
		shadowed("alias", k)
	}
	for _, k := range slices.Sorted(maps.Keys(d.Combos)) {
		shadowed("combo", k)
	}
	return out
}

// CountProviderRefs is how many document fields name provider id, counting
// its Providers entry.
func CountProviderRefs(d Document, id string) int {
	if _, ok := d.Providers[id]; !ok {
		return 0
	}
	n := 1
	scratch := cloneDocument(d)
	walkProviderRefs(&scratch, func(kind RefKind, value string) (string, bool) {
		if refNames(kind, value, id) {
			n++
		}
		return value, true
	})
	return n
}

// RenameProvider moves provider from to to, rewrites every field that names
// from, and appends the ledger record. The provider entry moves unchanged.
// Ledger records whose From is to are dropped: to is live again, so they no
// longer describe where any data lives.
func RenameProvider(d Document, from, to, id string, at time.Time) (Document, ProviderRename, error) {
	if conflicts := CheckRename(d, from, to); len(conflicts) > 0 {
		return Document{}, ProviderRename{}, conflicts
	}
	out := cloneDocument(d)
	p := out.Providers[from]
	delete(out.Providers, from)
	out.Providers[to] = p
	walkProviderRefs(&out, func(kind RefKind, value string) (string, bool) {
		next, _ := renameRef(kind, value, from, to)
		return next, true
	})
	rec := ProviderRename{ID: id, From: from, To: to, At: at.UTC()}
	out.ProviderRenames = slices.DeleteFunc(out.ProviderRenames, func(r ProviderRename) bool { return r.From == to })
	out.ProviderRenames = append(out.ProviderRenames, rec)
	if err := out.validate(); err != nil {
		return Document{}, ProviderRename{}, err
	}
	return out, rec, nil
}

// DeleteProvider removes provider id with the routes, aliases and combo
// targets that point at it. Route and alias keys that only look like its
// model ids stay: they are names clients send, not references.
func DeleteProvider(d Document, id string) Document {
	out := cloneDocument(d)
	delete(out.Providers, id)
	walkProviderRefs(&out, func(kind RefKind, value string) (string, bool) {
		switch kind {
		case RefComboTarget, RefRouteValue, RefAliasValue:
			return value, !refNames(kind, value, id)
		}
		return value, true
	})
	return out
}

// ActiveRename is a ledger record that still describes data to move on
// this machine: its From is not a live provider and following later
// records from its To reaches the live provider Live.
type ActiveRename struct {
	ProviderRename
	Live string
}

// ActiveRenames lists the effective ledger records in ledger order. A live
// provider named From (created again, or re-added by a sign-in) shadows its
// record, so the new provider's data is never pulled into the renamed one.
func (d Document) ActiveRenames() []ActiveRename {
	var out []ActiveRename
	for i, r := range d.ProviderRenames {
		if _, live := d.Providers[r.From]; live {
			continue
		}
		cur := r.To
		for _, later := range d.ProviderRenames[i+1:] {
			if _, live := d.Providers[cur]; live {
				break
			}
			if later.From == cur {
				cur = later.To
			}
		}
		if _, live := d.Providers[cur]; live {
			out = append(out, ActiveRename{ProviderRename: r, Live: cur})
		}
	}
	return out
}

// RenameApplied reports whether the document already shows from renamed to
// to: from is gone and the ledger leads from it to the live provider to.
func (d Document) RenameApplied(from, to string) bool {
	for _, r := range d.ActiveRenames() {
		if r.From == from && r.Live == to {
			return true
		}
	}
	return false
}

func (d Document) validateRenames() error {
	seen := make(map[string]bool, len(d.ProviderRenames))
	for i, r := range d.ProviderRenames {
		if r.ID == "" || r.From == "" || r.To == "" {
			return fmt.Errorf("%w: providerRenames[%d]", ErrEmptyField, i)
		}
		if r.From == r.To {
			return fmt.Errorf("%w: providerRenames[%d] renames %q to itself", ErrInvalidValue, i, r.From)
		}
		if seen[r.ID] {
			return fmt.Errorf("%w: providerRenames[%d] repeats id %q", ErrInvalidValue, i, r.ID)
		}
		seen[r.ID] = true
	}
	return nil
}
