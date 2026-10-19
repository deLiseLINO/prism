package integrations

import (
	"encoding/json"
	"fmt"
)

func (c *ClaudeIntegration) recoveryRemovalTransform(raw string) func(string) ConfigTransform {
	return func(current string) ConfigTransform {
		// Run the supported-structure check even if no values can be attributed.
		checked := claudeTransform(c.port, c.currentModels(), true)(current)
		if checked.Refused != "" {
			return checked
		}
		var settings struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			return refusedTransform(fmt.Sprintf("prism: settings.json cannot be read: %v", err))
		}
		if settings.Env["ANTHROPIC_AUTH_TOKEN"] != prismApiKey {
			return refusedTransform("prism: cannot identify managed env values; reconnect instead")
		}
		keys := []string{"ANTHROPIC_AUTH_TOKEN"}
		for _, e := range ClaudeEnvEntries(c.port, c.currentModels()) {
			value := settings.Env[e.Key]
			if value == "" {
				continue
			}
			switch e.Key {
			case "ANTHROPIC_BASE_URL":
				if value == ClaudeBaseURL(c.port) {
					keys = append(keys, e.Key)
				}
			case "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY":
				if value == "1" {
					keys = append(keys, e.Key)
				}
			case "ANTHROPIC_AUTH_TOKEN":
			default:
				// Preserve model choices unless they match the current generated
				// value. A model-name prefix is not ownership evidence.
				if value == e.Value {
					keys = append(keys, e.Key)
				}
			}
		}
		result := RemoveJSONScalarKeys(current, "env", "settings.json", keys)
		if result.Kind != "written" {
			return refusedTransform(result.Reason)
		}
		return nextTransform(result.Next, result.Changed)
	}
}
