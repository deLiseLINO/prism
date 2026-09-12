package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
)

// FetchModels lists the account's available models from the Antigravity
// model-discovery endpoint, the same fetchAvailableModels call the quota
// probe uses. The response keys the model ids the Cloud Code Assist backend
// will route for the credential's project.
func FetchModels(ctx context.Context, client *http.Client, baseURL string, cred CredentialPair) ([]string, error) {
	if client == nil {
		return nil, fmt.Errorf("antigravity: models fetch needs an http client")
	}
	if cred.AccessToken == "" {
		return nil, fmt.Errorf("antigravity: models fetch needs an access token")
	}
	if cred.ProjectID == "" {
		return nil, fmt.Errorf("antigravity: models fetch needs a project id")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]string{"project": cred.ProjectID})
	if err != nil {
		return nil, fmt.Errorf("antigravity: models request encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, quotaURL(baseURL), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("antigravity: models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("User-Agent", RequestUserAgent())
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("antigravity: models fetch: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("antigravity: models fetch: status %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, quotaMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("antigravity: models read: %w", err)
	}
	return parseModelIDs(raw)
}

func parseModelIDs(raw []byte) ([]string, error) {
	var payload struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("antigravity: models decode: %w", err)
	}
	if payload.Models == nil {
		return nil, fmt.Errorf("antigravity: models payload has no models object")
	}
	out := make([]string, 0, len(payload.Models))
	for id := range payload.Models {
		if id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("antigravity: models payload carries no model")
	}
	sort.Strings(out)
	return out, nil
}
