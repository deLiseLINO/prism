package integrations

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

var mutationLocks [64]sync.Mutex

func lockMutation(path string) func() {
	var hash uint64 = 14695981039346656037
	for _, b := range []byte(filepath.Clean(path)) {
		hash = (hash ^ uint64(b)) * 1099511628211
	}
	lock := &mutationLocks[hash%uint64(len(mutationLocks))]
	lock.Lock()
	return lock.Unlock
}

type fileState struct {
	Present bool   `json:"present"`
	Text    string `json:"text"`
}

type journalFile struct {
	Path     string     `json:"path"`
	Original fileState  `json:"original"`
	Written  fileState  `json:"written"`
	Pending  *fileState `json:"pending,omitempty"`
}

type restorationJournal struct {
	Version   int             `json:"version"`
	Target    string          `json:"target"`
	Retired   bool            `json:"retired"`
	Restoring bool            `json:"restoring"`
	Files     []journalFile   `json:"files"`
	History   [][]journalFile `json:"history,omitempty"`
}

func restorationPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".prism-journal.json")
}

func readState(io FileIO, path string) (fileState, error) {
	text, present, err := io.ReadText(path)
	return fileState{Present: present, Text: text}, err
}

func loadJournal(io FileIO, path string) (*restorationJournal, error) {
	raw, present, err := io.ReadText(restorationPath(path))
	if err != nil || !present {
		return nil, err
	}
	root, parseErr := parseJSONObject(raw)
	if parseErr != nil || root == nil || !json.Valid([]byte(raw)) {
		return nil, fmt.Errorf("restoration journal invalid; originals retained")
	}
	var j restorationJournal
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&j) != nil || j.Version != 1 || j.Target != filepath.Clean(path) || len(j.Files) == 0 {
		return nil, fmt.Errorf("restoration journal invalid; original bytes retained at %s", restorationPath(path))
	}
	seen := make(map[string]bool, len(j.Files))
	for _, f := range j.Files {
		if !journalPathAllowed(path, f.Path) || seen[f.Path] || (!f.Original.Present && f.Original.Text != "") || (!f.Written.Present && f.Written.Text != "") || (f.Pending != nil && !f.Pending.Present && f.Pending.Text != "") {
			return nil, fmt.Errorf("restoration journal invalid at %s", restorationPath(path))
		}
		seen[f.Path] = true
	}
	return &j, nil
}

func journalPathAllowed(target, path string) bool {
	if path == target {
		return true
	}
	return path == CodexCatalogPath(target) || path == filepath.Join(filepath.Dir(target), "cache", "gateway-models.json")
}

func publishJournal(io FileIO, j *restorationJournal) error {
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	text := string(raw) + "\n"
	path := restorationPath(j.Target)
	if err = AtomicWrite(io, path, text); err != nil {
		return err
	}
	observed, err := readState(io, path)
	if err != nil {
		return err
	}
	if !observed.Present || observed.Text != text {
		return fmt.Errorf("restoration journal publication readback failed")
	}
	return nil
}

func writeState(io FileIO, path string, state fileState) error {
	var err error
	if state.Present {
		err = AtomicWrite(io, path, state.Text)
	} else {
		err = io.RemoveDurable(path)
	}
	if err != nil {
		return err
	}
	observed, err := readState(io, path)
	if err != nil {
		return err
	}
	if observed != state {
		return fmt.Errorf("file publication readback failed for %s", path)
	}
	return nil
}

func resumeJournal(io FileIO, j *restorationJournal) error {
	for i := range j.Files {
		f := &j.Files[i]
		if f.Pending == nil {
			continue
		}
		current, err := readState(io, f.Path)
		if err != nil {
			return err
		}
		if current != f.Written && current != *f.Pending {
			return fmt.Errorf("restoration journal conflict at %s; original bytes retained", f.Path)
		}
	}
	pending := false
	for _, f := range j.Files {
		pending = pending || f.Pending != nil
	}
	if !pending {
		return nil
	}
	for i := range j.Files {
		f := &j.Files[i]
		if f.Pending == nil {
			continue
		}
		if err := writeState(io, f.Path, *f.Pending); err != nil {
			return err
		}
		f.Written = *f.Pending
		f.Pending = nil
	}
	if j.Restoring {
		j.Retired = true
		j.Restoring = false
	}
	return publishJournal(io, j)
}

func applyRestoration(io FileIO, path string, transform func(string) ConfigTransform, side map[string]fileState, crash bool) (WriteOutcome, error) {
	return applyRestorationJournal(io, path, transform, side, crash, nil)
}

