package openaierr

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
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

func ClassForInband(p canon.ProviderError, fallback provider.ErrorClass) provider.ErrorClass {
	for _, raw := range []json.RawMessage{p.Error, p.StatusDetails} {
		var fields struct {
			Code       string          `json:"code"`
			Type       string          `json:"type"`
			Status     json.RawMessage `json:"status"`
			StatusCode json.RawMessage `json:"status_code"`
			HTTPStatus json.RawMessage `json:"http_status"`
		}
		if json.Unmarshal(raw, &fields) != nil {
			continue
		}
		for _, code := range []string{fields.Code, fields.Type} {
			switch code {
			case "context_length_exceeded":
				return provider.ClassContextLength
			case "rate_limit_exceeded", "rate_limit_error", "too_many_requests":
				return provider.ClassRateLimited
			case "insufficient_quota":
				return provider.ClassQuotaExhausted
			}
			if strings.HasPrefix(code, "invalid_") {
				return provider.ClassInvalidRequest
			}
		}
		for _, rawStatus := range []json.RawMessage{fields.Status, fields.StatusCode, fields.HTTPStatus} {
			text := string(rawStatus)
			if len(text) > 0 && text[0] == '"' && json.Unmarshal(rawStatus, &text) != nil {
				continue
			}
			status, err := strconv.Atoi(text)
			if err != nil || status < 400 || status > 599 || status == 401 || status == 403 {
				continue
			}
			return ClassForStatus(status, fields.Code)
		}
	}
	return fallback
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
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	message := func(value any) string {
		if obj, ok := value.(map[string]any); ok {
			value = obj["message"]
		}
		text, _ := value.(string)
		return plainMessage(text)
	}
	if values, ok := value.([]any); ok {
		for _, value := range values {
			if text := message(value); text != "" {
				return text
			}
		}
		return ""
	}
	return message(value)
}

func plainMessage(text string) string {
	const maxBytes = 1024
	var out strings.Builder
	space := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			space = out.Len() > 0
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		size := utf8.RuneLen(r)
		if space {
			size++
		}
		if out.Len()+size > maxBytes {
			break
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
	}
	return out.String()
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
