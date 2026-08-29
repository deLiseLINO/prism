package anthropic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"prism/internal/provider"
)

const (
	DefaultBaseURL     = "https://api.anthropic.com"
	DefaultAPIVersion  = "2023-06-01"
	defaultMaxTokens   = 8192
	maxTokensCeiling   = 32000
	thinkingHeadroom   = 8192
	minThinkingBudget  = 1024
	maxErrorBodyBytes  = 1 << 20
	stateStoreName     = "anthropic"
	promptCacheKeyNote = "anthropic: prompt_cache_key accepted but not forwarded in MVP; no cache_control synthesis"
)

type Options struct {
	BaseURL          string
	AnthropicVersion string
	Beta             []string
	HTTP             *http.Client
	Log              *slog.Logger
}

type Runner struct {
	opts         Options
	http         *http.Client
	log          *slog.Logger
	state        *stateStore
	cacheKeyOnce sync.Once
}

var (
	_ provider.Runner       = (*Runner)(nil)
	_ provider.TokenCounter = (*Runner)(nil)
)

func New(opts Options) *Runner {
	if opts.HTTP == nil {
		opts.HTTP = http.DefaultClient
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Runner{
		opts:  opts,
		http:  opts.HTTP,
		log:   opts.Log,
		state: newStateStore(),
	}
}

func (r *Runner) notePromptCacheKey() {
	r.cacheKeyOnce.Do(func() {
		r.log.Info(promptCacheKeyNote)
	})
}

func (r *Runner) baseURL(target provider.Target) string {
	if target.BaseURL != "" {
		return target.BaseURL
	}
	if r.opts.BaseURL != "" {
		return r.opts.BaseURL
	}
	return DefaultBaseURL
}

func (r *Runner) apiVersion() string {
	if r.opts.AnthropicVersion != "" {
		return r.opts.AnthropicVersion
	}
	return DefaultAPIVersion
}

func stripMessagesPath(base string) string {
	trimmed := strings.TrimRight(base, "/")
	trimmed = strings.TrimSuffix(trimmed, "/v1/messages")
	trimmed = strings.TrimSuffix(trimmed, "/v1")
	return strings.TrimRight(trimmed, "/")
}

func messagesURL(base string) string {
	return stripMessagesPath(base) + "/v1/messages"
}

func countTokensURL(base string) string {
	return stripMessagesPath(base) + "/v1/messages/count_tokens"
}

type header struct {
	name  string
	value string
}

type headers []header

func (hs headers) Get(name string) (string, bool) {
	for _, h := range hs {
		if strings.EqualFold(h.name, name) {
			return h.value, true
		}
	}
	return "", false
}

func (r *Runner) buildHeaders(accept, apiKey string, beta []string) headers {
	hs := headers{
		{"Content-Type", "application/json"},
		{"Accept", accept},
		{"x-api-key", apiKey},
		{"anthropic-version", r.apiVersion()},
	}
	for _, b := range beta {
		hs = append(hs, header{"anthropic-beta", b})
	}
	return hs
}

type outbound struct {
	url     string
	headers headers
	body    []byte
}

func (r *Runner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	r.notePromptCacheKey()
	out, err := r.buildRequest(req)
	if err != nil {
		return buildFailure(err)
	}
	if req.Target.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Target.Timeout)
		defer cancel()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, out.url, bytes.NewReader(out.body))
	if err != nil {
		return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: err}
	}
	for _, h := range out.headers {
		httpReq.Header.Add(h.name, h.value)
	}
	resp, err := r.http.Do(httpReq)
	if err != nil {
		return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return upstreamError(resp)
	}
	return r.stream(resp.Body, sink)
}

func (r *Runner) buildRequest(req provider.RunRequest) (*outbound, error) {
	out, err := r.render(req.Request, true)
	if err != nil {
		return nil, err
	}
	out.url = messagesURL(r.baseURL(req.Target))
	out.headers = r.buildHeaders("text/event-stream", req.Target.APIKeyRef, r.opts.Beta)
	return out, nil
}

func buildFailure(err error) *provider.RunError {
	return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: err}
}

func upstreamError(resp *http.Response) *provider.RunError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	message, errType := parseErrorBody(body)
	if message == "" {
		message = fmt.Sprintf("upstream status %d", resp.StatusCode)
	}
	cause := fmt.Errorf("anthropic: %s (type=%s status=%d)", message, errType, resp.StatusCode)
	re := &provider.RunError{
		Kind:  provider.TerminalOmitted,
		Class: errorClassForStatus(resp.StatusCode),
		Cause: cause,
	}
	if retryAfter := retryAfterFor(resp); retryAfter > 0 {
		re.RetryAfter = retryAfter
	}
	return re
}

func parseErrorBody(body []byte) (string, string) {
	payload, err := decodeJSON(body)
	if err != nil {
		return "", ""
	}
	errObj, _ := payload["error"].(map[string]any)
	message, _ := errObj["message"].(string)
	errType, _ := errObj["type"].(string)
	return message, errType
}

func errorClassForStatus(status int) provider.ErrorClass {
	switch status {
	case http.StatusBadRequest:
		return provider.ClassInvalidRequest
	case http.StatusUnauthorized, http.StatusForbidden:
		return provider.ClassUnauthorized
	case http.StatusNotFound:
		return provider.ClassNotFound
	case http.StatusRequestTimeout:
		return provider.ClassTimeout
	case http.StatusRequestEntityTooLarge:
		return provider.ClassContextLength
	case http.StatusTooManyRequests:
		return provider.ClassRateLimited
	}
	if status >= 500 {
		return provider.ClassServer
	}
	return provider.ClassInvalidRequest
}

func retryAfterFor(resp *http.Response) time.Duration {
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

var errNoTerminal = errors.New("anthropic: stream ended without message_stop")
