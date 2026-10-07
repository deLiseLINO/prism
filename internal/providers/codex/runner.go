package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
	"github.com/deLiseLINO/prism/internal/quota"
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

func (r *Runner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) (runErr error) {
	cred, err := r.Creds.Credential(ctx, req.Lease)
	if err != nil {
		return provider.CredentialRunError(err)
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
	renewed := false
	resp, err := r.send(ctx, req.Lease, &cred, &renewed, req.AttemptObserver, req.CredentialObserver, func(c Credential) (*http.Request, error) {
		return r.runHTTPRequest(ctx, req, c, result.Body)
	})
	if err != nil {
		return err
	}
	defer func() {
		if resp != nil {
			if body, ok := resp.Body.(*attemptBody); ok && runErr != nil {
				body.err = runErr
			}
			resp.Body.Close()
		}
	}()
	if resp.StatusCode == http.StatusBadRequest && hasReplayedReasoning(req.Request) {
		// Encrypted reasoning is minted per caller identity. After a rotation
		// or a failover to another account the upstream refuses the blob; the
		// turn itself is fine, so resend it once without the reasoning items.
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(payload))
		if readErr != nil {
			return provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: readErr}
		}
		if reasoningBlobRejected(payload) {
			stripped := req.Request
			stripped.Input = withoutReasoning(req.Request.Input)
			retry, berr := BuildRequestBody(stripped)
			if berr != nil {
				return provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: berr}
			}
			resp, err = r.send(ctx, req.Lease, &cred, &renewed, req.AttemptObserver, req.CredentialObserver, func(c Credential) (*http.Request, error) {
				return r.runHTTPRequest(ctx, req, c, retry.Body)
			})
			if err != nil {
				return err
			}
		}
	}
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
	decoder.OnProgress(func() { provider.Mark(req.Progress) })
	if err := decoder.Decode(resp.Body); err != nil {
		var wrapped sinkWrap
		if errors.As(err, &wrapped) {
			return wrapped.cause
		}
		var re provider.RunError
		if errors.As(err, &re) {
			return re
		}
		return provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Accepted: true, Cause: err}
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

func (r *Runner) runHTTPRequest(ctx context.Context, req provider.RunRequest, cred Credential, body []byte) (*http.Request, error) {
	fp, err := BuildFingerprint(fingerprintInput{Target: req.Target, Facts: req.Facts, Credential: cred, Body: body})
	if err != nil {
		return nil, provider.RunError{Kind: provider.Retryable, Class: provider.ClassTransport, Cause: err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fp.URL, bytes.NewReader(fp.Body))
	if err != nil {
		return nil, provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: err}
	}
	applyHeaders(httpReq.Header, fp.Headers)
	return httpReq, nil
}

// send performs one upstream call. A 401 on a credential the store still
// holds as fresh means it was revoked or rotated server-side; the call was
// refused before any output, so one replay with a renewed token is safe.
func (r *Runner) send(ctx context.Context, lease account.Lease, cred *Credential, renewed *bool, observe func(provider.NetworkAttempt), credentialObserver func(account.CredentialGeneration), build func(Credential) (*http.Request, error)) (*http.Response, error) {
	resp, err := r.do(ctx, *cred, observe, credentialObserver, build)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || *renewed {
		return resp, err
	}
	renewer, ok := r.Creds.(CredentialRenewer)
	if !ok {
		return resp, nil
	}
	resp.Body.Close()
	*renewed = true
	fresh, err := renewer.RefreshRejected(ctx, lease, cred.AccessToken)
	if err != nil {
		return nil, provider.CredentialRunError(err)
	}
	*cred = fresh
	return r.do(ctx, fresh, observe, credentialObserver, build)
}

