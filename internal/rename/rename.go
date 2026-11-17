// Package rename moves a provider's local data to its new id: credential
// directories, account metadata, and usage history. It follows the rename
// ledger in the config document, so the same code finishes a fresh rename,
// a rename interrupted by a crash, and a rename replayed from a synced
// machine. It never writes the config.
package rename

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/store"
	"github.com/deLiseLINO/prism/internal/usage"
)

// Stores are this machine's stores that hold provider ids.
type Stores struct {
	Creds *store.FileCredentialStore
	// AccountsPath is where provider's account metadata lives, honoring
	// pool.accountsPath.
	AccountsPath func(provider string, pool *config.PoolSettings) string
	// Usage may be nil when the usage store is unavailable.
	Usage usage.Store
}

// Result is the answer to a rename request. Plan is empty when the rename
// had already been applied.
type Result struct {
	Generation     uint64
	AlreadyApplied bool
	Plan           Plan
}

// IncompleteError reports a committed rename whose local data could not be
// moved yet. The daemon retries on every reconcile until it succeeds.
type IncompleteError struct {
	Generation uint64
	Cause      error
}

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("rename committed at generation %d, moving local data failed: %v", e.Generation, e.Cause)
}

func (e *IncompleteError) Unwrap() error { return e.Cause }

type WarningKind string

const (
	// WarningUsageMerge: the usage history already has rows for the new id
	// (a provider of that name existed before); both histories merge.
	WarningUsageMerge WarningKind = "usage_merge"
	// WarningClientIDs: model ids change, and the old ones stop resolving.
	WarningClientIDs WarningKind = "client_model_ids"
)

type Warning struct {
	Kind   WarningKind `json:"kind"`
	Detail string      `json:"detail"`
}

// AccountMove is the id change of one account.
type AccountMove struct {
	From account.AccountID
	To   account.AccountID
}

// Plan is what a rename touches on this machine.
type Plan struct {
	From          string
	To            string
	ConfigRefs    int
	Accounts      []AccountMove
	UsageAttempts int64
	Warnings      []Warning
}

// Preflight checks a rename against the document and this machine's
// stores without changing anything.
func Preflight(ctx context.Context, d config.Document, from, to string, s Stores) (Plan, config.RenameConflicts, error) {
	conflicts := config.CheckRename(d, from, to)
	plan := Plan{From: from, To: to}
	if len(conflicts) > 0 {
		return plan, conflicts, nil
	}
	p := d.Providers[from]
	plan.ConfigRefs = config.CountProviderRefs(d, from)
	moves, err := accountMoves(from, to, p, s)
	if err != nil {
		return plan, nil, err
	}
	plan.Accounts = moves
	for _, m := range moves {
		present, err := s.Creds.AccountPresent(account.ProviderID(to), m.To)
		if err != nil {
			return plan, nil, err
		}
		if present {
			conflicts = append(conflicts, config.RenameConflict{Kind: config.ConflictCredentialsOrphan, Detail: fmt.Sprintf("credentials for %s are left from an earlier provider %q; remove them or pick another id", m.To, to)})
		}
	}
	if src, dst := s.AccountsPath(from, p.Pool), s.AccountsPath(to, p.Pool); src != dst {
		ids, err := account.ReadMetaIDs(dst, account.ProviderID(to))
		if err != nil {
			return plan, nil, err
		}
		if len(ids) > 0 {
			conflicts = append(conflicts, config.RenameConflict{Kind: config.ConflictAccountsOrphan, Detail: fmt.Sprintf("%s already lists %d accounts of an earlier provider %q", dst, len(ids), to)})
		}
	}
	if s.Usage != nil {
		if plan.UsageAttempts, err = s.Usage.CountProvider(ctx, from); err != nil {
			return plan, nil, err
		}
		existing, err := s.Usage.CountProvider(ctx, to)
		if err != nil {
			return plan, nil, err
		}
		if existing > 0 {
			plan.Warnings = append(plan.Warnings, Warning{Kind: WarningUsageMerge, Detail: fmt.Sprintf("usage history already has %d attempts under %q; they merge with %q's history", existing, to, from)})
		}
	}
	plan.Warnings = append(plan.Warnings, Warning{Kind: WarningClientIDs, Detail: fmt.Sprintf("models move from %s/<model> to %s/<model>; integrations Prism manages are updated, client configs written by hand must switch to the new ids", from, to)})
	return plan, conflicts, nil
}

