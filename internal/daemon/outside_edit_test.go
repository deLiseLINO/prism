package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/server"
)

type countingUpstream struct {
	*httptest.Server
	hits atomic.Int32
	keys chan string
}

func newCountingUpstream(t *testing.T) *countingUpstream {
	t.Helper()
	u := &countingUpstream{keys: make(chan string, 16)}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		u.keys <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(u.Close)
	return u
}

func outsideEditRuntime(t *testing.T, edge, side string) (*daemonEnv, *config.Manager, http.Handler) {
	t.Helper()
	env, mgr := testEnv(t, config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"edge": {Wire: config.WireOpenAIChat, BaseURL: edge + "/v1", Models: []string{"model"}, APIKeyRef: "edge:default"},
		"side": {Wire: config.WireOpenAIChat, BaseURL: side + "/v1", Models: []string{"model"}, APIKeyRef: "side:default"},
	}})
	for _, id := range []string{"edge", "side"} {
		if err := env.creds.file.Put(context.Background(), account.ProviderID(id), account.AccountID(id+":default"), 1, []byte(id+"-key")); err != nil {
			t.Fatal(err)
		}
	}
	env.reconcileOnce(context.Background())
	infer := server.New(server.Options{Planner: server.NewConfigPlanner(mgr), Registry: env.registry, Pool: env.pool, Config: mgr}).Handler()
	return env, mgr, infer
}

func inferModel(h http.Handler, model string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model)))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

// handEditProvider rewrites prism.json the way a text editor does: it keeps
// the rest of the document, changes one provider field, and leaves the
// generation alone unless bump is set.
func handEditProvider(t *testing.T, env *daemonEnv, id, field, value string, bump bool) {
	t.Helper()
	path := filepath.Join(filepath.Dir(env.credentialPath), "prism.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if bump {
		file["generation"] = file["generation"].(float64) + 1
	}
	file["config"].(map[string]any)["providers"].(map[string]any)[id].(map[string]any)[field] = value
	edited, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOutsideEditOfConfigKeepsDaemonServing(t *testing.T) {
	for _, bump := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-generation", true: "bumped-generation"}[bump], func(t *testing.T) {
			oldEdge, newEdge, side := newCountingUpstream(t), newCountingUpstream(t), newCountingUpstream(t)
			env, mgr, infer := outsideEditRuntime(t, oldEdge.URL, side.URL)
			if out := inferModel(infer, "edge/model"); out.Code != http.StatusOK {
				t.Fatalf("before edit edge=%d %s", out.Code, out.Body)
			}
			handEditProvider(t, env, "edge", "baseURL", newEdge.URL+"/v1", bump)
			if out := inferModel(infer, "side/model"); out.Code != http.StatusOK {
				t.Fatalf("unrelated provider after outside edit=%d %s", out.Code, out.Body)
			}
			oldHits := oldEdge.hits.Load()
			for range 2 {
				if out := inferModel(infer, "edge/model"); out.Code != http.StatusOK {
					t.Fatalf("edited provider after outside edit=%d %s", out.Code, out.Body)
				}
			}
			if newEdge.hits.Load() != 2 || oldEdge.hits.Load() != oldHits {
				t.Fatalf("edited baseURL not served: new=%d old=%d", newEdge.hits.Load(), oldEdge.hits.Load()-oldHits)
			}
			if got := mgr.Get().Config.Providers["edge"].BaseURL; got != newEdge.URL+"/v1" {
				t.Fatalf("adopted baseURL=%q", got)
			}
		})
	}
}

func TestOutsideEditOfKeyReferenceWaitsForPublication(t *testing.T) {
	edge, side := newCountingUpstream(t), newCountingUpstream(t)
	env, _, infer := outsideEditRuntime(t, edge.URL, side.URL)
	ref, err := env.creds.Stage(context.Background(), "edge", "edge:default", []byte("replacement-key"))
	if err != nil {
		t.Fatal(err)
	}
	handEditProvider(t, env, "edge", "apiKeyRef", ref, false)
	if out := inferModel(infer, "side/model"); out.Code != http.StatusOK {
		t.Fatalf("unrelated provider after outside edit=%d %s", out.Code, out.Body)
	}
	if out := inferModel(infer, "edge/model"); out.Code == http.StatusOK || edge.hits.Load() != 0 {
		t.Fatalf("unpublished key reference dispatched: %d hits=%d", out.Code, edge.hits.Load())
	}
	env.reconcileOnce(context.Background())
	if out := inferModel(infer, "edge/model"); out.Code != http.StatusOK {
		t.Fatalf("published key reference=%d %s", out.Code, out.Body)
	}
	if key := <-edge.keys; key != "Bearer replacement-key" {
		t.Fatalf("dispatched %q", key)
	}
}
