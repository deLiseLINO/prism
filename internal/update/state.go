package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const stateFile = "update_state.json"

type State struct {
	LatestVersion    string    `json:"latest_version"`
	LastCheckedAt    time.Time `json:"last_checked_at"`
	DismissedVersion string    `json:"dismissed_version"`
}

func LoadState(dir string) State {
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		return State{}
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}
	}
	state.LatestVersion = strings.TrimSpace(state.LatestVersion)
	state.DismissedVersion = strings.TrimSpace(state.DismissedVersion)
	return state
}

func SaveState(dir string, state State) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if !state.LastCheckedAt.IsZero() {
		state.LastCheckedAt = state.LastCheckedAt.UTC()
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, stateFile+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(append(encoded, '\n')); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, stateFile)); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func DismissVersion(dir, version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return errors.New("empty version")
	}
	state := LoadState(dir)
	state.DismissedVersion = version
	if err := SaveState(dir, state); err != nil {
		return fmt.Errorf("failed to save update state: %w", err)
	}
	return nil
}
