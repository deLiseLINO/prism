package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const Endpoint = "https://models.dev/api.json"

const cacheName = "model-catalog.json"

type Facts struct {
	ContextWindow int   `json:"contextWindow,omitempty"`
	Image         *bool `json:"image,omitempty"`
}

type Index struct {
	mu   sync.RWMutex
	dir  string
	rows map[string]Facts
}

func Empty() *Index {
	return &Index{rows: map[string]Facts{}}
}

func Open(dir string) (*Index, error) {
	x := &Index{dir: dir, rows: map[string]Facts{}}
	if dir == "" {
		return x, nil
	}
	b, err := os.ReadFile(filepath.Join(dir, cacheName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return x, nil
		}
		return x, nil
	}
	rows, err := decodeCache(b)
	if err != nil {
		return x, nil
	}
	x.rows = rows
	return x, nil
}

func (x *Index) Lookup(modelID string) Facts {
	if x == nil || modelID == "" {
		return Facts{}
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.rows[modelID]
}

func (x *Index) Refresh(ctx context.Context, client *http.Client) error {
	if x == nil {
		return errors.New("catalog: nil index")
	}
	if client == nil {
		client = http.DefaultClient
	}
	body, err := fetch(ctx, client)
	if err != nil {
		return err
	}
	rows, err := Parse(body)
	if err != nil {
		return err
	}
	if err := x.store(rows); err != nil {
		return err
	}
	return nil
}

func fetch(ctx context.Context, client *http.Client) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("catalog: %s: %s", Endpoint, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func (x *Index) store(rows map[string]Facts) error {
	if x.dir != "" {
		if err := writeCache(filepath.Join(x.dir, cacheName), rows); err != nil {
			return err
		}
	}
	x.mu.Lock()
	x.rows = rows
	x.mu.Unlock()
	return nil
}

type vote struct {
	window    int
	windowSet bool
	image     bool
	imageSet  bool
}

func (v *vote) add(row Facts) {
	if row.ContextWindow > 0 {
		if !v.windowSet {
			v.window = row.ContextWindow
			v.windowSet = true
		} else if v.window != row.ContextWindow {
			v.window = 0
		}
	}
	if row.Image != nil {
		if !v.imageSet {
			v.image = *row.Image
			v.imageSet = true
		} else if v.image != *row.Image {
			v.imageSet = false
			v.image = false
		}
	}
}

func (v vote) facts() Facts {
	out := Facts{}
	if v.windowSet && v.window > 0 {
		out.ContextWindow = v.window
	}
	if v.imageSet {
		on := v.image
		out.Image = &on
	}
	return out
}

func Parse(body []byte) (map[string]Facts, error) {
	var providers map[string]json.RawMessage
	if err := json.Unmarshal(body, &providers); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if providers == nil {
		return nil, errors.New("catalog: body is not an object")
	}
	acc := map[string]*vote{}
	for _, raw := range providers {
		var provider struct {
			Models map[string]json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(raw, &provider); err != nil {
			continue
		}
		for key, modelRaw := range provider.Models {
			row, ok := parseModel(key, modelRaw)
			if !ok {
				continue
			}
			for _, match := range matchKeys(row.id) {
				v := acc[match]
				if v == nil {
					v = &vote{}
					acc[match] = v
				}
				v.add(row.facts)
			}
		}
	}
	out := make(map[string]Facts, len(acc))
	for id, v := range acc {
		facts := v.facts()
		if facts.ContextWindow == 0 && facts.Image == nil {
			continue
		}
		out[id] = facts
	}
	return out, nil
}

type parsedModel struct {
	id    string
	facts Facts
}

func parseModel(key string, raw json.RawMessage) (parsedModel, bool) {
	var row struct {
		ID    string `json:"id"`
		Limit struct {
			Context json.RawMessage `json:"context"`
		} `json:"limit"`
		Modalities struct {
			Input json.RawMessage `json:"input"`
		} `json:"modalities"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return parsedModel{}, false
	}
	id := row.ID
	if id == "" {
		id = key
	}
	if id == "" {
		return parsedModel{}, false
	}
	return parsedModel{id: id, facts: Facts{
		ContextWindow: windowVote(row.Limit.Context),
		Image:         imageVote(row.Modalities.Input),
	}}, true
}

func windowVote(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil || n <= 0 || n != float64(int(n)) {
		return 0
	}
	return int(n)
}

func imageVote(raw json.RawMessage) *bool {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	on := false
	for _, item := range list {
		if item == "image" {
			on = true
			break
		}
	}
	return &on
}

func matchKeys(id string) []string {
	if i := strings.LastIndex(id, "/"); i >= 0 && i < len(id)-1 {
		tail := id[i+1:]
		if tail != id {
			return []string{id, tail}
		}
	}
	return []string{id}
}

func decodeCache(b []byte) (map[string]Facts, error) {
	var rows map[string]Facts
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		rows = map[string]Facts{}
	}
	return rows, nil
}

func writeCache(path string, rows map[string]Facts) error {
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, cacheName+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
