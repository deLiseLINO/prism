package integrations

import (
	"errors"
	"fmt"
	"path/filepath"
)

func recoveryRefusal(scope string, err error) WriteOutcome {
	outcome := WriteOutcome{Kind: OutcomeRefused, Reason: failureReason(scope, err)}
	var conflict *journalConflictError
	if errors.As(err, &conflict) {
		outcome.Retryable = true
		outcome.Conflict = ConflictJournal
	}
	return outcome
}

func inspectJournal(io FileIO, path string, inherited *restorationJournal) error {
	j, err := loadJournal(io, path)
	var conflict *journalConflictError
	if err != nil && !errors.As(err, &conflict) {
		return err
	}
	if j == nil && err == nil {
		j = inherited
	}
	return inspectJournalFiles(io, path, j, err)
}

func inspectJournalFiles(io FileIO, path string, j *restorationJournal, conflict error) error {
	paths := []string{path}
	var vectors [][]journalFile
	if j != nil {
		vectors = append([][]journalFile{j.Files}, j.History...)
		for _, files := range vectors {
			for _, f := range files {
				if !journalPathAllowed(path, f.Path) {
					conflict = journalConflict("restoration journal has an unsupported path; original bytes retained")
					continue
				}
				paths = append(paths, f.Path)
			}
		}
	}
	current := make(map[string]fileState, len(paths))
	for _, p := range paths {
		if _, exists := current[p]; exists {
			continue
		}
		state, err := readState(io, p)
		if err != nil {
			return err
		}
		current[p] = state
	}
	// Read the whole accepted inventory before classifying stored conflicts.
	for _, p := range paths {
		state := current[p]
		if state.Present {
			if err := inspectConfig(p, state.Text); err != nil {
				return err
			}
		}
	}
	for _, files := range vectors {
		for _, f := range files {
			if !journalPathAllowed(path, f.Path) {
				continue
			}
			states := []fileState{f.Original, f.Written}
			if f.Pending != nil {
				states = append(states, *f.Pending)
			}
			for _, state := range states {
				if !state.Present {
					if state.Text != "" {
						conflict = journalConflict("restoration journal has text for an absent file at %s", f.Path)
					}
					continue
				}
				if err := inspectConfig(f.Path, state.Text); err != nil {
					conflict = journalConflict("restoration journal contains unsupported config at %s; original bytes retained: %v", f.Path, err)
				}
			}
		}
	}
	if conflict != nil || j == nil {
		return conflict
	}
	if j.Retired && j.Restoring {
		return journalConflict("restoration journal has contradictory lifecycle; original bytes retained")
	}
	for _, f := range j.Files {
		observed := current[f.Path]
		if f.Pending != nil {
			if j.Retired || observed != f.Written && observed != *f.Pending {
				return journalConflict("restoration journal conflict at %s; original bytes retained", f.Path)
			}
		} else if !j.Retired {
			next, err := mergeJournalFile(f, observed)
			if err != nil {
				return journalConflict("restoration journal conflict at %s; original bytes retained", f.Path)
			}
			if next.Present {
				if err := inspectConfig(f.Path, next.Text); err != nil {
					return journalConflict("restoration journal restoration is unsupported at %s; original bytes retained: %v", f.Path, err)
				}
			}
		}
	}
	return nil
}

func journalStatus(io FileIO, path string, inherited *restorationJournal, status Status) Status {
	if status.Blocked {
		return status
	}
	err := inspectJournal(io, path, inherited)
	if err == nil {
		return status
	}
	outcome := recoveryRefusal("integration status", err)
	status.Detail = outcome.Reason
	status.Conflict = outcome.Conflict
	status.Blocked = outcome.Conflict == ""
	return status
}

func saveRecoveryBackup(io FileIO, path, text string) (string, error) {
	writer, ok := io.(durableBackupIO)
	if !ok {
		return "", fmt.Errorf("file transport cannot create exclusive recovery backups")
	}
	for n := 0; ; n++ {
		backup := path + ".bak"
		if n > 0 {
			backup = fmt.Sprintf("%s.bak.%d", path, n)
		}
		_, present, err := io.ReadText(backup)
		if err != nil {
			return "", err
		}
		if present {
			continue
		}
		if err := writer.CreateBackup(backup, text); err != nil {
			return "", fmt.Errorf("recovery backup at %s could not be verified: %w", backup, err)
		}
		observed, err := readState(io, backup)
		if err != nil {
			return "", fmt.Errorf("recovery backup at %s could not be verified: %w", backup, err)
		}
		if !observed.Present || observed.Text != text {
			return "", fmt.Errorf("recovery backup readback failed at %s", backup)
		}
		return backup, nil
	}
}

