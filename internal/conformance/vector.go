package conformance

import (
	"encoding/json"

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
	if o, ok := vector["options"].(map[string]any); ok {
		if t, ok := o["temperature"].(float64); ok {
			req.Sampling.Temperature = &t
		}
		if tf, ok := o["textFormat"].(map[string]any); ok {
			req.Text.Format = textFormatFrom(tf)
		}
	}
	return req
}

func textFormatFrom(m map[string]any) *canon.TextFormat {
	t, ok := m["type"].(string)
	if !ok {
		return nil
	}
	f := &canon.TextFormat{Type: t}
	if name, ok := m["name"].(string); ok {
		f.Name = name
	}
	if desc, ok := m["description"].(string); ok {
		f.Description = desc
	}
	if schema, ok := m["schema"].(map[string]any); ok {
		raw, _ := json.Marshal(schema)
		f.Schema = raw
	}
	if strict, ok := m["strict"].(bool); ok {
		f.Strict = &strict
	}
	return f
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
