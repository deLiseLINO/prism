package conformance

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func sseFieldValue(line, field string) (string, bool) {
	if !strings.HasPrefix(line, field) {
		return "", false
	}
	rest := line[len(field):]
	if rest == "" {
		return "", true
	}
	if !strings.HasPrefix(rest, ":") {
		return "", false
	}
	if strings.HasPrefix(rest, ": ") {
		return rest[2:], true
	}
	return rest[1:], true
}

func NormalizeSseBytes(bytes []byte, sourceProtocol string) ([]NormalizedEvent, error) {
	if !utf8.Valid(bytes) {
		return nil, fmt.Errorf("sse normalize: invalid utf-8")
	}
	text := string(bytes)
	if strings.HasPrefix(text, "\ufeff") {
		text = text[3:]
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var events []NormalizedEvent
	ordinal := 0
	frames := strings.Split(text, "\n\n")
	for _, rawFrame := range frames {
		if strings.TrimSpace(rawFrame) == "" {
			continue
		}
		lines := strings.Split(rawFrame, "\n")
		var dataLines []string
		eventName := ""
		hasEvent := false
		for _, line := range lines {
			if strings.HasPrefix(line, ":") {
				continue
			}
			if value, ok := sseFieldValue(line, "event"); ok {
				eventName = value
				hasEvent = true
				continue
			}
			if value, ok := sseFieldValue(line, "data"); ok {
				dataLines = append(dataLines, value)
			}
		}
		if len(dataLines) == 0 {
			continue
		}
		joined := strings.Join(dataLines, "\n")
		if sourceProtocol == "openai-chat" && joined == "[DONE]" {
			events = append(events, NormalizedEvent{Event: "[DONE]", Data: "[DONE]", Ordinal: ordinal})
			ordinal++
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(joined), &parsed); err != nil {
			name := "malformed"
			if hasEvent {
				name = eventName
			}
			events = append(events, NormalizedEvent{Event: name, Data: joined, Ordinal: ordinal})
			ordinal++
			continue
		}
		data, isObject := parsed.(map[string]any)
		if !isObject {
			continue
		}
		inferred := "message"
		if hasEvent {
			inferred = eventName
		} else if t, ok := data["type"].(string); ok {
			inferred = t
		}
		events = append(events, NormalizedEvent{Event: inferred, Data: data, Ordinal: ordinal})
		ordinal++
	}
	return events, nil
}
