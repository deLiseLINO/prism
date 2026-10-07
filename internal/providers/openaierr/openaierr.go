// Package openaierr classifies OpenAI-compatible upstream HTTP failures into
// typed provider errors. It is shared by the custom Responses and custom Chat
// Completions runners.
package openaierr

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
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
	var reported *canon.ProviderError
	if providerErr, ok := Parse(payload); ok {
		copied := providerErr
		reported = &copied
		if text := Text(providerErr); text != "" {
			msg = text
		}
		code = stringCode(providerErr.Error)
		if code == "" && ContextOverflow(msg) {
			code = codeContextLength
		}
	} else if json.Unmarshal(payload, &parsed) == nil && parsed.Message != "" {
		msg = parsed.Message
	}
	return provider.RunError{
		Kind:       provider.Retryable,
		Class:      ClassForStatus(resp.StatusCode, code),
		Accepted:   accepted,
		ReplaySafe: true,
		RetryAfter: provider.HTTPRetryAfterDelay(resp),
		Cause:      fmt.Errorf("%s: %s", runner, msg),
		Reported:   reported,
	}
}

// Code returns the string error code of a provider error value, or "".
func Code(p canon.ProviderError) string { return stringCode(p.Error) }

const (
	codeContextLength = "context_length_exceeded"
	codeInsufficient  = "insufficient_quota"
)

// Compatible servers rarely set the OpenAI error code; the message is the
// only signal that the prompt did not fit.
var contextOverflowText = regexp.MustCompile(`(?i)maximum context length|context[_ ](length|window)[^.]*(exceed|too long|overflow)|exceeds? the context (window|length|size)|prompt is too long|input is too long|reduce the length of the messages`)

// ContextOverflow reports whether an upstream error message says the prompt
// did not fit the model's context window.
func ContextOverflow(message string) bool {
	return contextOverflowText.MatchString(message)
}

func stringCode(raw []byte) string {
	var obj struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Code
	}
	return ""
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
	case status == 402:
		return provider.ClassQuotaExhausted
	case status == 413 || code == codeContextLength:
		return provider.ClassContextLength
	case status == 429 && code == codeInsufficient:
		return provider.ClassQuotaExhausted
	case status == 429:
		return provider.ClassRateLimited
	case status >= 500:
		return provider.ClassServer
	default:
		return provider.ClassInvalidRequest
	}
}
