package daemon

import (
	"context"
	"fmt"

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
	return d.images.GenerateImage(ctx, req)
}
