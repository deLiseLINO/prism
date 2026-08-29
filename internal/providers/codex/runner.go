package codex

import (
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
	"prism/internal/quota"
)

type Runner struct {
	Creds       CredentialSource
	Client      *http.Client
	Now         func() time.Time
	QuotaSink   func(account.AccountID, quota.Snapshot, []string)
	WarningSink func(string)
}

var (
	_ provider.Runner    = (*Runner)(nil)
	_ provider.Compactor = (*Runner)(nil)
)

func Register(r *provider.Registry, creds CredentialSource, client *http.Client) error {
	return r.Register(ProviderID, &Runner{Creds: creds, Client: client, Now: time.Now})
}

func (r *Runner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	cred, err := r.Creds.Credential(req.Lease.Account)
	if err != nil {
		return provider.RunError{Kind: provider.Retryable, Class: provider.ClassTransport, Cause: err}
	}
	result, err := BuildRequestBody(req.Request)
	if err != nil {
		return provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: err}
	}
	for _, w := range result.Warnings {
		if r.WarningSink != nil {
			r.WarningSink(w)
		}
	}
	fp, err := BuildFingerprint(fingerprintInput{Target: req.Target, Facts: req.Facts, Credential: cred, Body: result.Body})
	if err != nil {
		return provider.RunError{Kind: provider.Retryable, Class: provider.ClassTransport, Cause: err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fp.URL, strings.NewReader(string(fp.Body)))
	if err != nil {
		return provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: err}
	}
	applyHeaders(httpReq.Header, fp.Headers)
	resp, err := r.httpClient().Do(httpReq)
	if err != nil {
		class := provider.ClassTransport
		if ctx.Err() != nil {
			class = provider.ClassTimeout
		}
		return provider.RunError{Kind: provider.Retryable, Class: class, Cause: err}
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return classifyStatus(resp)
	}
	if r.QuotaSink != nil {
		q := ParseQuotaHeaders(resp.Header)
		if q.OK {
			r.QuotaSink(req.Lease.Account, q.Snapshot, q.Warnings)
		} else if len(q.Warnings) > 0 {
			r.QuotaSink(req.Lease.Account, quota.Snapshot{Source: quota.SourceHeader}, q.Warnings)
		}
	}
	decoder := NewDecoder(func(ev canon.Event) error {
		if err := sink.Emit(ev); err != nil {
			return sinkWrap{err}
		}
		return nil
	})
	if err := decoder.Decode(resp.Body); err != nil {
		var wrapped sinkWrap
		if errors.As(err, &wrapped) {
			return wrapped.cause
		}
		return provider.RunError{Kind: provider.UnsafeReplay, Class: provider.ClassTransport, Accepted: true, Cause: err}
	}
	usage := decoder.Usage()
	if !decoder.Done() {
		err := sink.Emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUpstreamTransport, Message: "codex stream ended without a terminal event"}, Usage: usage})
		if err != nil {
			return err
		}
		return provider.RunError{Kind: provider.TerminalEmitted, Class: provider.ClassTransport, Accepted: true, Cause: errors.New("codex: stream ended without terminal")}
	}
	return nil
}

type sinkWrap struct{ cause error }

func (e sinkWrap) Error() string { return e.cause.Error() }
func (e sinkWrap) Unwrap() error { return e.cause }

func applyHeaders(h http.Header, headers []Header) {
	for _, v := range headers {
		h[v.Name] = []string{v.Value}
	}
}

