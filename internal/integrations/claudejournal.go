package integrations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type historicalEnvValue struct {
	Present bool   `json:"present"`
	Value   string `json:"value"`
}
type historicalEnvChange struct {
	Before  historicalEnvValue `json:"before"`
	Written string             `json:"written"`
}
type historicalEnvJournal struct {
	Version int                            `json:"version"`
	Target  string                         `json:"target"`
	Changes map[string]historicalEnvChange `json:"changes"`
	Cache   struct {
		BeforePresent    bool   `json:"beforePresent"`
		BeforeHash       string `json:"beforeHash"`
		WrittenSignature string `json:"writtenSignature"`
		WrittenEndpoint  string `json:"writtenEndpoint"`
	} `json:"cache"`
}

func (c *ClaudeIntegration) inheritVersionedJournal(raw string, cache, backup fileState, legacy string) (*restorationJournal, error) {
	var old historicalEnvJournal
	decoder := json.NewDecoder(strings.NewReader(legacy))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&old) != nil || old.Version != 2 || old.Target != filepath.Clean(c.paths()) || len(old.Changes) != len(claudeEnvKeyNames()) {
		return nil, fmt.Errorf("historical restoration journal invalid; originals retained")
	}
	next := raw
	for _, key := range claudeEnvKeyNames() {
		change, ok := old.Changes[key]
		if !ok || (!change.Before.Present && change.Before.Value != "") {
			return nil, fmt.Errorf("historical restoration journal incomplete")
		}
		_, env, reason := jsonContainer(next, "env", "settings.json")
		if reason != "" {
			return nil, fmt.Errorf("historical settings invalid")
		}
		m, present := jsonFind(env, key)
		var observed string
		if present && json.Unmarshal([]byte(next[m.value.start:m.value.end]), &observed) != nil {
			return nil, fmt.Errorf("historical written value invalid at %s", key)
		}
		before := present == change.Before.Present && (!present || observed == change.Before.Value)
		if !before && (!present || observed != change.Written) {
			return nil, fmt.Errorf("historical written value conflict at %s", key)
		}
		if change.Before.Present {
			patch := UpsertJSONScalarKeysForced(next, "env", "settings.json", []JSONScalarEntry{{Key: key, Value: change.Before.Value}}, true)
			if patch.Kind != "written" {
				return nil, fmt.Errorf("historical original invalid")
			}
			next = patch.Next
		} else {
			patch := RemoveJSONScalarKeys(next, "env", "settings.json", []string{key})
			if patch.Kind != "written" {
				return nil, fmt.Errorf("historical original invalid")
			}
			next = patch.Next
		}
	}
	if old.Cache.BeforePresent != backup.Present {
		return nil, fmt.Errorf("historical cache original unavailable")
	}
	if backup.Present {
		hash := sha256.Sum256([]byte(backup.Text))
		if hex.EncodeToString(hash[:]) != old.Cache.BeforeHash {
			return nil, fmt.Errorf("historical cache original hash mismatch")
		}
	}
	if cache != backup {
		var observed struct {
			BaseURL   string `json:"baseUrl"`
			Signature string `json:"signature"`
		}
		if !cache.Present || json.Unmarshal([]byte(cache.Text), &observed) != nil || observed.BaseURL != old.Cache.WrittenEndpoint || (observed.Signature != "" && observed.Signature != old.Cache.WrittenSignature) {
			return nil, fmt.Errorf("historical written cache conflict")
		}
	}
	return &restorationJournal{Version: 1, Target: filepath.Clean(c.paths()), Files: []journalFile{{Path: c.paths(), Original: fileState{Present: true, Text: next}, Written: fileState{Present: true, Text: raw}}, {Path: c.cachePath(), Original: backup, Written: cache}}}, nil
}
