package conformance

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/canon"
)

func RequestFromWire(inbound string, body []byte) (canon.Request, error) {
	switch inbound {
	case "openai-responses":
		return requestFromResponses(body)
	default:
		return canon.Request{}, fmt.Errorf("ingress: inbound protocol %s not implemented", inbound)
	}
}

func requestFromResponses(body []byte) (canon.Request, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return canon.Request{}, fmt.Errorf("responses ingress: decode: %w", err)
	}
	req := canon.Request{}
	if m, ok := root["model"].(string); ok {
		req.Model = canon.ModelID(m)
	}
	if s, ok := root["stream"].(bool); ok {
		req.Stream = s
	}
	if ins, ok := root["instructions"].(string); ok && ins != "" {
		req.Instructions = []canon.Content{canon.TextContent{Text: ins}}
	}
	input, ok := root["input"]
	if !ok {
		return canon.Request{}, fmt.Errorf("responses ingress: missing input")
	}
	switch in := input.(type) {
	case string:
		req.Input = []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: in}}}}
	case []any:
		for _, item := range in {
			rec, ok := item.(map[string]any)
			if !ok {
				return canon.Request{}, fmt.Errorf("responses ingress: input item must be an object")
			}
			role, _ := rec["role"].(string)
			r, err := roleFromWire(role)
			if err != nil {
				return canon.Request{}, err
			}
			content, err := responsesContent(rec["content"])
			if err != nil {
				return canon.Request{}, err
			}
			req.Input = append(req.Input, canon.Message{Role: r, Content: content})
		}
	default:
		return canon.Request{}, fmt.Errorf("responses ingress: input must be a string or an array")
	}
	return req, nil
}

func roleFromWire(role string) (canon.Role, error) {
	switch role {
	case "user":
		return canon.RoleUser, nil
	case "assistant":
		return canon.RoleAssistant, nil
	case "system":
		return canon.RoleSystem, nil
	default:
		return 0, fmt.Errorf("responses ingress: unsupported role %q", role)
	}
}

func responsesContent(v any) ([]canon.Content, error) {
	switch c := v.(type) {
	case string:
		return []canon.Content{canon.TextContent{Text: c}}, nil
	case []any:
		var out []canon.Content
		for _, raw := range c {
			part, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("responses ingress: content part must be an object")
			}
			content, err := responsesPartToContent(part)
			if err != nil {
				return nil, err
			}
			out = append(out, content)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("responses ingress: content must be a string or an array")
	}
}

func responsesPartToContent(part map[string]any) (canon.Content, error) {
	typ, _ := part["type"].(string)
	switch typ {
	case "input_text", "text":
		text, ok := part["text"].(string)
		if !ok {
			return nil, fmt.Errorf("responses ingress: %s part has no text", typ)
		}
		return canon.TextContent{Text: text}, nil
	case "input_image":
		imageURL, _ := part["image_url"].(string)
		detail, _ := part["detail"].(string)
		img, err := dataURLImage(imageURL, detail)
		if err != nil {
			return nil, fmt.Errorf("responses ingress: %w", err)
		}
		return img, nil
	default:
		return nil, fmt.Errorf("responses ingress: unsupported content part type %q", typ)
	}
}

func dataURLImage(imageURL, detail string) (canon.ImageContent, error) {
	const prefix = "data:"
	if !strings.HasPrefix(imageURL, prefix) {
		return canon.ImageContent{}, fmt.Errorf("input_image image_url %q is not a data URL", imageURL)
	}
	rest := imageURL[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return canon.ImageContent{}, fmt.Errorf("input_image data URL %q has no payload", imageURL)
	}
	meta := rest[:comma]
	payload := rest[comma+1:]
	if !strings.HasSuffix(meta, ";base64") {
		return canon.ImageContent{}, fmt.Errorf("input_image data URL %q is not base64", imageURL)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return canon.ImageContent{}, fmt.Errorf("input_image data URL %q: %w", imageURL, err)
	}
	return canon.ImageContent{
		MIMEType: strings.TrimSuffix(meta, ";base64"),
		Data:     data,
		Detail:   normalizeImageDetail(detail),
	}, nil
}

func normalizeImageDetail(detail string) string {
	if detail == "original" {
		return "high"
	}
	return detail
}
