// Package customimages sends standalone image generation requests to
// OpenAI-compatible providers.
package customimages

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
)

const (
	// MaxResponseBytes caps the buffered upstream answer. Each generated
	// image arrives base64-encoded, a few megabytes per PNG.
	MaxResponseBytes = 128 << 20
	// Timeout bounds one generation; upstreams take tens of seconds and up
	// to a few minutes for large outputs.
	Timeout = 5 * time.Minute
)

type KeyResolver func(ctx context.Context, target provider.Target, lease account.Lease) (string, error)

type Generator struct {
	resolve KeyResolver
	client  *http.Client
}

var _ provider.ImageGenerator = (*Generator)(nil)

func New(resolve KeyResolver, client *http.Client) *Generator {
	return &Generator{resolve: resolve, client: client}
}

// URL is the generation endpoint beside the chat and responses endpoints
// built from the same configured base.
func URL(base string) string {
	return openaierr.APIBase(base) + "/images/generations"
}

func (g *Generator) GenerateImage(ctx context.Context, req provider.ImageRequest) (result provider.ImageResponse, err error) {
	if strings.TrimSpace(req.Target.BaseURL) == "" {
		return provider.ImageResponse{}, provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest,
			Cause: fmt.Errorf("provider %s has no baseURL", req.Target.Provider)}
	}
	var key string
	if req.Target.APIKeyRef != "" {
		key, err = g.resolve(ctx, req.Target, req.Lease)
		if err != nil {
			return provider.ImageResponse{}, provider.CredentialRunError(err)
		}
		if strings.TrimSpace(key) == "" {
			return provider.ImageResponse{}, provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest,
				Cause: fmt.Errorf("apiKeyRef %q on provider %s resolved to an empty credential", req.Target.APIKeyRef, req.Target.Provider)}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, URL(req.Target.BaseURL), bytes.NewReader(req.Body))
	if err != nil {
		return provider.ImageResponse{}, provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest, Cause: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	client := g.client
	if client == nil {
		client = http.DefaultClient
	}
	started := time.Now()
	resp, err := client.Do(httpReq)
	if req.AttemptObserver != nil {
		defer func() {
			attempt := provider.NetworkAttempt{StartedAt: started, FinishedAt: time.Now(), Err: err}
			if resp != nil {
				attempt.StatusCode = resp.StatusCode
			}
			req.AttemptObserver(attempt)
		}()
	}
	if err != nil {
		return provider.ImageResponse{}, exchangeError(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return provider.ImageResponse{}, exchangeError(ctx, err)
	}
	if len(body) > MaxResponseBytes {
		return provider.ImageResponse{}, provider.RunError{Kind: provider.UnsafeReplay, Class: provider.ClassServer, Accepted: true,
			Cause: fmt.Errorf("image response exceeds %d bytes", MaxResponseBytes)}
	}
	return provider.ImageResponse{Status: resp.StatusCode, Body: body}, nil
}

// exchangeError classifies a failure after the request may have reached the
// upstream: the generation might be under way, so it is never replay safe.
func exchangeError(ctx context.Context, err error) error {
	class := provider.ClassTransport
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		class = provider.ClassTimeout
	}
	return provider.RunError{Kind: provider.UnsafeReplay, Class: class, Cause: err}
}
