package integrations

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

func managedCacheState(written, current fileState) bool {
	if !written.Present || !current.Present {
		return false
	}
	var w, c struct {
		BaseURL   string `json:"baseUrl"`
		Signature string `json:"signature"`
	}
	if json.Unmarshal([]byte(written.Text), &w) != nil || json.Unmarshal([]byte(current.Text), &c) != nil {
		return false
	}
	return w.BaseURL != "" && w.BaseURL == c.BaseURL && (c.Signature == w.Signature || c.Signature == "")
}

func mergeJournalFile(f journalFile, current fileState) (fileState, error) {
	if current == f.Written {
		return f.Original, nil
	}
	if filepath.Base(f.Path) == "gateway-models.json" && managedCacheState(f.Written, current) {
		return f.Original, nil
	}
	if base := filepath.Base(f.Path); base == "models.yml" || base == "models.yaml" {
		if adopted, ok := adoptWrittenLeaf(f.Written.Text, current.Text); ok {
			current.Text = adopted
		}
	}
	if FindFencedRegion(f.Written.Text, GrokFence).Kind == FencedFound && FindFencedRegion(current.Text, GrokFence).Kind == FencedFound {
		f.Written.Text, _ = removePrismLegacyTables(f.Written.Text, GrokFence)
		current.Text, _ = removePrismLegacyTables(current.Text, GrokFence)
	}
	if strings.HasSuffix(f.Path, ".toml") || filepath.Base(f.Path) == "config" {
		current.Text = restoreMissingMarkers(f.Written.Text, current.Text)
	}
	return mergeRestoration(f.Written, current, f.Original)
}

func restoreMissingMarkers(written, current string) string {
	if !strings.Contains(written, prismRoutingMarker) && !strings.Contains(written, prismCatalogMarker) {
		return current
	}
	if written == current {
		return current
	}
	lines := strings.Split(current, "\n")
	old := strings.Split(written, "\n")
	for i, line := range old {
		if line != prismRoutingMarker && line != prismCatalogMarker {
			continue
		}
		if i+1 >= len(old) {
			continue
		}
		for k, candidate := range lines {
			if candidate == old[i+1] && (k == 0 || lines[k-1] != line) {
				lines = append(lines, "")
				copy(lines[k+1:], lines[k:])
				lines[k] = line
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}
