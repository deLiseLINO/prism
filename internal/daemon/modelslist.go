package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/providers/anthropic"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
)

func modelsRequest(ctx context.Context, p config.Provider, apiKey string) (*http.Request, error) {
	u, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil {
		return nil, err
	}
	u.Path = openaierr.APIBase(u.Path) + "/models"
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	switch p.Wire {
	case config.WireOpenAIChat, config.WireOpenAIResponses:
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	case config.WireAnthropicMessages:
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", anthropic.DefaultAPIVersion)
	default:
		return nil, fmt.Errorf("wire %q does not support custom model listing", p.Wire)
	}
	return req, nil
}

func parseOpenAIModelList(body []byte) ([]listedModel, error) {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Data) == 0 || envelope.Data[0] != '[' {
		return nil, fmt.Errorf("malformed models response")
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(envelope.Data, &rows); err != nil {
		return nil, fmt.Errorf("malformed models response")
	}
	out := make([]listedModel, 0, len(rows))
	for _, raw := range rows {
		row, ok := parseOpenAIModel(raw)
		if !ok {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func parseOpenAIModel(raw json.RawMessage) (listedModel, bool) {
	var row struct {
		ID              string          `json:"id"`
		MaxModelLen     json.RawMessage `json:"max_model_len"`
		ContextLength   json.RawMessage `json:"context_length"`
		Input           json.RawMessage `json:"input"`
		InputModalities json.RawMessage `json:"input_modalities"`
		Architecture    json.RawMessage `json:"architecture"`
	}
	if err := json.Unmarshal(raw, &row); err != nil || row.ID == "" {
		return listedModel{}, false
	}
	return listedModel{
		ID:            row.ID,
		ContextWindow: openAIWindow(row.MaxModelLen, row.ContextLength),
		Image:         openAIImage(row.Input, row.InputModalities, architectureModalities(row.Architecture)),
	}, true
}

func architectureModalities(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var arch struct {
		InputModalities json.RawMessage `json:"input_modalities"`
	}
	if err := json.Unmarshal(raw, &arch); err != nil {
		return json.RawMessage(`"bad"`)
	}
	return arch.InputModalities
}

func openAIWindow(maxModelLen, contextLength json.RawMessage) *int {
	if n := positiveNumber(maxModelLen); n != nil {
		return n
	}
	return positiveNumber(contextLength)
}

func positiveNumber(raw json.RawMessage) *int {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil || n <= 0 || n != float64(int(n)) {
		return nil
	}
	v := int(n)
	return &v
}

func openAIImage(fields ...json.RawMessage) *bool {
	saw := false
	on := false
	for _, raw := range fields {
		present, hasImage, ok := modalityField(raw)
		if !ok || !present {
			continue
		}
		saw = true
		if hasImage {
			on = true
		}
	}
	if !saw {
		return nil
	}
	return &on
}

func modalityField(raw json.RawMessage) (present, hasImage, ok bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, false, true
	}
	var modalities []string
	if err := json.Unmarshal(raw, &modalities); err != nil {
		return false, false, false
	}
	for _, modality := range modalities {
		if modality == "image" {
			return true, true, true
		}
	}
	return true, false, true
}
