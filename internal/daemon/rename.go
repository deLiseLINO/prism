package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/rename"
)

// converge moves local data along the rename ledger of doc and reports, by
// live provider id, the renames whose data could not be moved yet. Those
// providers stay unbuilt: building one would load an empty account list from
// its new paths. Callers hold renameMu.
func (e *daemonEnv) converge(ctx context.Context, doc config.Document) map[string]error {
	if e.renames.Creds == nil {
		return nil
	}
	var blocked map[string]error
	for _, o := range rename.Converge(ctx, doc, e.renames) {
		switch {
		case o.Err != nil:
			log.Printf("prism: rename %s -> %s: moving local data: %v", o.Record.From, o.Record.To, o.Err)
			if blocked == nil {
				blocked = map[string]error{}
			}
			blocked[o.Live] = o.Err
		case o.Moved:
			log.Printf("prism: rename %s -> %s: local data moved", o.Record.From, o.Record.To)
		}
	}
	return blocked
}

// dropProvider forgets every runtime structure of provider id. Requests in
// flight keep the runner they hold.
func (e *daemonEnv) dropProvider(id string) {
	providerID := account.ProviderID(id)
	e.registry.Remove(providerID)
	var dropped []account.AccountID
	for _, a := range e.pool.Snapshot().Accounts {
		if a.Provider == providerID {
			if err := e.pool.DeleteAccount(context.Background(), a.ID); err == nil {
				dropped = append(dropped, a.ID)
			}
		}
	}
	e.reposMu.Lock()
	delete(e.repos, providerID)
	e.reposMu.Unlock()
	delete(e.flows, providerID)
	delete(e.wires, providerID)
	delete(e.lastDiscover, providerID)
	if e.creds.publication != nil {
		e.creds.publication.mu.Lock()
		delete(e.creds.publication.refs, id)
		e.creds.publication.mu.Unlock()
	}
	if e.quotas != nil {
		e.quotas.mu.Lock()
		for _, a := range dropped {
			delete(e.quotas.lastProbe, a)
		}
		e.quotas.mu.Unlock()
	}
}

// providerRenamer renames providers on this machine: the config commit,
// the runtime cutover, and the move of local data.
type providerRenamer struct {
	env *daemonEnv
	// signingIn reports an open sign-in for a provider; nil without sign-in.
	signingIn func(account.ProviderID) bool
	busy      sync.Mutex
	now       func() time.Time
}

func newProviderRenamer(env *daemonEnv, signingIn func(account.ProviderID) bool) *providerRenamer {
	return &providerRenamer{env: env, signingIn: signingIn, now: time.Now}
}

// Rename renames provider from to to. A repeated request for a rename that
// is already in the document succeeds without a new commit, whatever its
// expected generation, and finishes any data move still pending.
func (r *providerRenamer) Rename(ctx context.Context, from, to string, expected uint64, dryRun bool) (rename.Result, error) {
	if !r.busy.TryLock() {
		return rename.Result{}, config.RenameConflicts{{Kind: config.ConflictRenameBusy, Detail: "another rename is running"}}
	}
	defer r.busy.Unlock()
	r.env.renameMu.Lock()
	defer r.env.renameMu.Unlock()
	snap := r.env.cfg.Get()
	if snap.Config.RenameApplied(from, to) {
		res := rename.Result{Generation: snap.Generation, AlreadyApplied: true, Plan: rename.Plan{From: from, To: to}}
		if dryRun {
			return res, nil
		}
		return res, r.finish(ctx, snap)
	}
	plan, conflicts, err := r.preflight(ctx, snap.Config, from, to)
	res := rename.Result{Generation: snap.Generation, Plan: plan}
	if err != nil {
		return res, err
	}
	if len(conflicts) > 0 {
		return res, conflicts
	}
	if dryRun {
		return res, nil
	}
	updated, err := r.commit(snap.Config, from, to, uuid.NewString(), r.now(), expected)
	if err != nil {
		return res, err
	}
	res.Generation = updated.Generation
	return res, r.finish(ctx, updated)
}

func (r *providerRenamer) preflight(ctx context.Context, doc config.Document, from, to string) (rename.Plan, config.RenameConflicts, error) {
	plan, conflicts, err := rename.Preflight(ctx, doc, from, to, r.env.renames)
	if err != nil {
		return plan, nil, err
	}
	if r.signingIn != nil && r.signingIn(account.ProviderID(from)) {
		conflicts = append(conflicts, config.RenameConflict{Kind: config.ConflictAuthPending, Detail: fmt.Sprintf("a sign-in for %q is open; finish or cancel it first", from)})
	}
	return plan, conflicts, nil
}

// commit writes the renamed document and drops the old id from the
// runtime. From here on the ledger record makes the rename finish.
func (r *providerRenamer) commit(doc config.Document, from, to, id string, at time.Time, expected uint64) (config.Snapshot, error) {
	next, _, err := config.RenameProvider(doc, from, to, id, at)
	if err != nil {
		return config.Snapshot{}, err
	}
	updated, err := r.env.cfg.Update(next, expected)
	if err != nil {
		return config.Snapshot{}, err
	}
	r.env.dropProvider(from)
	return updated, nil
}

// finish moves local data and builds the renamed providers.
func (r *providerRenamer) finish(ctx context.Context, snap config.Snapshot) error {
	blocked := r.env.converge(ctx, snap.Config)
	var errs []error
	for _, ar := range snap.Config.ActiveRenames() {
		if err := blocked[ar.Live]; err != nil {
			errs = append(errs, fmt.Errorf("provider %s: %w", ar.Live, err))
			continue
		}
		if err := r.env.ensureProvider(ctx, ar.Live, snap.Config.Providers[ar.Live]); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return &rename.IncompleteError{Generation: snap.Generation, Cause: err}
	}
	return nil
}
