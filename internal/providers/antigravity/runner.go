package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/provider"
)

type CredentialPair struct {
	AccessToken string
	ProjectID   string
	Generation  account.CredentialGeneration
}

type CredentialSource interface {
	Credential(ctx context.Context, lease account.Lease) (CredentialPair, error)
}

// CredentialRenewer is an optional CredentialSource capability: it replaces
// an access token the upstream rejected even though it had not expired.
type CredentialRenewer interface {
	RefreshRejected(ctx context.Context, lease account.Lease, rejected string) (CredentialPair, error)
}

type Runner struct {
	creds           CredentialSource
	client          *http.Client
	baseURL         string
	currentProvider func() config.Provider
	rand            func() float64
	sleep           func(time.Duration)
	newID           func() string
}

func NewRunner(creds CredentialSource, client *http.Client, baseURL string, currentProvider func() config.Provider) (*Runner, error) {
	if creds == nil {
		return nil, fmt.Errorf("antigravity: credential source is required")
	}
	if client == nil {
		return nil, fmt.Errorf("antigravity: http client is required")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Runner{
		creds:           creds,
		client:          client,
		baseURL:         baseURL,
		currentProvider: currentProvider,
		rand:            rand.Float64,
		newID:           NewRequestID,
	}, nil
}

func (r *Runner) SetRand(f func() float64) {
	if f != nil {
		r.rand = f
	}
}

func (r *Runner) SetSleep(f func(time.Duration)) {
	if f != nil {
		r.sleep = f
	}
}

func (r *Runner) SetRequestIDs(f func() string) {
	if f != nil {
		r.newID = f
	}
}

func (r *Runner) Register(reg *provider.Registry, id account.ProviderID) error {
	return reg.Register(id, r)
}

func (r *Runner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	creds, err := r.creds.Credential(ctx, req.Lease)
	if err != nil {
		return provider.CredentialRunError(err)
	}
	sessionID := SessionID(string(req.Facts.Thread), firstUserText(req.Request.Input))
	var p config.Provider
	if r.currentProvider != nil {
		p = r.currentProvider()
	}
	baseURL := req.Target.BaseURL
	if baseURL == "" {
		baseURL = r.baseURL
	}
	selection := ModelSelection{Mode: p.ModelMode}
	for _, c := range p.ModelCatalogs {
		if c.BaseURL == EndpointKey(baseURL) && c.Account == string(req.Lease.Account) && c.Project == creds.ProjectID {
			selection.Catalog = NewModelCatalog(c.RawModels)
			break
		}
	}
	body, err := BuildEnvelope(req.Request, creds.ProjectID, r.newID(), sessionID, selection)
	if err != nil {
		return provider.RunError{
			Kind:       provider.TerminalOmitted,
			Class:      provider.ClassInvalidRequest,
			ReplaySafe: true,
			Cause:      err,
		}
	}
	return r.run(ctx, req, sink, creds, body)
}

func firstUserText(items []canon.Item) string {
	for _, item := range items {
		msg, ok := item.(canon.Message)
		if !ok || msg.Role != canon.RoleUser {
			continue
		}
		for _, c := range msg.Content {
			if t, ok := c.(canon.TextContent); ok && t.Text != "" {
				return t.Text
			}
		}
	}
	return ""
}

const (
	headerWaiting int32 = iota
	headerReceived
	headerExpired
)

func (r *Runner) run(ctx context.Context, req provider.RunRequest, sink provider.Sink, creds CredentialPair, body []byte) error {
	timeout := req.Target.Timeout
	if timeout <= 0 {
		timeout = AttemptTimeout
	}
	baseURL := req.Target.BaseURL
	if baseURL == "" {
		baseURL = r.baseURL
	}
	url := StreamURL(baseURL)
	replayUsed := false
	renewed := false
	for attempt := 0; attempt < RetryAttempts; attempt++ {
		if ctx.Err() != nil {
			return transportError(ctx.Err())
		}
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		var headerState atomic.Int32
		timer := time.AfterFunc(timeout, func() {
			if headerState.CompareAndSwap(headerWaiting, headerExpired) {
				cancelAttempt()
			}
		})
		cancel := func() {
			timer.Stop()
			cancelAttempt()
		}
		httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			cancel()
			return transportError(err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", RequestUserAgent())
		httpReq.Header.Set("Authorization", "Bearer "+creds.AccessToken)
		if req.CredentialObserver != nil {
			req.CredentialObserver(creds.Generation)
		}
		startedAt := time.Now()
		observe := func(status int, err error) {
			if req.AttemptObserver != nil {
				req.AttemptObserver(provider.NetworkAttempt{StartedAt: startedAt, FinishedAt: time.Now(), StatusCode: status, Err: err})
			}
		}
		resp, err := r.client.Do(httpReq)
		if err == nil {
			if !headerState.CompareAndSwap(headerWaiting, headerReceived) {
				resp.Body.Close()
				err = context.DeadlineExceeded
			}
			timer.Stop()
		}
		if err != nil {
			observe(0, err)
			cancel()
			if ctx.Err() != nil {
				return transportError(ctx.Err())
			}
			if headerState.Load() == headerExpired || isTimeout(err) {
				return provider.RunError{
					Kind: provider.Retryable, Class: provider.ClassTimeout, ReplaySafe: true, Cause: err,
				}
			}
			failure := provider.RunError{Kind: provider.Retryable, Class: provider.ClassTransport, ReplaySafe: true, Cause: err}
			if attempt < RetryAttempts-1 {
				if err := r.waitRetry(ctx, BackoffDelay(attempt, r.rand()), failure); err != nil {
					return err
				}
				continue
			}
			return failure
		}
		if resp.StatusCode == http.StatusOK {
			result := r.stream(resp, sink)
			observe(resp.StatusCode, result)
			cancel()
			return result
		}
		errorBody, readErr := readBody(resp)
		cancel()
		if readErr != nil {
			observe(resp.StatusCode, readErr)
		} else {
			class, kind := classifyStatus(resp.StatusCode)
			if resp.StatusCode == http.StatusTooManyRequests && isQuotaExhaustedBody(string(errorBody)) {
				class = provider.ClassQuotaExhausted
			}
			observe(resp.StatusCode, provider.RunError{Kind: kind, Class: class, Cause: fmt.Errorf("antigravity: upstream status %d", resp.StatusCode)})
		}
		if readErr != nil {
			return transportError(readErr)
		}
		payloadText := string(errorBody)

		if resp.StatusCode == http.StatusUnauthorized && !renewed {
			if renewer, ok := r.creds.(CredentialRenewer); ok {
				// The token passed the expiry check yet the upstream refused
				// it: revoked or rotated server-side. Nothing was streamed,
				// so one replay with a renewed token is safe.
				renewed = true
				fresh, rerr := renewer.RefreshRejected(ctx, req.Lease, creds.AccessToken)
				if rerr != nil {
					return provider.CredentialRunError(rerr)
				}
				creds = fresh
				attempt--
				continue
			}
		}
		if resp.StatusCode == http.StatusBadRequest && !replayUsed {
			if repaired, changed := repairEnvelope(body, payloadText); changed {
				replayUsed = true
				body = repaired
				attempt--
				continue
			}
		}
		if resp.StatusCode == http.StatusTooManyRequests && isQuotaExhaustedBody(payloadText) {
			return provider.RunError{
				Kind:       provider.TerminalOmitted,
				Class:      provider.ClassQuotaExhausted,
				ReplaySafe: true,
				RetryAfter: provider.HTTPRetryAfterDelay(resp),
				Cause:      fmt.Errorf("antigravity: quota exhausted: %s", errorEnvelopeMessage(payloadText)),
			}
		}
		class, kind := classifyStatus(resp.StatusCode)
		failure := provider.RunError{Kind: kind, Class: class, ReplaySafe: true,
			RetryAfter: provider.HTTPRetryAfterDelay(resp),
			Cause:      fmt.Errorf("antigravity: upstream status %d: %s", resp.StatusCode, errorEnvelopeMessage(payloadText))}
		if kind == provider.Retryable && attempt < RetryAttempts-1 {
			delay := max(BackoffDelay(attempt, r.rand()), failure.RetryAfter)
			if err := r.waitRetry(ctx, delay, failure); err != nil {
				return err
			}
			continue
		}
		return failure
	}
	return provider.RunError{
		Kind:       provider.TerminalOmitted,
		Class:      provider.ClassServer,
		ReplaySafe: true,
		Cause:      fmt.Errorf("antigravity: retry budget exhausted"),
	}
}

func (r *Runner) stream(resp *http.Response, sink provider.Sink) error {
	defer resp.Body.Close()
	return DecodeStream(resp.Body, sink.Emit)
}

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func errorEnvelopeMessage(payloadText string) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payloadText), &env); err == nil && env.Error.Message != "" {
		if env.Error.Status != "" {
			return env.Error.Status + ": " + env.Error.Message
		}
		return env.Error.Message
	}
	return strings.TrimSpace(payloadText)
}

func isTimeout(err error) bool {
	for err != nil {
		if t, ok := err.(interface{ Timeout() bool }); ok {
			return t.Timeout()
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

func transportError(err error) error {
	return provider.RunError{
		Kind:       provider.TerminalOmitted,
		Class:      provider.ClassTransport,
		ReplaySafe: true,
		Cause:      err,
	}
}
