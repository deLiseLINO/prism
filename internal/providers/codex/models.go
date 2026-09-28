package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

func modelsURL(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = BaseURL
	}
	return strings.TrimRight(baseURL, "/") + "/models?client_version=0.0.0"
}

const modelsTimeout = 8 * time.Second

const modelsMaxBytes = 512 << 10

// FetchModels lists the account's usable models from the Codex backend
// models endpoint (the same one codex-rs hits: GET {base}/models with a
// client_version query). Only entries the backend marks visible are
// returned; hidden entries are aliases or internal models the picker must
// not offer. An empty baseURL selects the production ChatGPT backend.
func FetchModels(ctx context.Context, client *http.Client, baseURL string, cred Credential) ([]string, error) {
	rows, err := FetchModelList(ctx, client, baseURL, cred)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out, nil
}

type ListedModel struct {
	ID            string
	ContextWindow *int
	Image         *bool
}

func FetchModelList(ctx context.Context, client *http.Client, baseURL string, cred Credential) ([]ListedModel, error) {
	if client == nil {
		return nil, fmt.Errorf("codex: models fetch needs an http client")
	}
	if cred.AccessToken == "" {
		return nil, fmt.Errorf("codex: models fetch needs an access token")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = BaseURL
	}
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL(baseURL), nil)
	if err != nil {
		return nil, fmt.Errorf("codex: models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	if cred.ChatGPTAccountID != "" {
		req.Header.Set(HeaderChatGPTAccountID, cred.ChatGPTAccountID)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex: models fetch: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codex: models fetch: status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, modelsMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("codex: models read: %w", err)
	}
	return parseModelList(body)
}

type modelsResponse struct {
	Models []codexModelRow `json:"models"`
}

type codexModelRow struct {
	Slug            string          `json:"slug"`
	Visibility      string          `json:"visibility"`
	ContextWindow   json.RawMessage `json:"context_window"`
	InputModalities json.RawMessage `json:"input_modalities"`
}

func parseModels(body []byte) ([]string, error) {
	rows, err := parseModelList(body)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out, nil
}

func parseModelList(body []byte) ([]ListedModel, error) {
	var payload modelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("codex: models decode: %w", err)
	}
	out := make([]ListedModel, 0, len(payload.Models))
	for _, m := range payload.Models {
		if m.Slug == "" || m.Visibility != "list" {
			continue
		}
		out = append(out, ListedModel{
			ID:            m.Slug,
			ContextWindow: positiveInt(m.ContextWindow),
			Image:         imageFromModalities(m.InputModalities),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("codex: models payload carries no visible model")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func positiveInt(raw json.RawMessage) *int {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil || n <= 0 {
		return nil
	}
	return &n
}

func imageFromModalities(raw json.RawMessage) *bool {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var modalities []string
	if err := json.Unmarshal(raw, &modalities); err != nil {
		return nil
	}
	on := false
	for _, modality := range modalities {
		if modality == "image" {
			on = true
			break
		}
	}
	return &on
}
