package customresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
)

type responsePayload struct {
	Status            string           `json:"status"`
	IncompleteDetails *incompleteWire  `json:"incomplete_details"`
	Output            []map[string]any `json:"output"`
}

type incompleteWire struct {
	Reason string `json:"reason"`
}

func (r *Runner) runAggregate(body io.Reader, sink provider.Sink) error {
	raw, err := io.ReadAll(io.LimitReader(body, 100<<20))
	if err != nil {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, err)
	}
	var payload responsePayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, fmt.Errorf("customresponses: malformed upstream response: %w", err))
	}
	var fields map[string]any
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil || decoder.Decode(new(any)) != io.EOF || fields == nil {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, errors.New("customresponses: malformed upstream response"))
	}
	if fields["status"] == nil && fields["output"] == nil && fields["error"] == nil {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, errors.New("customresponses: missing terminal shape"))
	}
	usage, err := decodeResponseUsage(fields, canon.Usage{})
	if err != nil {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, err)
	}
	if output, supplied := fields["output"]; supplied {
		if _, ok := output.([]any); !ok {
			return runError(provider.Retryable, provider.ClassTransport, true, true, 0, errors.New("customresponses: output must be an array"))
		}
	}
	if payload.Status != "" && payload.Status != "completed" && payload.Status != "incomplete" && payload.Status != "failed" {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, errors.New("customresponses: response has no terminal status"))
	}
	if payload.Status == "incomplete" && (payload.IncompleteDetails == nil || payload.IncompleteDetails.Reason != "max_output_tokens" && payload.IncompleteDetails.Reason != "content_filter") {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, errors.New("customresponses: interrupted upstream completion"))
	}
	items := make([]canon.Item, 0, len(payload.Output))
	for _, wire := range payload.Output {
		if payload.Status == "incomplete" && (wire["type"] == "function_call" || wire["type"] == "custom_tool_call" || wire["type"] == "local_shell_call") && wire["status"] != "completed" {
			return runError(provider.Retryable, provider.ClassTransport, true, true, 0, errors.New("customresponses: incomplete tool snapshot"))
		}
		if err := validateFinishedItem(wire); err != nil {
			return runError(provider.Retryable, provider.ClassTransport, true, true, 0, err)
		}
		item, _, err := itemFromWire(wire)
		if err != nil {
			return runError(provider.Retryable, provider.ClassTransport, true, true, 0, err)
		}
		items = append(items, item)
	}
	emit := func(ev canon.Event) error {
		if err := sink.Emit(ev); err != nil {
			return runError(provider.UnsafeReplay, provider.ClassTransport, true, false, 0, err)
		}
		return nil
	}
	if fields["error"] != nil || payload.Status == "failed" {
		parsed, ok := openaierr.Parse(raw)
		if !ok {
			return runError(provider.Retryable, provider.ClassTransport, true, true, 0, errors.New("customresponses: failed response carried no error value"))
		}
		copied := parsed
		message := openaierr.Text(parsed)
		if err := emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message, Provider: &copied}, Usage: usage}); err != nil {
			return err
		}
		cause := message
		if cause == "" {
			cause = "provider error"
		}
		return provider.RunError{Kind: provider.TerminalEmitted, Class: openaierr.ClassForInband(parsed, provider.ClassServer), Accepted: true, Cause: errors.New(cause), Reported: &copied}
	}
	for _, item := range items {
		if err := emit(canon.ItemStarted{Item: item}); err != nil {
			return err
		}
		if err := emit(canon.ItemFinished{Item: item}); err != nil {
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
