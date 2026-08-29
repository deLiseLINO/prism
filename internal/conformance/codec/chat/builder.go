package chat

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"prism/internal/canon"
	"prism/internal/conformance"
)

type jsonSchemaSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type Builder struct{}

func (Builder) Build(ctx context.Context, req canon.Request, opts conformance.BuildOptions) (*conformance.UpstreamRequest, error) {
	messages, warnings, err := messagesFrom(req.Input)
	if err != nil {
		return nil, err
	}
	if sys := systemMessage(req.Instructions); sys != nil {
		messages = append([]message{*sys}, messages...)
	}
	body := body{Model: req.Model, Messages: messages, Stream: req.Stream}
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
	if req.Sampling.PresencePenalty != nil {
		body.PresencePenalty = req.Sampling.PresencePenalty
	}
	if req.Sampling.FrequencyPenalty != nil {
		body.FrequencyPenalty = req.Sampling.FrequencyPenalty
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
		if wire, ok := mapReasoningEffort(opts.ReasoningEffortMap, effort); ok {
			if opts.ReasoningWireFormat == "gateway-object" && !nativeOpenAI(opts.BaseURL) {
				body.Reasoning = &reasoningWire{Enabled: true, Effort: wire}
			} else {
				body.ReasoningEffort = &wire
			}
		}
	}
	if len(req.Tools) > 0 {
		tools := filterToolsByChoice(req.Tools, req.ToolChoice)
		if len(tools) > 0 {
			body.Tools, err = toolsFrom(tools)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(body.Tools) > 0 {
		if tc, ok := toolChoiceFrom(req.ToolChoice); ok {
			body.ToolChoice = tc
		}
	}
	if rf, ok := responseFormat(req.Text.Format); ok {
		body.ResponseFormat = rf
	}
	if req.Stream {
		body.StreamOptions = json.RawMessage(`{"include_usage":true}`)
	}
	if req.Text.Format != nil {
		body.ResponseFormat, err = responseFormatWire(req.Text.Format)
		if err != nil {
			return nil, err
		}
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
		Body:     raw,
		Warnings: warnings,
	}, nil
}

func systemMessage(instructions []canon.Content) *message {
	var parts []string
	for _, c := range instructions {
		if t, ok := c.(canon.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return &message{Role: "system", Content: strings.Join(parts, "\n\n")}
}

func responseFormat(f *canon.TextFormat) (json.RawMessage, bool) {
	if f == nil {
		return nil, false
	}
	switch f.Type {
	case "json_object":
		return json.RawMessage(`{"type":"json_object"}`), true
	case "json_schema":
		spec := jsonSchemaSpec{Name: "response"}
		if f.Name != "" {
			spec.Name = f.Name
		}
		if f.Description != "" {
			spec.Description = f.Description
		}
		if len(f.Schema) > 0 {
			spec.Schema = f.Schema
		}
		if f.Strict != nil {
			spec.Strict = f.Strict
		}
		raw, err := json.Marshal(struct {
			Type       string         `json:"type"`
			JSONSchema jsonSchemaSpec `json:"json_schema"`
		}{Type: "json_schema", JSONSchema: spec})
		if err != nil {
			return nil, false
		}
		return raw, true
	default:
		return nil, false
	}
}

func nativeOpenAI(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "api.openai.com"
}
