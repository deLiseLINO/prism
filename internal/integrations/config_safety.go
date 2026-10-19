package integrations

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// inspectConfig checks the structures that the integration's patcher can edit.
// It does not interpret unrelated client settings.
func inspectConfig(path string, text string) error {
	text = ApplyEol(text, EolLF)
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, ".toml") || base == "config":
		for _, fence := range []Fence{CodexFence, GrokFence} {
			if FindFencedRegion(text, fence).Kind == FencedOrphaned {
				return fmt.Errorf("%s: %s", path, DamagedFenceApply)
			}
		}
		lines := strings.Split(text, "\n")
		root := lines[:codexRootEnd(lines)]
		for _, key := range []struct {
			name  string
			key   *regexp.Regexp
			value *regexp.Regexp
		}{
			{"model_provider", codexRootProviderKeyRe, codexRootProviderValueRe},
			{"openai_base_url", codexRootBaseUrlKeyRe, codexRootBaseUrlValueRe},
			{"model_catalog_json", codexRootCatalogKeyRe, codexRootCatalogValueRe},
		} {
			count := 0
			for _, line := range root {
				if !key.key.MatchString(line) {
					continue
				}
				count++
				if !key.value.MatchString(line) {
					return fmt.Errorf("%s: root %s is in an unsupported form", path, key.name)
				}
			}
			if count > 1 {
				return fmt.Errorf("%s: multiple root %s keys", path, key.name)
			}
		}
	case strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml"):
		if _, reason := scanProviderLeaf(text, "prism", base, false); reason != "" {
			return fmt.Errorf("%s: %s", path, reason)
		}
	case base == "settings.json":
		_, env, reason := jsonContainer(text, "env", base)
		if reason != "" {
			return fmt.Errorf("%s: %s", path, reason)
		}
		for _, key := range claudeEnvKeyNames() {
			if m, ok := jsonFind(env, key); ok {
				var value string
				raw := text[m.value.start:m.value.end]
				if !strings.HasPrefix(raw, `"`) || json.Unmarshal([]byte(raw), &value) != nil {
					return fmt.Errorf("%s: env.%s is not a string", path, key)
				}
			}
		}
	case base == "models.json" || base == "opencode.json" || base == "opencode.jsonc":
		containers := []string{"providers"}
		if base != "models.json" {
			containers = append(containers, "provider")
		}
		for _, container := range containers {
			read := ReadJSONBlockLeaf(text, container, "prism", base, "baseUrl")
			if read.Kind == jsonLeafRefused {
				return fmt.Errorf("%s: %s", path, read.Reason)
			}
		}
	case base == "gateway-models.json" || base == "prism-catalog.json":
		root, err := parseJSONObject(text)
		if err != nil || root == nil || !json.Valid([]byte(text)) {
			return fmt.Errorf("%s: expected a JSON object", path)
		}
	}
	return nil
}
