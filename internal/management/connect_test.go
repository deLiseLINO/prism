package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/connectlink"
)

const connectKey = "sk-connect-secret-value"

func connectLink(models ...string) connectlink.Link {
	return connectlink.Link{
		ID:        "edge",
		Wire:      "responses",
		Endpoints: []string{"http://10.0.0.1:8080/v1", "http://10.0.0.2:8080/v1"},
		Spread:    true,
		Models:    models,
		Key:       connectKey,
	}
}

// postConnect sends one connect request and fails the test when the key
// shows up in the response body.
func postConnect(t *testing.T, env *testEnv, link string, dryRun bool) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(ConnectRequest{Link: link, DryRun: dryRun})
	if err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, http.MethodPost, "/api/v1/connect", string(body))
	if strings.Contains(rec.Body.String(), connectKey) {
		t.Fatalf("response contains the key: %s", rec.Body.String())
	}
	return rec
}

func TestConnectDryRunWritesNothing(t *testing.T) {
	env := newEnv(t)
	before := env.cfg.Get().Generation
	rec := postConnect(t, env, connectLink("m1").String(), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[ConnectResponse](t, rec)
	want := ConnectPlan{
		ID:               "edge",
		Wire:             "responses",
		BaseURL:          "http://10.0.0.1:8080/v1",
		FallbackBaseURLs: []string{"http://10.0.0.2:8080/v1"},
		EndpointMode:     "spread",
		Models:           []string{"m1"},
		KeyPreview:       "sk-c…",
		HasKey:           true,
	}
	if got.Applied || got.Provider != nil || got.Generation != before || !reflect.DeepEqual(got.Plan, want) {
		t.Fatalf("dry run = %+v, want plan %+v", got, want)
	}
	snap := env.cfg.Get()
	if snap.Generation != before || len(snap.Config.Providers) != 0 || len(env.creds.staged) != 0 || len(env.creds.store) != 0 {
		t.Fatalf("dry run wrote: generation %d, providers %v, staged %v, store %v", snap.Generation, snap.Config.Providers, env.creds.staged, env.creds.store)
	}

	if _, err := env.cfg.Update(config.Document{
		Version:   config.SchemaVersion,
		Providers: map[string]config.Provider{"edge": {Wire: config.WireOpenAIChat, BaseURL: "https://edge.example/v1"}},
	}, snap.Generation); err != nil {
		t.Fatal(err)
	}
	rec = postConnect(t, env, connectLink("m1").String(), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeBody[ConnectResponse](t, rec); !got.Plan.Exists || got.Applied {
		t.Fatalf("dry run over existing id = %+v", got)
	}
	if p := env.cfg.Get().Config.Providers["edge"]; p.Wire != config.WireOpenAIChat {
		t.Fatalf("dry run changed the existing provider: %+v", p)
	}
}

func TestConnectAppliesProvider(t *testing.T) {
	env := newEnv(t)
	before := env.cfg.Get().Generation
	rec := postConnect(t, env, connectLink("m1").String(), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[ConnectResponse](t, rec)
	if !got.Applied || got.Generation != before+1 || got.Warning != "" || got.Plan.Exists {
		t.Fatalf("connect = %+v", got)
	}
	v := got.Provider
	if v == nil || v.ID != "edge" || v.Wire != "responses" || v.BaseURL != "http://10.0.0.1:8080/v1" ||
		!slices.Equal(v.FallbackBaseURLs, []string{"http://10.0.0.2:8080/v1"}) || v.EndpointMode != "spread" ||
		!slices.Equal(v.Models, []string{"m1"}) || v.Credential.State != "set" {
		t.Fatalf("provider = %+v", v)
	}
	p := env.cfg.Get().Config.Providers["edge"]
	if p.BaseURL != "http://10.0.0.1:8080/v1" || !slices.Equal(p.FallbackBaseURLs, []string{"http://10.0.0.2:8080/v1"}) || p.EndpointMode != config.EndpointModeSpread || p.APIKeyRef != "edge:default" {
		t.Fatalf("stored = %+v", p)
	}
	if string(env.creds.store["edge"]) != connectKey || len(env.creds.staged) != 0 {
		t.Fatalf("credential store = %v, staged = %v", env.creds.store, env.creds.staged)
	}
}

func TestConnectFailoverLinkStoresNoMode(t *testing.T) {
	env := newEnv(t)
	link := connectLink("m1")
	link.Spread = false
	if rec := postConnect(t, env, link.String(), false); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if p := env.cfg.Get().Config.Providers["edge"]; p.EndpointMode != "" {
		t.Fatalf("endpoint mode = %q, want unset", p.EndpointMode)
	}
}

func TestConnectRefusesExistingProvider(t *testing.T) {
	env := newEnv(t)
	if _, err := env.cfg.Update(config.Document{
		Version:   config.SchemaVersion,
		Providers: map[string]config.Provider{"edge": {Wire: config.WireOpenAIChat, BaseURL: "https://edge.example/v1"}},
	}, env.cfg.Get().Generation); err != nil {
		t.Fatal(err)
	}
	before := env.cfg.Get().Generation
	rec := postConnect(t, env, connectLink("m1").String(), false)
	assertErrorBody(t, rec, http.StatusConflict, "already_exists")
	snap := env.cfg.Get()
	if snap.Generation != before || snap.Config.Providers["edge"].Wire != config.WireOpenAIChat || len(env.creds.staged) != 0 {
		t.Fatalf("refused connect wrote: %+v, staged %v", snap, env.creds.staged)
	}
}

func TestConnectRejectsBadLink(t *testing.T) {
	env := newEnv(t)
	for _, link := range []string{
		"prism://connect?v=2&id=edge&wire=chat&endpoint=http://10.0.0.1:8080/v1&key=" + connectKey,
		"prism://connect?v=1&id=edge&wire=codex&endpoint=http://10.0.0.1:8080/v1&key=" + connectKey,
		"https://connect?v=1&id=edge&wire=chat&endpoint=http://10.0.0.1:8080/v1&key=" + connectKey,
	} {
		for _, dryRun := range []bool{true, false} {
			rec := postConnect(t, env, link, dryRun)
			assertErrorBody(t, rec, http.StatusBadRequest, "invalid_link")
		}
	}
	if len(env.cfg.Get().Config.Providers) != 0 {
		t.Fatal("bad link created a provider")
	}
}

func TestConnectDiscoversModelsWhenLinkHasNone(t *testing.T) {
	env := newEnv(t)
	env.srv.syncer = &fakeSyncer{models: map[string][]ListedModel{"edge": {{ID: "m2"}, {ID: "m1"}}}}
	before := env.cfg.Get().Generation
	rec := postConnect(t, env, connectLink().String(), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[ConnectResponse](t, rec)
	if !got.Applied || got.Warning != "" || got.Generation != before+2 || got.Provider == nil ||
		!slices.Equal(got.Provider.Models, []string{"m1", "m2"}) || got.Provider.Credential.State != "set" {
		t.Fatalf("connect = %+v", got)
	}
	if p := env.cfg.Get().Config.Providers["edge"]; !slices.Equal(p.Models, []string{"m1", "m2"}) || !slices.Equal(p.SyncedModels, []string{"m2", "m1"}) {
		t.Fatalf("stored = %+v", p)
	}
}

func TestConnectKeepsProviderWhenDiscoveryFails(t *testing.T) {
	for name, syncer := range map[string]ModelSyncer{
		"failing": &fakeSyncer{err: errors.New("upstream rejected " + connectKey)},
		"absent":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			env := newEnv(t)
			env.srv.syncer = syncer
			before := env.cfg.Get().Generation
			rec := postConnect(t, env, connectLink().String(), false)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			got := decodeBody[ConnectResponse](t, rec)
			if !got.Applied || got.Warning == "" || got.Generation != before+1 || got.Provider == nil || len(got.Provider.Models) != 0 || got.Provider.Credential.State != "set" {
				t.Fatalf("connect = %+v", got)
			}
			if _, ok := env.cfg.Get().Config.Providers["edge"]; !ok {
				t.Fatal("provider dropped after discovery failure")
			}
		})
	}
}