func (r *Runner) Compact(ctx context.Context, req provider.CompactRequest) (provider.CompactResult, error) {
	cred, err := r.Creds.Credential(req.Lease.Account)
	if err != nil {
		return provider.CompactResult{}, provider.RunError{Kind: provider.Retryable, Class: provider.ClassTransport, Cause: err}
	}
	body, err := compactBody(req)
	if err != nil {
		return provider.CompactResult{}, provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: err}
	}
	headers, err := BuildFingerprint(fingerprintInput{Target: req.Target, Facts: req.Facts, Credential: cred, Body: body})
	if err != nil {
		return provider.CompactResult{}, provider.RunError{Kind: provider.Retryable, Class: provider.ClassTransport, Cause: err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, compactURL(req.Target.BaseURL), strings.NewReader(string(body)))
	if err != nil {
		return provider.CompactResult{}, provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: err}
	}
	applyHeaders(httpReq.Header, headers.Headers)
	resp, err := r.httpClient().Do(httpReq)
	if err != nil {
		class := provider.ClassTransport
		if ctx.Err() != nil {
			class = provider.ClassTimeout
		}
		return provider.CompactResult{}, provider.RunError{Kind: provider.Retryable, Class: class, Cause: err}
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return provider.CompactResult{}, classifyStatus(resp)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return provider.CompactResult{}, provider.RunError{Kind: provider.Retryable, Class: provider.ClassTransport, Accepted: true, Cause: err}
	}
	return decodeCompact(payload)
}

func compactBody(req provider.CompactRequest) ([]byte, error) {
	input, err := inputFrom(req.Input)
	if err != nil {
		return nil, fmt.Errorf("codex compact: %w", err)
	}
	body := struct {
		Model  canon.ModelID `json:"model"`
		Input  []wireItem    `json:"input"`
		Stream bool          `json:"stream"`
		Store  bool          `json:"store"`
	}{Model: req.Target.Model, Input: input, Store: false}
	return json.Marshal(body)
}

func decodeCompact(payload []byte) (provider.CompactResult, error) {
	var resp struct {
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return provider.CompactResult{}, provider.RunError{
			Kind: provider.Retryable, Class: provider.ClassServer, Accepted: true,
			Cause: errors.New("codex compact: malformed response"),
		}
	}
	if resp.Error != nil {
		return provider.CompactResult{}, provider.RunError{
			Kind: provider.Retryable, Class: provider.ClassServer, Accepted: true,
			Cause: fmt.Errorf("codex compact: %s", resp.Error.Message),
		}
	}
	if resp.Status == "failed" {
		return provider.CompactResult{}, provider.RunError{
			Kind: provider.Retryable, Class: provider.ClassServer, Accepted: true,
			Cause: errors.New("codex compact: upstream compaction failed"),
		}
	}
	var text strings.Builder
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
	}
	if text.Len() == 0 {
		return provider.CompactResult{}, provider.RunError{
			Kind: provider.Retryable, Class: provider.ClassServer, Accepted: true,
			Cause: errors.New("codex compact: upstream compaction returned no summary text"),
		}
	}
	usage := canon.Usage{}
	if resp.Usage != nil {
		usage = canon.Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}
	}
	summary := canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: text.String()}}}
	return provider.CompactResult{Summary: summary, Usage: usage}, nil
}

func (r *Runner) httpClient() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

func drainClose(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
}

func classifyStatus(resp *http.Response) error {
	retryAfter := retryAfterFrom(resp.Header)
	class := classForStatus(resp.StatusCode)
	if class == provider.ClassInvalidRequest {
		return provider.RunError{
			Kind: provider.TerminalOmitted, Class: class, Accepted: true,
			Cause: fmt.Errorf("codex: upstream status %d", resp.StatusCode),
		}
	}
	return provider.RunError{
		Kind: provider.Retryable, Class: class, Accepted: true, RetryAfter: retryAfter,
		Cause: fmt.Errorf("codex: upstream status %d", resp.StatusCode),
	}
}

func classForStatus(status int) provider.ErrorClass {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return provider.ClassUnauthorized
	case http.StatusTooManyRequests:
		return provider.ClassRateLimited
	case http.StatusPaymentRequired:
		return provider.ClassQuotaExhausted
	case http.StatusNotFound:
		return provider.ClassNotFound
	case http.StatusRequestTimeout:
		return provider.ClassTimeout
	}
	if status >= 500 {
		return provider.ClassServer
	}
	return provider.ClassInvalidRequest
}

func retryAfterFrom(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("retry-after"))
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
