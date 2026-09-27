package cline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type recommendedModelsResponse struct {
	Free []struct {
		ID string `json:"id"`
	} `json:"free"`
	ClinePass []struct {
		ID string `json:"id"`
	} `json:"clinePass"`
}

func FetchModels(ctx context.Context, client *http.Client, baseURL, bearer string) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	url := GatewayBase(baseURL) + "/api/v1/ai/cline/recommended-models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	for k, v := range ProductHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cline model catalog returned status %d", resp.StatusCode)
	}
	var out recommendedModelsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("cline model catalog returned invalid JSON")
	}
	models := make([]string, 0, len(out.Free)+len(out.ClinePass))
	for _, m := range out.Free {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	for _, m := range out.ClinePass {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}
