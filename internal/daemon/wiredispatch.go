package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/failover"
	"github.com/deLiseLINO/prism/internal/provider"
)

type wireDispatcher struct {
	responses provider.Runner
	chat      provider.Runner
	messages  provider.Runner
	images    provider.ImageGenerator
	creds     *credentialStore
}

func (d wireDispatcher) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	if d.creds != nil && req.Target.APIKeyRef == "" {
		if err := d.creds.checkAnonymous(req); err != nil {
			return provider.CredentialRunError(err)
		}
	}
	ctx = failover.WithEndpoints(ctx, endpointsOf(req.Target, sessionKey(req)))
	switch req.Target.Wire {
	case provider.WireResponses:
		return d.responses.Run(ctx, req, sink)
	case provider.WireChat:
		return d.chat.Run(ctx, req, sink)
	case provider.WireMessages:
		return d.messages.Run(ctx, req, sink)
	default:
		return fmt.Errorf("prism: provider %q has no runner for wire %d", req.Target.Provider, req.Target.Wire)
	}
}

// GenerateImage serves the images route for the OpenAI-compatible wires,
// which share one generation endpoint.
func (d wireDispatcher) GenerateImage(ctx context.Context, req provider.ImageRequest) (provider.ImageResponse, error) {
	if req.Target.Wire != provider.WireResponses && req.Target.Wire != provider.WireChat || d.images == nil {
		return provider.ImageResponse{}, provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest,
			Cause: fmt.Errorf("prism: provider %q does not serve image generation", req.Target.Provider)}
	}
	if d.creds != nil && req.Target.APIKeyRef == "" {
		if err := d.creds.checkAnonymous(provider.RunRequest{Target: req.Target, Lease: req.Lease}); err != nil {
			return provider.ImageResponse{}, provider.CredentialRunError(err)
		}
	}
	ctx = failover.WithEndpoints(ctx, endpointsOf(req.Target, ""))
	return d.images.GenerateImage(ctx, req)
}

func endpointsOf(t provider.Target, session string) failover.Endpoints {
	return failover.Endpoints{
		Provider:  string(t.Provider),
		Primary:   t.BaseURL,
		Fallbacks: t.FallbackBaseURLs,
		Spread:    t.SpreadEndpoints,
		Session:   session,
	}
}

// sessionKey names the client conversation a request belongs to, so spread
// endpoints keep it on one instance. Ingress keeps only the session header
// (X-Session-Id, or session_id on the responses route) and drops body
// session fields, so after the header comes a digest of the first user
// message, the same fallback the upstream proxy uses.
func sessionKey(req provider.RunRequest) string {
	if v, ok := req.Facts.Forward.Get(execution.ForwardSessionID); ok && strings.TrimSpace(v) != "" {
		return "session:" + strings.TrimSpace(v)
	}
	if v := strings.TrimSpace(string(req.Facts.Session)); v != "" {
		return "session:" + v
	}
	for _, item := range req.Request.Input {
		msg, ok := item.(canon.Message)
		if !ok || msg.Role != canon.RoleUser {
			continue
		}
		h := sha256.New()
		text := false
		for _, c := range msg.Content {
			if t, ok := c.(canon.TextContent); ok && t.Text != "" {
				h.Write([]byte(t.Text))
				text = true
			}
		}
		if !text {
			return ""
		}
		return "first-user:" + hex.EncodeToString(h.Sum(nil))
	}
	return ""
}
