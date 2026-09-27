package cline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchModelsSendsProductHeadersAndParsesCatalog(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		fmt.Fprint(w, `{"free":[{"id":"cline-free/kimi-k3"}],"clinePass":[{"id":"cline-pass/glm-5.3"},{"id":""}]}`)
	}))
	defer srv.Close()
	models, err := FetchModels(context.Background(), srv.Client(), srv.URL, "workos:tok")
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if gotPath != "/api/v1/ai/cline/recommended-models" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer workos:tok" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotUA != "Cline/3.0.49" {
		t.Fatalf("user-agent = %q", gotUA)
	}
	if len(models) != 2 || models[0] != "cline-free/kimi-k3" || models[1] != "cline-pass/glm-5.3" {
		t.Fatalf("models = %v", models)
	}
}

func TestFetchModelsRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := FetchModels(context.Background(), srv.Client(), srv.URL, "workos:tok")
	if err == nil {
		t.Fatal("expected error for 403")
	}
}

func TestEnsureWorkosPrefix(t *testing.T) {
	if got := EnsureWorkosPrefix("bare"); got != "workos:bare" {
		t.Fatalf("prefix = %q", got)
	}
	if got := EnsureWorkosPrefix("workos:bare"); got != "workos:bare" {
		t.Fatalf("idempotent prefix = %q", got)
	}
}

func TestGatewayBaseNormalizesSuffixVariants(t *testing.T) {
	cases := map[string]string{
		"https://api.cline.bot":          "https://api.cline.bot",
		"https://api.cline.bot/":         "https://api.cline.bot",
		"https://api.cline.bot/api/v1":   "https://api.cline.bot",
		"https://api.cline.bot/api/v1/":  "https://api.cline.bot",
		" https://api.cline.bot/api/v1 ": "https://api.cline.bot",
		"":                               "https://api.cline.bot",
	}
	for in, want := range cases {
		if got := GatewayBase(in); got != want {
			t.Fatalf("GatewayBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnwrapTransportStripsSuccessEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"data":{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}}`)
	}))
	defer srv.Close()
	client := &http.Client{Transport: UnwrapTransport{Next: srv.Client().Transport}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unwrapped body is not JSON: %v", err)
	}
	if _, wrapped := payload["data"]; wrapped {
		t.Fatalf("envelope survived: %s", raw)
	}
	if _, ok := payload["choices"]; !ok {
		t.Fatalf("unwrapped body lost choices: %s", raw)
	}
}

func TestUnwrapTransportPassesThroughUnwrappedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c1","choices":[]}`)
	}))
	defer srv.Close()
	client := &http.Client{Transport: UnwrapTransport{Next: srv.Client().Transport}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if string(raw) != `{"id":"c1","choices":[]}` {
		t.Fatalf("passthrough body changed: %s", raw)
	}
}

func TestProductHeadersCoverGatewayRequirements(t *testing.T) {
	h := ProductHeaders()
	for _, name := range []string{"User-Agent", "HTTP-Referer", "X-Title", "X-IS-MULTIROOT", "X-CLIENT-TYPE", "X-CLIENT-VERSION", "X-PLATFORM", "X-PLATFORM-VERSION"} {
		if h[name] == "" {
			t.Fatalf("product header %q is empty", name)
		}
	}
	_, _ = json.Marshal(h)
}
