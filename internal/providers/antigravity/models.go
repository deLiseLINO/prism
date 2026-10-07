package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type ModelCatalog struct{ present map[string]bool }

func NewModelCatalog(raw []string) ModelCatalog {
	present := make(map[string]bool, len(raw))
	for _, id := range raw {
		present[id] = true
	}
	return ModelCatalog{present: present}
}

func (c ModelCatalog) RawIDs() []string {
	ids := make([]string, 0, len(c.present))
	for id := range c.present {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (c ModelCatalog) Models() []string {
	ids := collapseIds(c.RawIDs())
	sort.Strings(ids)
	return ids
}

func (c ModelCatalog) RawModels(logical string) []string { return rawMembersFor(logical, c.present) }

func EndpointKey(baseURL string) string {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	return strings.TrimRight(u.String(), "/")
}

func FetchModels(ctx context.Context, client *http.Client, baseURL string, cred CredentialPair) (ModelCatalog, error) {
	if client == nil {
		return ModelCatalog{}, fmt.Errorf("antigravity: models fetch needs an http client")
	}
	if cred.AccessToken == "" {
		return ModelCatalog{}, fmt.Errorf("antigravity: models fetch needs an access token")
	}
	if cred.ProjectID == "" {
		return ModelCatalog{}, fmt.Errorf("antigravity: models fetch needs a project id")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]string{"project": cred.ProjectID})
	if err != nil {
		return ModelCatalog{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, quotaURL(baseURL), bytes.NewReader(body))
	if err != nil {
		return ModelCatalog{}, fmt.Errorf("antigravity: models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("User-Agent", RequestUserAgent())
	res, err := client.Do(req)
	if err != nil {
		return ModelCatalog{}, fmt.Errorf("antigravity: models fetch: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ModelCatalog{}, fmt.Errorf("antigravity: models fetch: status %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, quotaMaxBytes))
	if err != nil {
		return ModelCatalog{}, fmt.Errorf("antigravity: models read: %w", err)
	}
	return parseModelIDs(raw)
}

func parseModelIDs(raw []byte) (ModelCatalog, error) {
	var payload struct {
		Models map[string]struct {
			Internal bool `json:"isInternal"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ModelCatalog{}, fmt.Errorf("antigravity: models decode: %w", err)
	}
	if payload.Models == nil {
		return ModelCatalog{}, fmt.Errorf("antigravity: models payload has no models object")
	}
	rawIDs := make([]string, 0, len(payload.Models))
	for id, meta := range payload.Models {
		if id != "" && !discoveryDenylist[id] && !meta.Internal {
			rawIDs = append(rawIDs, id)
		}
	}
	return NewModelCatalog(rawIDs), nil
}

func DeniedModel(id string) bool { return discoveryDenylist[id] }

var discoveryDenylist = map[string]bool{"chat_20706": true, "chat_23310": true}

func ModelEfforts(logical string) []effort { return effortsFor(logical) }

func LogicalModel(raw string) string {
	for _, f := range families {
		if f.id == raw || containsString(f.members, raw) {
			return f.id
		}
	}
	if rev, ok := templateRevision(raw); ok {
		if f := geminiFlashTemplate.instantiate(rev); f != nil && (f.id == raw || containsString(f.members, raw)) {
			return f.id
		}
	}
	return ""
}