func (r *Runner) do(ctx context.Context, cred Credential, observe func(provider.NetworkAttempt), credentialObserver func(account.CredentialGeneration), build func(Credential) (*http.Request, error)) (*http.Response, error) {
	httpReq, err := build(cred)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, provider.RunError{Kind: provider.Retryable, Class: provider.ClassTimeout, Cause: ctx.Err()}
	}
	if credentialObserver != nil {
		credentialObserver(cred.Generation)
	}
	started := time.Now()
	resp, err := r.httpClient().Do(httpReq)
	if err != nil {
		if observe != nil {
			observe(provider.NetworkAttempt{StartedAt: started, FinishedAt: time.Now(), Err: err})
		}
		class := provider.ClassTransport
		if ctx.Err() != nil {
			class = provider.ClassTimeout
		}
		return nil, provider.RunError{Kind: provider.Retryable, Class: class, Cause: err}
	}
	if observe != nil {
		body := &attemptBody{ReadCloser: resp.Body, observe: observe, attempt: provider.NetworkAttempt{StartedAt: started, StatusCode: resp.StatusCode}}
		if resp.StatusCode != http.StatusOK {
			body.err = provider.RunError{Kind: provider.TerminalOmitted, Class: openaierr.ClassForStatus(resp.StatusCode, ""), Cause: fmt.Errorf("codex: upstream status %d", resp.StatusCode)}
		}
		resp.Body = body
	}
	return resp, nil
}

type attemptBody struct {
	io.ReadCloser
	observe func(provider.NetworkAttempt)
	attempt provider.NetworkAttempt
	err     error
	closed  bool
}

func (b *attemptBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		b.err = err
	}
	return n, err
}

func (b *attemptBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	err := b.ReadCloser.Close()
	if b.err == nil {
		b.err = err
	}
	b.attempt.FinishedAt, b.attempt.Err = time.Now(), b.err
	b.observe(b.attempt)
	return err
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
	cred, err := r.Creds.Credential(ctx, req.Lease)
	if err != nil {
		return provider.CompactResult{}, provider.CredentialRunError(err)
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
		Model        canon.ModelID `json:"model"`
		Instructions string        `json:"instructions,omitempty"`
		Input        []wireItem    `json:"input"`
		Stream       bool          `json:"stream"`
		Store        bool          `json:"store"`
	}{Model: req.Target.Model, Input: input.items, Store: false}
	for _, c := range req.Instructions {
		if t, ok := c.(canon.TextContent); ok {
			body.Instructions += t.Text
		}
	}
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
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	snippet := payload
	if len(snippet) > 2048 {
		snippet = snippet[:2048]
	}
	log.Printf("prismd: codex upstream status %d body %q", resp.StatusCode, string(snippet))
	code := ""
	var reported *canon.ProviderError
	if parsed, ok := openaierr.Parse(payload); ok {
		reported = &parsed
		code = openaierr.Code(parsed)
	}
	class := classForStatus(resp.StatusCode, code)
	if reported != nil && class == provider.ClassInvalidRequest && openaierr.ContextOverflow(openaierr.Text(*reported)) {
		class = provider.ClassContextLength
	}
	re := provider.RunError{
		Kind: provider.Retryable, Class: class, Accepted: true, Reported: reported,
		RetryAfter: provider.HTTPRetryAfterDelay(resp),
		Cause:      fmt.Errorf("codex: upstream status %d body %q", resp.StatusCode, string(snippet)),
	}
	if class == provider.ClassInvalidRequest || class == provider.ClassContextLength {
		re.Kind = provider.TerminalOmitted
	}
	return re
}

func classForStatus(status int, code string) provider.ErrorClass {
	switch status {
	case http.StatusUnauthorized:
		return provider.ClassUnauthorized
	case http.StatusForbidden:
		return provider.ClassForbidden
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
	return openaierr.ClassForStatus(status, code)
}

func hasReplayedReasoning(req canon.Request) bool {
	for _, it := range req.Input {
		if r, ok := it.(canon.ReasoningItem); ok && r.State.Store == reasoningStoreNative && r.State.Key != "" {
			return true
		}
	}
	return false
}

func withoutReasoning(in []canon.Item) []canon.Item {
	out := make([]canon.Item, 0, len(in))
	for _, it := range in {
		if _, ok := it.(canon.ReasoningItem); ok {
			continue
		}
		out = append(out, it)
	}
	return out
}

// reasoningBlobRejected reports the upstream refusing a replayed encrypted
// reasoning payload: the coded rejection, or the caller-mismatch wording.
func reasoningBlobRejected(payload []byte) bool {
	parsed, ok := openaierr.Parse(payload)
	if !ok {
		return false
	}
	if openaierr.Code(parsed) == "invalid_encrypted_content" {
		return true
	}
	msg := openaierr.Text(parsed)
	return strings.Contains(msg, "was not issued to this caller") && (strings.Contains(msg, "encrypted_content") || strings.Contains(msg, "reasoning"))
}
