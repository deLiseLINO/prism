package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/customimages"
	"github.com/deLiseLINO/prism/internal/routing"
)

type imageRunner struct {
	*fakeRunner
	*customimages.Generator
}

func imageDoc(baseURL string) config.Document {
	return config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"gw": {
				Wire:      config.WireOpenAIChat,
				BaseURL:   baseURL + "/v1",
				APIKeyRef: "gw-key",
				Models:    []string{"chat-a", "img-model-a"},
				Discovered: map[string]config.DiscoveredFacts{
					"img-model-a": {Kind: config.ModelKindImage},
				},
			},
		},
	}
}

// newImageServer wires the images route to a real generator aimed at the
// given upstream, so the test covers request mapping end to end.
func newImageServer(t *testing.T, upstream *httptest.Server) *harness {
	t.Helper()
	resolve := func(ctx context.Context, target provider.Target, lease account.Lease) (string, error) {
		if target.APIKeyRef != "gw-key" || lease.Account != "gw:default" {
			t.Errorf("credential resolved for %q lease %+v", target.APIKeyRef, lease)
		}
		return "sk-upstream", nil
	}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{}, func(reg *provider.Registry) {
		if err := reg.Register("gw", imageRunner{fakeRunner: &fakeRunner{}, Generator: customimages.New(resolve, upstream.Client())}); err != nil {
			t.Fatal(err)
		}
	})
	h.pool.(interface{ Register(account.Account) }).Register(activeAccount("gw:default", "gw"))
	if _, err := h.cfg.Update(imageDoc(upstream.URL), 0); err != nil {
		t.Fatalf("update config: %v", err)
	}
	return h
}

func TestImageGenerationsForwardsAndRelays(t *testing.T) {
	const answer = `{"created":1767225600,"data":[{"b64_json":"iVBORw0KGgo="}],"model":"img-model-a"}`
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" {
			t.Errorf("upstream request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-upstream" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("upstream body %s: %v", raw, err)
		}
		if body["model"] != "img-model-a" || body["prompt"] != "a red cube" || body["size"] != "1024x1024" || body["n"] != float64(1) {
			t.Errorf("upstream body = %v", body)
		}
		images, _ := body["images"].([]any)
		if len(images) != 1 {
			t.Errorf("reference images not forwarded: %v", body["images"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	}))
	defer upstream.Close()
	h := newImageServer(t, upstream)

	rec := postJSON(t, h, "/v1/images/generations", `{"model":"gw/img-model-a","prompt":"a red cube","n":1,"size":"1024x1024","response_format":"b64_json","images":[{"image_url":"data:image/png;base64,iVBORw0KGgo="}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != answer {
		t.Fatalf("body = %s, want upstream body verbatim", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestImageGenerationsRelaysUpstreamErrorWithoutRetry(t *testing.T) {
	const failure = `{"error":{"message":"slow down","type":"rate_limit_error"}}`
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, failure)
	}))
	defer upstream.Close()
	h := newImageServer(t, upstream)

	rec := postJSON(t, h, "/v1/images/generations", `{"model":"gw/img-model-a","prompt":"a red cube"}`)
	if rec.Code != http.StatusTooManyRequests || rec.Body.String() != failure {
		t.Fatalf("status = %d body %s, want upstream 429 relayed", rec.Code, rec.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want exactly one paid attempt", calls.Load())
	}
}

func TestImageGenerationsRejectsBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer upstream.Close()
	h := newImageServer(t, upstream)

	cases := []struct {
		name   string
		body   string
		status int
		code   string
		param  string
	}{
		{"missing model", `{"prompt":"a red cube"}`, http.StatusBadRequest, "invalid_request", "model"},
		{"missing prompt", `{"model":"gw/img-model-a"}`, http.StatusBadRequest, "invalid_request", "prompt"},
		{"several images", `{"model":"gw/img-model-a","prompt":"x","n":2}`, http.StatusBadRequest, "invalid_request", "n"},
		{"url format", `{"model":"gw/img-model-a","prompt":"x","response_format":"url"}`, http.StatusBadRequest, "invalid_request", "response_format"},
		{"stream", `{"model":"gw/img-model-a","prompt":"x","stream":true}`, http.StatusBadRequest, "invalid_request", "stream"},
		{"chat model", `{"model":"gw/chat-a","prompt":"x"}`, http.StatusNotFound, "model_not_found", "model"},
		{"unknown model", `{"model":"gw/img-model-z","prompt":"x"}`, http.StatusNotFound, "model_not_found", "model"},
		{"no provider", `{"model":"img-model-a","prompt":"x"}`, http.StatusNotFound, "model_not_found", "model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postJSON(t, h, "/v1/images/generations", tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d body %s, want %d", rec.Code, rec.Body.String(), tc.status)
			}
			var env chatErrorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("body %s: %v", rec.Body.String(), err)
			}
			if env.Error.Code != tc.code || env.Error.Param != tc.param || env.Error.Type != "invalid_request_error" || env.Error.Message == "" {
				t.Fatalf("error = %+v", env.Error)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want none", calls.Load())
	}
}

func TestModelsListsImageModelsWithOutputModalities(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	h := newImageServer(t, upstream)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, row := range list.Data {
		byID[row["id"].(string)] = row
	}
	img, ok := byID["gw/img-model-a"]
	if !ok {
		t.Fatalf("image model not listed: %s", rec.Body.String())
	}
	if mods, _ := img["output_modalities"].([]any); len(mods) != 1 || mods[0] != "image" || img["owned_by"] != "gw" || img["object"] != "model" {
		t.Fatalf("image row = %v", img)
	}
	if _, ok := byID["claude-gw--img-model-a"]; ok {
		t.Fatalf("image model offered as a chat alias: %s", rec.Body.String())
	}
	chat, ok := byID["claude-gw--chat-a"]
	if !ok {
		t.Fatalf("chat alias missing: %s", rec.Body.String())
	}
	if _, marked := chat["output_modalities"]; marked {
		t.Fatalf("chat row carries output_modalities: %v", chat)
	}
	if strings.Contains(rec.Body.String(), `"gw/chat-a"`) {
		t.Fatalf("chat model listed under its plain id: %s", rec.Body.String())
	}
}

func TestPlannerRefusesImageModelsForChat(t *testing.T) {
	doc := imageDoc("http://up.example")
	p := &ConfigPlanner{get: func() config.Document { return doc }}
	if _, ok := p.Plan("gw/img-model-a"); ok {
		t.Fatal("image model planned as a chat target")
	}
	if _, ok := p.Plan("claude-gw--img-model-a"); ok {
		t.Fatal("image model planned through its claude alias")
	}
	if plan, ok := p.Plan("gw/chat-a"); !ok || len(plan.Targets) != 1 {
		t.Fatalf("chat model plan = %+v ok=%v", plan, ok)
	}
	target, err := imageTargetFor(doc, "gw", "img-model-a")
	if err != nil || target.Model != "img-model-a" || target.APIKeyRef != "gw-key" {
		t.Fatalf("image target = %+v err=%v", target, err)
	}
	if _, err := imageTargetFor(doc, "gw", "chat-a"); err == nil {
		t.Fatal("chat model resolved as an image target")
	}
}
