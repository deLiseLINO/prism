package customchat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/provider"
	"prism/internal/providers/openaierr"
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
	}
	if key != "" {
		headers = append(headers, Header{Name: "Authorization", Value: "Bearer " + key})
	}
	headers = append(headers, r.extra...)
	return &upstreamRequest{
		URL:     chatURL(target.BaseURL),
		Headers: headers,
		Body:    raw,
	}, nil
}

func (r *Runner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	if err := validateTarget(req.Target); err != nil {
		return runError(provider.TerminalOmitted, provider.ClassInvalidRequest, false, false, 0, err)
	}
	var key string
	if req.Target.APIKeyRef != "" {
		var err error
		key, err = r.resolve(ctx, req.Target, req.Lease)
		if err != nil {
			return runError(provider.TerminalOmitted, provider.ClassTransport, false, false, 0, err)
		}
		if strings.TrimSpace(key) == "" {
			return runError(provider.TerminalOmitted, provider.ClassInvalidRequest, false, false, 0,
				fmt.Errorf("apiKeyRef %q on provider %s resolved to an empty credential", req.Target.APIKeyRef, req.Target.Provider))
		}
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
		return openaierr.HTTPError(resp, "customchat")
	}
	if req.Request.Stream {
		return r.runStream(resp.Body, sink)
	}
	return r.runAggregate(resp.Body, sink)
}

func validateTarget(target provider.Target) error {
	if target.Wire != provider.WireChat {
		return fmt.Errorf("customchat: target wire %d is not chat", target.Wire)
	}
	if target.BaseURL == "" {
		return errors.New("customchat: target base url is required")
	}
	return nil
}

func (r *Runner) Catalog(target provider.Target, lease account.Lease) provider.Catalog {
	return staticCatalog(r.models)
}

type staticCatalog []provider.Model

func (s staticCatalog) Models(ctx context.Context) ([]provider.Model, error) {
	return s, nil
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

var mintedCalls atomic.Uint64

// Clients echo tool_calls.id back as tool_call_id on the next turn; a call
// observed without an upstream id keeps one minted value everywhere it travels.
func mintCallID() string {
	return "call_prism_" + strconv.FormatUint(mintedCalls.Add(1), 10)
}
