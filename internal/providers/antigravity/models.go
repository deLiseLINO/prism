package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
)

// discoverySnapshot is the last raw model list a successful FetchModels
// observed, shared across the process. The envelope consults it to resolve
// family wire models at request time: the Runner carries no presence, and a
// package-level snapshot populated by the same call that feeds config sync
// keeps raw member ids out of every other layer.
var discoverySnapshot struct {
	sync.Mutex
	present map[string]bool
}

// FetchModels lists the account's available models from the Antigravity
// model-discovery endpoint, the same fetchAvailableModels call the quota
// probe uses. The response keys the model ids the Cloud Code Assist backend
// will route for the credential's project.
//
// Effort-tier variants collapse here: sibling SKUs the backend lists
// separately (the family's tiered member ids) return as one
// logical id. The raw list stays in the package via the snapshot; the raw
// member ids are exposed to the management layer through RawModels.
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

// parseModelIDs decodes and collapses the discovery payload. The raw ids are
// recorded in the package snapshot before collapsing, so resolveWireModel
// and RawModels stay presence-faithful between syncs.
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
	rawIDs := make([]string, 0, len(payload.Models))
	for id := range payload.Models {
		if id != "" {
			rawIDs = append(rawIDs, id)
		}
	}
	if len(rawIDs) == 0 {
		return nil, fmt.Errorf("antigravity: models payload carries no model")
	}
	recordDiscovery(rawIDs)
	collapsed := collapseIds(rawIDs)
	sort.Strings(collapsed)
	return collapsed, nil
}

// recordDiscovery replaces the presence snapshot with a fresh raw list.
func recordDiscovery(raw []string) {
	present := make(map[string]bool, len(raw))
	for _, id := range raw {
		present[id] = true
	}
	discoverySnapshot.Lock()
	discoverySnapshot.present = present
	discoverySnapshot.Unlock()
}

// presenceSnapshot returns a copy of the current presence map; nil when no
// discovery has run. An empty (nil) presence map leaves every family inert,
// so a pre-sync request passes logical ids through unchanged.
func presenceSnapshot() map[string]bool {
	discoverySnapshot.Lock()
	defer discoverySnapshot.Unlock()
	if discoverySnapshot.present == nil {
		return nil
	}
	out := make(map[string]bool, len(discoverySnapshot.present))
	for id, ok := range discoverySnapshot.present {
		out[id] = ok
	}
	return out
}

// RawModels exposes the raw wire ids behind a logical model id: the live
// non-retired members of its family, resolved against the last discovery
// snapshot. Unknown or non-family ids return nil. This is the single
// accessor the management layer uses for the provider view's raw list.
func RawModels(logical string) []string {
	return rawMembersFor(logical, presenceSnapshot())
}

// ModelEfforts exposes the supported efforts behind a logical model id for
// the provider view; nil for non-family ids.
func ModelEfforts(logical string) []effort {
	return effortsFor(logical)
}
