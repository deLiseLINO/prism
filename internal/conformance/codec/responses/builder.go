package responses

import (
	"context"
	"encoding/json"
	"strings"

	"prism/internal/canon"
	"prism/internal/conformance"
)

type Builder struct{}

func (Builder) Build(ctx context.Context, req canon.Request, opts conformance.BuildOptions) (*conformance.UpstreamRequest, error) {
	input, warnings, err := inputFrom(req.Input)
	if err != nil {
		return nil, err
	}
	body := body{Model: req.Model, Input: input, Stream: req.Stream}
	var systemParts []string
	for _, c := range req.Instructions {
		if t, ok := c.(canon.TextContent); ok {
			systemParts = append(systemParts, t.Text)
		}
	}
	if len(systemParts) > 0 {
		body.Instructions = strings.Join(systemParts, "\n\n")
	}
	if req.MaxOutputTokens > 0 {
		body.MaxOutputTokens = &req.MaxOutputTokens
	}
	if req.Sampling.Temperature != nil {
		body.Temperature = req.Sampling.Temperature
	}
	if req.Sampling.TopP != nil {
		body.TopP = req.Sampling.TopP
	}
	if req.Sampling.Stop != nil {
		body.Stop = req.Sampling.Stop
	}
	switch req.Sampling.ServiceTier {
	case canon.TierFlex:
		s := "flex"
		body.ServiceTier = &s
	case canon.TierPriority:
		s := "priority"
		body.ServiceTier = &s
	}
	if effort := effortWire(req.Reasoning.Effort); effort != "" {
		body.Reasoning = &reasoning{Effort: &effort}
	}
	if len(req.Tools) > 0 {
		body.Tools, err = toolsFrom(req.Tools)
		if err != nil {
			return nil, err
		}
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
		URL:    conformance.ResponsesURL(opts.BaseURL),
		Headers: conformance.Headers{
			{Name: "Content-Type", Value: "application/json"},
			{Name: "Authorization", Value: "Bearer " + opts.APIKey},
		},
		Body:     raw,
		Warnings: warnings,
	}, nil
}
