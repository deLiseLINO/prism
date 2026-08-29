package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadGoldens(dir string) (map[string]Golden, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	goldens := map[string]Golden{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var g Golden
		if err := json.Unmarshal(raw, &g); err != nil {
			return nil, err
		}
		if g.CaseID == "" {
			return nil, fmt.Errorf("%s: golden missing caseId", e.Name())
		}
		goldens[g.CaseID] = g
	}
	return goldens, nil
}