func applyRestorationJournal(io FileIO, path string, transform func(string) ConfigTransform, side map[string]fileState, crash bool, inherited *restorationJournal) (WriteOutcome, error) {
	unlock := lockMutation(path)
	defer unlock()
	return applyRestorationUnlocked(io, path, transform, side, crash, inherited)
}

func applyRestorationUnlocked(io FileIO, path string, transform func(string) ConfigTransform, side map[string]fileState, crash bool, inherited *restorationJournal) (WriteOutcome, error) {
	j, err := loadJournal(io, path)
	if err != nil {
		return WriteOutcome{}, err
	}
	if j == nil {
		j = inherited
	}
	paths := []string{path}
	for p := range side {
		paths = append(paths, p)
	}
	current := make(map[string]fileState, len(paths))
	for _, p := range paths {
		state, err := readState(io, p)
		if err != nil {
			return WriteOutcome{}, err
		}
		current[p] = state
	}
	if j != nil {
		for _, f := range j.Files {
			observed, err := readState(io, f.Path)
			if err != nil {
				return WriteOutcome{}, err
			}
			if !j.Retired && f.Pending == nil {
				if _, err := mergeJournalFile(f, observed); err != nil {
					return WriteOutcome{}, fmt.Errorf("restoration journal conflict at %s; original bytes retained", f.Path)
				}
			}
		}
		for _, f := range j.Files {
			if _, err := readState(io, f.Path); err != nil {
				return WriteOutcome{}, err
			}
		}
		if err = resumeJournal(io, j); err != nil {
			return WriteOutcome{}, err
		}
		for _, p := range paths {
			current[p], err = readState(io, p)
			if err != nil {
				return WriteOutcome{}, err
			}
		}
	}
	state := current[path]
	planned := transform(ApplyEol(state.Text, EolLF))
	if planned.Refused != "" {
		return WriteOutcome{Kind: OutcomeRefused, Reason: planned.Refused, Retryable: planned.Retryable}, nil
	}
	if j == nil {
		j = &restorationJournal{Version: 1, Target: filepath.Clean(path)}
	} else if j.Retired {
		history := append(j.History, j.Files)
		j = &restorationJournal{Version: 1, Target: filepath.Clean(path), History: history}
	}
	changes := make(map[string]fileState, len(side)+1)
	for p, s := range side {
		changes[p] = s
	}
	if planned.Changed {
		changes[path] = fileState{Present: true, Text: ApplyEol(planned.Next, DominantEol(state.Text))}
	}
	changed := false
	for _, p := range paths {
		next, exists := changes[p]
		if !exists {
			continue
		}
		index := -1
		for i := range j.Files {
			if j.Files[i].Path == p {
				index = i
				break
			}
		}
		if index < 0 {
			j.Files = append(j.Files, journalFile{Path: p, Original: current[p], Written: current[p]})
			index = len(j.Files) - 1
		}
		f := &j.Files[index]
		original, err := mergeJournalFile(*f, current[p])
		if err != nil {
			return WriteOutcome{}, fmt.Errorf("restoration journal conflict at %s: %w", p, err)
		}
		f.Original = original
		if p == path && current[p] != f.Written {
			if root, err := parseJSONObject(f.Written.Text); err == nil && root != nil {
				preserved, err := mergeJSONValues(f.Written.Text, current[p].Text, next.Text)
				if err != nil {
					return WriteOutcome{}, fmt.Errorf("restoration journal conflict at %s; settings retained", p)
				}
				next.Text = preserved
			}
		}
		if next != current[p] {
			f.Written = current[p]
			f.Pending = &next
			changed = true
		}
	}
	if !changed {
		return WriteOutcome{Kind: OutcomeUnchanged}, nil
	}
	if crash {
		if err = io.StageWrite(path, changes[path].Text); err != nil {
			return WriteOutcome{}, err
		}
		return WriteOutcome{Kind: OutcomeCrashed}, nil
	}
	if err = publishJournal(io, j); err != nil {
		return WriteOutcome{}, err
	}
	if err = resumeJournal(io, j); err != nil {
		return WriteOutcome{}, err
	}
	return WriteOutcome{Kind: OutcomeWritten}, nil
}

func rollbackRestoration(io FileIO, path string, fallback func(string) ConfigTransform) (WriteOutcome, error) {
	unlock := lockMutation(path)
	defer unlock()
	return rollbackRestorationUnlocked(io, path, fallback)
}

