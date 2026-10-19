// Package openaierr classifies OpenAI-compatible upstream HTTP failures into
// typed provider errors. It is shared by the custom Responses and custom Chat
// Completions runners.
package openaierr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

type Captured struct {
	Body     []byte
	Reported canon.ProviderError
}

func Capture(resp *http.Response) (Captured, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	reported, ok := Parse(body)
	if !ok && len(body) > 0 {
		if raw := bytes.TrimSpace(body); json.Valid(raw) {
			reported.Error = raw
		} else {
			reported.Error, _ = json.Marshal(string(body))
		}
	}
	reported.Status = resp.StatusCode
	return Captured{Body: body, Reported: reported}, err
}

// HTTPError captures the upstream response without coupling its status to retry classification.
func HTTPError(resp *http.Response, runner string) error {
	captured, _ := Capture(resp)
	msg := Text(captured.Reported)
	if msg == "" {
		msg = fmt.Sprintf("upstream http %d", resp.StatusCode)
	}
	code := Code(captured.Reported)
	if code == "" && ContextOverflow(msg) {
		code = codeContextLength
	}
	return provider.RunError{
		Kind:       provider.Retryable,
		Class:      ClassForStatus(resp.StatusCode, code),
		Accepted:   resp.StatusCode >= 500,
		ReplaySafe: true,
		RetryAfter: provider.HTTPRetryAfterDelay(resp),
		Cause:      fmt.Errorf("%s: %s", runner, msg),
		Reported:   &captured.Reported,
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