// Outcome is what Converge did for one ledger record.
type Outcome struct {
	Record config.ProviderRename
	Live   string
	Moved  bool
	Err    error
}

// Converge moves local data along every active ledger record of d, in
// ledger order. Each step is idempotent, so a record whose data already sits
// under its new id costs a few stat calls and two no-op updates. It stops at
// the first failing record: later records may chain on it.
func Converge(ctx context.Context, d config.Document, s Stores) []Outcome {
	var out []Outcome
	for _, r := range d.ActiveRenames() {
		moved, err := converge(ctx, r, d.Providers[r.Live], s)
		out = append(out, Outcome{Record: r.ProviderRename, Live: r.Live, Moved: moved, Err: err})
		if err != nil {
			break
		}
	}
	return out
}

func converge(ctx context.Context, r config.ActiveRename, p config.Provider, s Stores) (bool, error) {
	moves, err := accountMoves(r.From, r.To, p, s)
	if err != nil {
		return false, err
	}
	changed := false
	for _, m := range moves {
		moved, err := s.Creds.MoveAccount(ctx, account.ProviderID(r.From), m.From, account.ProviderID(r.To), m.To)
		if errors.Is(err, store.ErrMoveConflict) {
			return changed, config.RenameConflicts{{Kind: config.ConflictCredentialsOrphan, Detail: fmt.Sprintf("credentials for both %s and %s exist", m.From, m.To)}}
		}
		if err != nil {
			return changed, fmt.Errorf("move credentials of %s: %w", m.From, err)
		}
		changed = changed || moved
	}
	index := make(map[account.AccountID]account.AccountID, len(moves))
	for _, m := range moves {
		index[m.From] = m.To
	}
	moved, err := account.RenameMeta(s.AccountsPath(r.From, p.Pool), s.AccountsPath(r.To, p.Pool), account.ProviderID(r.From), account.ProviderID(r.To), index)
	if errors.Is(err, account.ErrMetaConflict) {
		return changed, config.RenameConflicts{{Kind: config.ConflictAccountsOrphan, Detail: err.Error()}}
	}
	if err != nil {
		return changed, err
	}
	changed = changed || moved
	if s.Usage != nil {
		prefixes := map[string]string{r.From + "/": r.To + "/"}
		if stem := config.ClaudeAliasStem(r.From); stem != "" {
			prefixes[stem] = config.ClaudeAliasStem(r.To)
		}
		n, err := s.Usage.RenameProvider(ctx, r.From, r.To, prefixes)
		if err != nil {
			return changed, err
		}
		changed = changed || n > 0
	}
	return changed, nil
}

// accountMoves lists the accounts of from by its metadata file, plus the
// API-key account every custom provider has. Ids of the form "<from>:x"
// become "<to>:x"; any other id keeps its text.
func accountMoves(from, to string, p config.Provider, s Stores) ([]AccountMove, error) {
	ids, err := account.ReadMetaIDs(s.AccountsPath(from, p.Pool), account.ProviderID(from))
	if err != nil {
		return nil, err
	}
	if def := account.AccountID(from + ":default"); p.Wire.Custom() && !slices.Contains(ids, def) {
		ids = append(ids, def)
	}
	out := make([]AccountMove, len(ids))
	for i, id := range ids {
		next := id
		if rest, ok := strings.CutPrefix(string(id), from+":"); ok {
			next = account.AccountID(to + ":" + rest)
		}
		out[i] = AccountMove{From: id, To: next}
	}
	return out, nil
}