func rollbackRestorationUnlocked(io FileIO, path string, fallback func(string) ConfigTransform) (WriteOutcome, error) {
	j, err := loadJournal(io, path)
	if err != nil {
		return WriteOutcome{}, err
	}
	if j == nil {
		return applyConfigTransformUnlocked(io, path, fallback, false)
	}
	wasRetired := j.Retired
	for _, f := range j.Files {
		if _, err := readState(io, f.Path); err != nil {
			return WriteOutcome{}, err
		}
	}
	if err = resumeJournal(io, j); err != nil {
		return WriteOutcome{}, err
	}
	if j.Retired {
		if err = publishJournal(io, j); err != nil {
			return WriteOutcome{}, err
		}
		if wasRetired {
			return WriteOutcome{Kind: OutcomeUnchanged}, nil
		}
		return WriteOutcome{Kind: OutcomeWritten}, nil
	}
	for i := range j.Files {
		f := &j.Files[i]
		current, err := readState(io, f.Path)
		if err != nil {
			return WriteOutcome{}, err
		}
		next, err := mergeJournalFile(*f, current)
		if err != nil {
			return WriteOutcome{}, fmt.Errorf("restoration journal conflict at %s; original bytes retained", f.Path)
		}
		f.Written = current
		f.Pending = &next
	}
	j.Restoring = true
	if err = publishJournal(io, j); err != nil {
		return WriteOutcome{}, err
	}
	if err = resumeJournal(io, j); err != nil {
		return WriteOutcome{}, err
	}
	return WriteOutcome{Kind: OutcomeWritten}, nil
}

func recoverConfigStage(io FileIO, path string) bool {
	unlock := lockMutation(path)
	defer unlock()
	return io.RecoverStaged(path)
}

type lineChange struct {
	start, end int
	lines      []string
}

func lineChanges(before, after []string) []lineChange {
	width := len(after) + 1
	table := make([]int, (len(before)+1)*width)
	for i := len(before) - 1; i >= 0; i-- {
		for k := len(after) - 1; k >= 0; k-- {
			if before[i] == after[k] {
				table[i*width+k] = table[(i+1)*width+k+1] + 1
			} else {
				table[i*width+k] = max(table[(i+1)*width+k], table[i*width+k+1])
			}
		}
	}
	var out []lineChange
	i, k := 0, 0
	for i < len(before) || k < len(after) {
		if i < len(before) && k < len(after) && before[i] == after[k] {
			i++
			k++
			continue
		}
		change := lineChange{start: i}
		for i < len(before) || k < len(after) {
			if i < len(before) && k < len(after) && before[i] == after[k] {
				break
			}
			if k < len(after) && (i == len(before) || table[i*width+k+1] >= table[(i+1)*width+k]) {
				change.lines = append(change.lines, after[k])
				k++
			} else {
				i++
			}
		}
		change.end = i
		out = append(out, change)
	}
	return out
}

func mergeRestoration(written, current, original fileState) (fileState, error) {
	if current == written {
		return original, nil
	}
	if current == original {
		return original, nil
	}
	if !written.Present || !current.Present {
		return fileState{}, fmt.Errorf("written value changed")
	}
	if root, err := parseJSONObject(written.Text); err == nil && root != nil {
		next, err := mergeJSONValues(written.Text, current.Text, original.Text)
		if err != nil {
			return fileState{}, err
		}
		return fileState{Present: true, Text: next}, nil
	}
	baseLines := strings.Count(written.Text, "\n") + 1
	if baseLines > 1000000/(strings.Count(current.Text, "\n")+1) || baseLines > 1000000/(strings.Count(original.Text, "\n")+1) {
		return fileState{}, fmt.Errorf("restoration comparison exceeds 1000000 line pairs; original and current bytes retained")
	}
	base := strings.Split(written.Text, "\n")
	user := lineChanges(base, strings.Split(current.Text, "\n"))
	var wanted []string
	if original.Present {
		wanted = strings.Split(original.Text, "\n")
	}
	restore := lineChanges(base, wanted)
	for _, a := range user {
		for _, b := range restore {
			if a.start < b.end && b.start < a.end || a.start == a.end && a.start > b.start && a.start < b.end || b.start == b.end && b.start > a.start && b.start < a.end || a.start == b.start && (a.end == a.start) == (b.end == b.start) {
				return fileState{}, fmt.Errorf("written value changed")
			}
		}
	}
	var result []string
	i, u, r := 0, 0, 0
	for i <= len(base) {
		if u < len(user) && (r == len(restore) || user[u].start <= restore[r].start) && user[u].start == i {
			result = append(result, user[u].lines...)
			i = user[u].end
			u++
			continue
		}
		if r < len(restore) && restore[r].start == i {
			result = append(result, restore[r].lines...)
			i = restore[r].end
			r++
			continue
		}
		if i == len(base) {
			break
		}
		result = append(result, base[i])
		i++
	}
	return fileState{Present: true, Text: strings.Join(result, "\n")}, nil
}
