package openaierr

import (
	"bytes"
	"encoding/json"

	"github.com/deLiseLINO/prism/internal/canon"
)

func Parse(raw []byte) (canon.ProviderError, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || !json.Valid(raw) {
		return canon.ProviderError{}, false
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return canon.ProviderError{}, false
	}
	var out canon.ProviderError
	if response, ok := objectField(body, "response"); ok {
		if v, ok := nonEmpty(response["error"]); ok {
			out.Error = v
		}
		if details, ok := objectField(response, "status_details"); ok {
			if v, ok := nonEmpty(details["error"]); ok {
				out.StatusDetails = v
			}
		}
	}
	if len(out.Error) == 0 {
		if v, ok := nonEmpty(body["error"]); ok {
			out.Error = v
		}
	}
	if len(out.Error) == 0 && len(out.StatusDetails) == 0 && hasIdentityKey(body) {
		out.Error = append([]byte(nil), raw...)
	}
	if len(out.Error) == 0 && len(out.StatusDetails) == 0 {
		return canon.ProviderError{}, false
	}
	return out, true
}

func Text(p canon.ProviderError) string {
	if msg := messageOf(p.Error); msg != "" {
		return msg
	}
	return messageOf(p.StatusDetails)
}

func messageOf(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || !json.Valid(raw) {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Message
	}
	return ""
}

func objectField(parent map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	raw, ok := nonEmpty(parent[key])
	if !ok || !json.Valid(raw) {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

func hasIdentityKey(body map[string]json.RawMessage) bool {
	for _, key := range []string{"message", "code", "type"} {
		if _, ok := body[key]; ok {
			return true
		}
	}
	return false
}

func nonEmpty(raw json.RawMessage) (json.RawMessage, bool) {
	raw = json.RawMessage(bytes.TrimSpace(raw))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	return append(json.RawMessage(nil), raw...), true
}
