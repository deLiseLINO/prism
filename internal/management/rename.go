package management

import (
	"context"
	"errors"
	"net/http"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/rename"
)

// ProviderRenamer renames providers on this machine. Replay applies the
// renames of a synced ledger that this machine has not applied yet.
type ProviderRenamer interface {
	Rename(ctx context.Context, from, to string, expected uint64, dryRun bool) (rename.Result, error)
	Replay(ctx context.Context, ledger []config.ProviderRename) error
}

func (s *Server) SetRenamer(r ProviderRenamer) {
	s.renamer = r
}

func (s *Server) providersRename(w http.ResponseWriter, r *http.Request) {
	if s.renamer == nil {
		writeError(w, http.StatusServiceUnavailable, "rename_unavailable", "provider rename is not available")
		return
	}
	body, ok := decodeJSON[ProviderRenameWrite](w, r)
	if !ok {
		return
	}
	from := r.PathValue("id")
	res, err := s.renamer.Rename(r.Context(), from, body.To, body.ExpectedGeneration, body.DryRun)
	if err != nil {
		writeRenameError(w, err, res.Generation)
		return
	}
	out := ProviderRenameResponse{Generation: res.Generation, From: from, To: body.To, DryRun: body.DryRun, AlreadyApplied: res.AlreadyApplied, Plan: RenamePlan{ConfigRefs: res.Plan.ConfigRefs, Accounts: []string{}, UsageAttempts: res.Plan.UsageAttempts}}
	for _, m := range res.Plan.Accounts {
		out.Plan.Accounts = append(out.Plan.Accounts, string(m.From))
	}
	for _, warn := range res.Plan.Warnings {
		out.Warnings = append(out.Warnings, RenameIssue{Kind: string(warn.Kind), Detail: warn.Detail})
	}
	if !body.DryRun {
		snap := s.cfg.Get()
		if p, ok := snap.Config.Providers[body.To]; ok {
			v, err := s.providerView(r.Context(), snap.Config, body.To, p)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", err.Error())
				return
			}
			out.Provider = &v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// writeRenameError answers a refused or unfinished rename. generation is
// the config generation the refusal was checked against.
func writeRenameError(w http.ResponseWriter, err error, generation uint64) {
	var conflicts config.RenameConflicts
	var incomplete *rename.IncompleteError
	switch {
	case errors.As(err, &conflicts):
		issues := make([]RenameIssue, len(conflicts))
		for i, c := range conflicts {
			issues[i] = RenameIssue{Kind: string(c.Kind), Detail: c.Detail}
		}
		writeJSON(w, http.StatusConflict, RenameConflictBody{Error: ErrorDetail{Code: "rename_conflict", Message: err.Error()}, Generation: generation, Conflicts: issues})
	case errors.As(err, &incomplete):
		writeJSON(w, http.StatusInternalServerError, RenameIncompleteBody{Error: ErrorDetail{Code: "rename_incomplete", Message: err.Error()}, Generation: incomplete.Generation})
	default:
		writeConfigError(w, err)
	}
}
