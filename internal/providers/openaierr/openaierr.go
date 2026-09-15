// Package openaierr classifies OpenAI-compatible upstream HTTP failures into
// typed provider errors. It is shared by the custom Responses and custom Chat
// Completions runners.
package openaierr

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"prism/internal/provider"
)

// HTTPError reads an OpenAI-compatible error response and returns a typed
// provider.RunError. The cause carries the upstream message, never headers
// or credentials.
func HTTPError(resp *http.Response, runner string) error {
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		payload = nil
	}
	var parsed struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	msg := fmt.Sprintf("upstream http %d", resp.StatusCode)
	code := ""
	accepted := resp.StatusCode >= 500
	if json.Unmarshal(payload, &parsed) == nil {
		if len(parsed.Error) > 0 {
			var obj struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			}
			var flat string
			switch {
			case json.Unmarshal(parsed.Error, &obj) == nil && (obj.Message != "" || obj.Code != ""):
				if obj.Message != "" {
					msg = obj.Message
				}
				code = obj.Code
			case json.Unmarshal(parsed.Error, &flat) == nil && flat != "":
				msg = flat
			}
		} else if parsed.Message != "" {
			msg = parsed.Message
		}
	}
	return provider.RunError{
		Kind:       provider.Retryable,
		Class:      ClassForStatus(resp.StatusCode, code),
		Accepted:   accepted,
		ReplaySafe: true,
		RetryAfter: RetryAfter(resp.Header.Get("Retry-After")),
		Cause:      fmt.Errorf("%s: %s", runner, msg),
	}
}

func ClassForStatus(status int, code string) provider.ErrorClass {
	switch {
	case status == 401:
		return provider.ClassUnauthorized
	case status == 403:
		return provider.ClassForbidden
	case status == 404:
		return provider.ClassNotFound
	case status == 408:
		return provider.ClassTimeout
	case status == 429:
		return provider.ClassRateLimited
	case status == 400 && code == "context_length_exceeded":
		return provider.ClassContextLength
	case status >= 500:
		return provider.ClassServer
	default:
		return provider.ClassInvalidRequest
	}
}

func RetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds * float64(time.Second))
	}
	if when, err := http.ParseTime(raw); err == nil {
		d := time.Until(when)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
