package main

import (
	"context"
	"fmt"

	"prism/internal/provider"
)

type wireDispatcher struct {
	responses provider.Runner
	chat      provider.Runner
	messages  provider.Runner
}

func (d wireDispatcher) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	switch req.Target.Wire {
	case provider.WireResponses:
		return d.responses.Run(ctx, req, sink)
	case provider.WireChat:
		return d.chat.Run(ctx, req, sink)
	case provider.WireMessages:
		return d.messages.Run(ctx, req, sink)
	default:
		return fmt.Errorf("prismd: provider %q has no runner for wire %d", req.Target.Provider, req.Target.Wire)
	}
}
