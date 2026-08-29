package customresponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/provider"
)

type responsePayload struct {
	Status            string           `json:"status"`
	Error             *errorWire       `json:"error"`
	IncompleteDetails *incompleteWire  `json:"incomplete_details"`
	Output            []map[string]any `json:"output"`
	Usage             usageWire        `json:"usage"`
}

type errorWire struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type incompleteWire struct {
	Reason string `json:"reason"`
}

type usageWire struct {
	InputTokens        float64 `json:"input_tokens"`
	InputTokensDetails *struct {
		CachedTokens float64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens        float64 `json:"output_tokens"`
	OutputTokensDetails *struct {
		ReasoningTokens float64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	TotalTokens float64 `json:"total_tokens"`
}

func (r *Runner) runAggregate(body io.Reader, sink provider.Sink) error {
	raw, err := io.ReadAll(io.LimitReader(body, 100<<20))
	if err != nil {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, err)
	}
	var payload responsePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, fmt.Errorf("customresponses: malformed upstream response: %w", err))
	}
	emit := func(ev canon.Event) error {
		if err := sink.Emit(ev); err != nil {
			return runError(provider.UnsafeReplay, provider.ClassTransport, true, false, 0, err)
		}
		return nil
	}
	usage := canon.Usage{
		InputTokens:       int64(payload.Usage.InputTokens),
		CachedInputTokens: int64(payload.Usage.InputTokensDetails.CachedTokens),
		OutputTokens:      int64(payload.Usage.OutputTokens),
		ReasoningTokens:   int64(payload.Usage.OutputTokensDetails.ReasoningTokens),
		TotalTokens:       int64(payload.Usage.TotalTokens),
	}
	if payload.Error != nil || payload.Status == "failed" {
		message := "upstream request failed"
		if payload.Error != nil && payload.Error.Message != "" {
			message = payload.Error.Message
		}
		if err := emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message}, Usage: usage}); err != nil {
			return err
		}
		return runError(provider.TerminalEmitted, provider.ClassServer, true, false, 0, errors.New(message))
	}
	for _, item := range payload.Output {
		canonItem, _, err := itemFromWire(item)
		if err != nil {
			return err
		}
		if err := emit(canon.ItemStarted{Item: canonItem}); err != nil {
			return err
		}
		if err := emit(canon.ItemFinished{Item: canonItem}); err != nil {
			return err
		}
	}
	switch payload.Status {
	case "completed", "":
		return emit(canon.TurnFinished{Status: canon.Completed(), Usage: usage})
	case "incomplete":
		reason := ""
		if payload.IncompleteDetails != nil {
			reason = payload.IncompleteDetails.Reason
		}
		var status canon.Status
		switch reason {
		case "max_output_tokens":
			status = canon.Incomplete(canon.IncompleteMaxOutputTokens)
		case "content_filter":
			status = canon.Incomplete(canon.IncompleteContentFilter)
		default:
			message := fmt.Sprintf("upstream stream ended early (%s)", reason)
			if err := emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message}, Usage: usage}); err != nil {
				return err
			}
			return runError(provider.TerminalEmitted, provider.ClassServer, true, false, 0, errors.New(message))
		}
		return emit(canon.TurnFinished{Status: status, Usage: usage})
	default:
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, fmt.Errorf("customresponses: unexpected upstream response status %q", payload.Status))
	}
}

func (r *Runner) Catalog(target provider.Target, lease account.Lease) provider.Catalog {
	if len(r.models) > 0 {
		return staticCatalog(r.models)
	}
	return &liveCatalog{runner: r, target: target, lease: lease}
}

type staticCatalog []provider.Model

func (s staticCatalog) Models(ctx context.Context) ([]provider.Model, error) {
	return s, nil
}

type liveCatalog struct {
	runner *Runner
	target provider.Target
	lease  account.Lease
}

func (c *liveCatalog) Models(ctx context.Context) ([]provider.Model, error) {
	key, err := c.runner.resolve(ctx, c.target, c.lease)
	if err != nil {
		return nil, runError(provider.Retryable, provider.ClassTransport, false, true, 0, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL(c.target.BaseURL), nil)
	if err != nil {
		return nil, runError(provider.Retryable, provider.ClassInvalidRequest, false, true, 0, err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	for _, h := range c.runner.extra {
		req.Header.Set(h.Name, h.Value)
	}
	client := c.runner.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, runError(provider.Retryable, provider.ClassTransport, false, true, 0, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, c.runner.httpError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, runError(provider.Retryable, provider.ClassTransport, true, true, 0, err)
	}
	ids, err := modelIDsFrom(raw)
	if err != nil {
		return nil, runError(provider.Retryable, provider.ClassTransport, true, true, 0, err)
	}
	out := make([]provider.Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, provider.Model{ID: canon.ModelID(id)})
	}
	return out, nil
}

func modelIDsFrom(raw []byte) ([]string, error) {
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Data != nil {
		ids := make([]string, 0, len(envelope.Data))
		for _, row := range envelope.Data {
			ids = append(ids, row.ID)
		}
		return ids, nil
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, errors.New("customresponses: malformed models response")
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}
