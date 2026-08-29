package conformance

import "prism/internal/canon"

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
	}
	return req
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
	default:
		return canon.RoleUser
	}
}
