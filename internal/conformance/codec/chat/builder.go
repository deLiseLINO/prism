package chat

import (
	"context"
	"encoding/json"

	"prism/internal/canon"
	"prism/internal/conformance"
)

type Builder struct{}

func (Builder) Build(ctx context.Context, req canon.Request, opts conformance.BuildOptions) (*conformance.UpstreamRequest, error) {
	body := body{Model: req.Model, Messages: messagesFrom(req.Input), Stream: req.Stream}
	if req.Sampling.Temperature != nil {
		body.Temperature = req.Sampling.Temperature
	}
	if req.Sampling.TopP != nil {
		body.TopP = req.Sampling.TopP
	}
	if req.Sampling.Stop != nil {
		body.Stop = req.Sampling.Stop
	}
	if req.Sampling.ParallelToolCalls != nil {
		body.ParallelToolCalls = req.Sampling.ParallelToolCalls
	}
	switch req.Sampling.ServiceTier {
	case canon.TierFlex:
		s := "flex"
		body.ServiceTier = &s
	case canon.TierPriority:
		s := "priority"
		body.ServiceTier = &s
	}
	if req.MaxOutputTokens > 0 {
		body.MaxTokens = &req.MaxOutputTokens
	}
	if effort := effortWire(req.Reasoning.Effort); effort != "" {
		body.ReasoningEffort = &effort
	}
	if len(req.Tools) > 0 {
		body.Tools = toolsFrom(req.Tools)
	}
	if tc, ok := toolChoiceFrom(req.ToolChoice); ok {
		body.ToolChoice = tc
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &conformance.UpstreamRequest{
		Method: "POST",
		URL:    conformance.ChatCompletionsURL(opts.BaseURL),
		Headers: conformance.Headers{
			{Name: "Content-Type", Value: "application/json"},
			{Name: "Authorization", Value: "Bearer " + opts.APIKey},
		},
		Body: raw,
	}, nil
}
