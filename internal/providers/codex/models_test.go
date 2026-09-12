package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchModelsListsVisibleSlugs(t *testing.T) {
	var gotAuth, gotAccount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("client_version") == "" {
			t.Error("request must carry the client_version query")
		}
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("chatgpt-account-id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-5.6-terra","visibility":"list","supported_in_api":true,"priority":7},
			{"slug":"gpt-reserve","visibility":"hide","supported_in_api":true,"priority":3},
			{"slug":"gpt-5.5","visibility":"list","supported_in_api":true,"priority":12},
			{"slug":"codex-auto-review","visibility":"hide","supported_in_api":true,"priority":43}]}`))
	}))
	t.Cleanup(server.Close)

	models, err := FetchModels(context.Background(), server.Client(), server.URL+"/", Credential{AccessToken: "tok", ChatGPTAccountID: "acct-1"})
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if gotAuth != "Bearer tok" || gotAccount != "acct-1" {
		t.Fatalf("auth headers: %q %q", gotAuth, gotAccount)
	}
	want := []string{"gpt-5.5", "gpt-5.6-terra"}
	if len(models) != len(want) || models[0] != want[0] || models[1] != want[1] {
		t.Fatalf("models = %v, want %v (sorted, hide filtered)", models, want)
	}
}

func TestFetchModelsDefaultsToProductionBaseURL(t *testing.T) {
	if got := modelsURL(""); !strings.HasPrefix(got, "https://chatgpt.com/backend-api/codex/models?client_version=") {
		t.Fatalf("empty baseURL must select the production models endpoint, got %q", got)
	}
}

func TestFetchModelsRejectsNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	if _, err := FetchModels(context.Background(), server.Client(), server.URL, Credential{AccessToken: "tok"}); err == nil {
		t.Fatal("non-200 response must error")
	}
}

func TestFetchModelsRequiresAccessToken(t *testing.T) {
	if _, err := FetchModels(context.Background(), http.DefaultClient, "", Credential{}); err == nil {
		t.Fatal("missing token must error")
	}
}

func TestParseModelsRejectsEmptyAndMalformed(t *testing.T) {
	if _, err := parseModels([]byte(`{`)); err == nil {
		t.Fatal("malformed payload must error")
	}
	if _, err := parseModels([]byte(`{"models":[{"slug":"x","visibility":"hide"}]}`)); err == nil {
		t.Fatal("payload without a visible model must error")
	}
	if _, err := parseModels([]byte(`{"models":[{"slug":"","visibility":"list"}]}`)); err == nil {
		t.Fatal("blank slug must not count as a model")
	}
}