// The caller holds the target mutation lock. Old metadata is backed up before
// the active journal is replaced; a fresh journal also suppresses legacy import
// on retries, including when reconnect writes no new config bytes.
func recoverRestorationUnlocked(io FileIO, path string, legacy, sidePaths []string, transform func(string) ConfigTransform, side map[string]fileState, remove bool) (outcome WriteOutcome, err error) {
	paths := []string{restorationPath(path)}
	paths = append(paths, legacy...)
	metadataCount := len(paths)
	paths = append(paths, path)
	paths = append(paths, sidePaths...)
	states := make(map[string]fileState, len(paths))
	for _, p := range paths {
		if _, exists := states[p]; exists {
			continue
		}
		state, readErr := readState(io, p)
		if readErr != nil {
			return outcome, readErr
		}
		states[p] = state
	}
	for _, p := range paths[metadataCount:] {
		state := states[p]
		if state.Present {
			if err := inspectConfig(p, state.Text); err != nil {
				return outcome, err
			}
		}
	}
	target := states[path]
	planned := transform(ApplyEol(target.Text, EolLF))
	if planned.Refused != "" {
		return WriteOutcome{Kind: OutcomeRefused, Reason: planned.Refused}, nil
	}
	if planned.Changed {
		if err := inspectConfig(path, planned.Next); err != nil {
			return outcome, err
		}
	}
	for p, state := range side {
		if !journalPathAllowed(path, p) {
			return outcome, fmt.Errorf("unsupported integration side path %s", p)
		}
		if state.Present {
			if err := inspectConfig(p, state.Text); err != nil {
				return outcome, err
			}
		}
	}
	backups := []string{}
	defer func() {
		outcome.Backups = backups
		if len(backups) > 0 {
			outcome.Backup = backups[0]
		}
	}()
	for _, p := range paths {
		state := states[p]
		if !state.Present {
			continue
		}
		saved, saveErr := saveRecoveryBackup(io, p, state.Text)
		if saved != "" {
			backups = append(backups, saved)
		}
		if saveErr != nil {
			return outcome, saveErr
		}
	}
	for p, before := range states {
		observed, readErr := readState(io, p)
		if readErr != nil {
			return outcome, readErr
		}
		if observed != before {
			return outcome, journalConflict("file changed while saving recovery backups at %s", p)
		}
	}
	fresh := &restorationJournal{Version: 1, Target: filepath.Clean(path), CurrentBaseline: true}
	seen := map[string]bool{}
	for _, p := range paths[metadataCount:] {
		if seen[p] {
			continue
		}
		seen[p] = true
		state := states[p]
		original := state
		if remove && p == path && planned.Changed {
			original = fileState{Present: true, Text: ApplyEol(planned.Next, DominantEol(state.Text))}
		}
		fresh.Files = append(fresh.Files, journalFile{Path: p, Original: original, Written: state})
	}
	if err := publishJournal(io, fresh); err != nil {
		return outcome, err
	}
	for _, p := range legacy {
		before := states[p]
		if !before.Present {
			continue
		}
		observed, readErr := readState(io, p)
		if readErr != nil {
			return outcome, readErr
		}
		if observed != before {
			return outcome, journalConflict("legacy restoration metadata changed at %s", p)
		}
		if err := io.RemoveDurable(p); err != nil {
			return outcome, err
		}
		observed, readErr = readState(io, p)
		if readErr != nil {
			return outcome, readErr
		}
		if observed.Present {
			return outcome, fmt.Errorf("legacy restoration metadata removal failed at %s", p)
		}
	}
	if remove {
		return rollbackRestorationUnlocked(io, path, transform)
	}
	return applyRestorationUnlocked(io, path, transform, side, false, nil)
}

func withRecoveryBackups(outcome WriteOutcome, backups WriteOutcome, scope string, err error) WriteOutcome {
	if err != nil {
		outcome = recoveryRefusal(scope, err)
	}
	outcome.Backup, outcome.Backups = backups.Backup, backups.Backups
	if !outcomeOK(outcome) && outcome.Backup != "" {
		outcome.Reason += "; recovery backups retained at " + outcome.Backup
	}
	return outcome
}

func outcomeOK(outcome WriteOutcome) bool {
	return outcome.Kind == OutcomeWritten || outcome.Kind == OutcomeUnchanged
}
