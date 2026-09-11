package conformance

import (
	"encoding/json"
	"fmt"

	"prism/internal/canon"
)

func VectorToRequest(vector map[string]any) canon.Request {
	var req canon.Request
	if m, ok := vector["modelId"].(string); ok {
		req.Model = canon.ModelID(m)
	} else {
		req.Model = "fixture-model"
	}
	if s, ok := vector["stream"].(bool); ok {
		req.Stream = s
	}
	mapped := false
	if c, ok := vector["context"].(map[string]any); ok {
		if sp, ok := c["systemPrompt"].([]any); ok && len(sp) > 0 {
			if s, ok := sp[0].(string); ok {
				req.Instructions = []canon.Content{canon.TextContent{Text: s}}
			}
		}
		if msgs, ok := c["messages"].([]any); ok {
			mapped = true
			for _, mv := range msgs {
				m, ok := mv.(map[string]any)
				if !ok {
					continue
				}
				role, _ := m["role"].(string)
				content := contentFrom(m["content"])
				if content != nil {
					req.Input = append(req.Input, canon.Message{Role: roleFrom(role), Content: content})
				}
			}
		}
	}
	if !mapped {
		input := "PING"
		if v, ok := vector["input"].(string); ok {
			input = v
		}
		req.Input = []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: input}}}}
	}
	if s, ok := vector["requested"].(string); ok {
		if e, ok := effortFrom(s); ok {
			req.Reasoning.Effort = e
		}
	}
	if o, ok := vector["options"].(map[string]any); ok {
		if t, ok := o["temperature"].(float64); ok {
			req.Sampling.Temperature = &t
		}
		if tf, ok := o["textFormat"].(map[string]any); ok {
			req.Text.Format = textFormatFrom(tf)
		}
	}
	if rawTools, ok := vector["tools"].([]any); ok {
		if tools, err := vectorTools(rawTools); err == nil {
			req.Tools = tools
		}
	}
	if rawChoice, ok := vector["tool_choice"]; ok {
		if tc, ok := toolChoiceFromVector(rawChoice); ok {
			req.ToolChoice = tc
		}
	}
	return req
}

func effortFrom(label string) (canon.ReasoningEffort, bool) {
	switch label {
	case "minimal":
		return canon.EffortMinimal, true
	case "low":
		return canon.EffortLow, true
	case "medium":
		return canon.EffortMedium, true
	case "high":
		return canon.EffortHigh, true
	case "xhigh":
		return canon.EffortXHigh, true
	case "max":
		return canon.EffortMax, true
	default:
		return 0, false
	}
}

func vectorTools(raw []any) ([]canon.Tool, error) {
	var out []canon.Tool
	for _, rv := range raw {
		m, ok := rv.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tools entry must be an object")
		}
		t, _ := m["type"].(string)
		if t == "" {
			if _, hasName := m["name"]; hasName {
				t = "function"
			}
		}
		name, _ := m["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("tool missing name")
		}
		switch t {
		case "function":
			fn := canon.FunctionTool{Name: canon.ToolName(name)}
			fn.Description, _ = m["description"].(string)
			if strict, ok := m["strict"].(bool); ok {
				fn.Strict = strict
			}
			if params, ok := m["parameters"]; ok {
				raw, err := json.Marshal(params)
				if err != nil {
					return nil, err
				}
				fn.Parameters = raw
			}
			out = append(out, fn)
		case "custom":
			def := canon.CustomToolDef{Name: canon.ToolName(name)}
			def.Description, _ = m["description"].(string)
			if formatMap, ok := m["format"].(map[string]any); ok {
				ft, _ := formatMap["type"].(string)
				switch ft {
				case "grammar":
					def.Format = canon.FormatGrammar
					def.Grammar = &canon.ToolGrammar{}
					def.Grammar.Syntax, _ = formatMap["syntax"].(string)
					def.Grammar.Definition, _ = formatMap["definition"].(string)
				default:
					return nil, fmt.Errorf("custom tool format %q unsupported", ft)
				}
			}
			out = append(out, def)
		default:
			return nil, fmt.Errorf("unsupported tool type %q", t)
		}
	}
	return out, nil
}

func toolChoiceFromVector(v any) (canon.ToolChoice, bool) {
	switch tc := v.(type) {
	case string:
		switch tc {
		case "none":
			return canon.ToolNone{}, true
		case "auto":
			return canon.ToolAuto{}, true
		case "required":
			return canon.ToolRequired{}, true
		}
	case map[string]any:
		t, _ := tc["type"].(string)
		switch t {
		case "allowed_tools":
			mode, _ := tc["mode"].(string)
			var tools []canon.ToolName
			if rawTools, ok := tc["tools"].([]any); ok {
				for _, rt := range rawTools {
					rm, ok := rt.(map[string]any)
					if !ok {
						continue
					}
					name, _ := rm["name"].(string)
					if name != "" {
						tools = append(tools, canon.ToolName(name))
					}
				}
			}
			m := canon.AllowedAuto
			if mode == "required" {
				m = canon.AllowedRequired
			}
			return canon.ToolAllowed{Mode: m, Tools: tools}, true
		case "function":
			fn, _ := tc["function"].(map[string]any)
			name, _ := fn["name"].(string)
			if name != "" {
				return canon.ToolNamed{Name: canon.ToolName(name)}, true
			}
		}
	}
	return nil, false
}

func contentFrom(v any) []canon.Content {
	switch x := v.(type) {
	case string:
		return []canon.Content{canon.TextContent{Text: x}}
	case []any:
		var out []canon.Content
		for _, p := range x {
			m, ok := p.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "text":
				text, _ := m["text"].(string)
				out = append(out, canon.TextContent{Text: text})
			case "image":
				mt, _ := m["media_type"].(string)
				data, _ := m["data"].(string)
				out = append(out, canon.ImageContent{MIMEType: mt, Data: []byte(data)})
			}
		}
		return out
	default:
		return nil
	}
}

func roleFrom(role string) canon.Role {
	switch role {
	case "assistant":
		return canon.RoleAssistant
	case "system":
		return canon.RoleSystem
	case "developer":
		return canon.RoleDeveloper
	default:
		return canon.RoleUser
	}
}
