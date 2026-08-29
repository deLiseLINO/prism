package customresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/provider"
)

type Header struct {
	Name  string
	Value string
}

type KeyResolver func(ctx context.Context, target provider.Target, lease account.Lease) (string, error)

type Options struct {
	ExtraHeaders []Header
	Models       []provider.Model
}

type Runner struct {
	resolve KeyResolver
	extra   []Header
	models  []provider.Model
	client  *http.Client
}

var (
	_ provider.Runner = (*Runner)(nil)
)

func New(resolve KeyResolver, opts Options) *Runner {
	return &Runner{resolve: resolve, extra: opts.ExtraHeaders, models: opts.Models}
}

type upstreamRequest struct {
	URL     string
	Headers []Header
	Body    []byte
}

func (r *Runner) buildUpstream(target provider.Target, key string, req canon.Request) (*upstreamRequest, error) {
	raw, err := buildBody(req)
	if err != nil {
		return nil, err
	}
	headers := []Header{
		{Name: "Content-Type", Value: "application/json"},
		{Name: "Authorization", Value: "Bearer " + key},
	}
	headers = append(headers, r.extra...)
	return &upstreamRequest{
		URL:     responsesURL(target.BaseURL),
		Headers: headers,
		Body:    raw,
	}, nil
}

func (r *Runner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	if err := validateTarget(req.Target); err != nil {
		return runError(provider.TerminalOmitted, provider.ClassInvalidRequest, false, false, 0, err)
	}
	key, err := r.resolve(ctx, req.Target, req.Lease)
	if err != nil {
		return runError(provider.TerminalOmitted, provider.ClassTransport, false, false, 0, err)
	}
	up, err := r.buildUpstream(req.Target, key, req.Request)
	if err != nil {
		return runError(provider.TerminalOmitted, provider.ClassInvalidRequest, false, false, 0, err)
	}
	if req.Target.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Target.Timeout)
		defer cancel()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, up.URL, bytes.NewReader(up.Body))
	if err != nil {
		return runError(provider.TerminalOmitted, provider.ClassInvalidRequest, false, false, 0, err)
	}
	for _, h := range up.Headers {
		httpReq.Header.Set(h.Name, h.Value)
	}
	client := r.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return runError(provider.Retryable, provider.ClassTimeout, false, true, 0, err)
		}
		return runError(provider.Retryable, provider.ClassTransport, false, true, 0, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return r.httpError(resp)
	}
	if req.Request.Stream {
		return r.runStream(resp.Body, sink)
	}
	return r.runAggregate(resp.Body, sink)
}

func validateTarget(target provider.Target) error {
	if target.Wire != provider.WireResponses {
		return fmt.Errorf("customresponses: target wire %d is not responses", target.Wire)
	}
	if target.BaseURL == "" {
		return errors.New("customresponses: target base url is required")
	}
	if target.APIKeyRef == "" {
		return errors.New("customresponses: target api key ref is required")
	}
	return nil
}

func (r *Runner) httpError(resp *http.Response) error {
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		payload = nil
	}
	var parsed struct {
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	msg := fmt.Sprintf("upstream http %d", resp.StatusCode)
	code := ""
	accepted := resp.StatusCode >= 500
	if json.Unmarshal(payload, &parsed) == nil && parsed.Error != nil {
		if parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		code = parsed.Error.Code
	}
	return runError(provider.Retryable, classForStatus(resp.StatusCode, code), accepted, true, retryAfterFrom(resp.Header.Get("Retry-After")), errors.New(msg))
}

func classForStatus(status int, code string) provider.ErrorClass {
	switch {
	case status == 401 || status == 403:
		return provider.ClassUnauthorized
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

func retryAfterFrom(raw string) time.Duration {
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

func runError(kind provider.RunErrorKind, class provider.ErrorClass, accepted, replaySafe bool, retryAfter time.Duration, cause error) error {
	return provider.RunError{
		Kind:       kind,
		Class:      class,
		Accepted:   accepted,
		ReplaySafe: replaySafe,
		RetryAfter: retryAfter,
		Cause:      cause,
	}
}

func usageFrom(response map[string]any) canon.Usage {
	var usage canon.Usage
	u, ok := response["usage"].(map[string]any)
	if !ok {
		return usage
	}
	usage.InputTokens = numberOf(u["input_tokens"])
	usage.OutputTokens = numberOf(u["output_tokens"])
	usage.TotalTokens = numberOf(u["total_tokens"])
	if d, ok := u["input_tokens_details"].(map[string]any); ok {
		usage.CachedInputTokens = numberOf(d["cached_tokens"])
	}
	if d, ok := u["output_tokens_details"].(map[string]any); ok {
		usage.ReasoningTokens = numberOf(d["reasoning_tokens"])
	}
	return usage
}

func numberOf(v any) int64 {
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return int64(f)
}
