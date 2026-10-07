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
