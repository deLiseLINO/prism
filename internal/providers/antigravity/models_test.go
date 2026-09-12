package antigravity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchModelsReturnsModelIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1internal:fetchAvailableModels" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{
			"gemini-3.7-flash":{"displayName":"Gemini 3.7 Flash","quotaInfo":{"remainingPercentage":75}},
			"claude-opus-4":{"displayName":"Claude Opus 4","quotaInfo":{"remainingPercentage":40}}}}`))
	}))
	t.Cleanup(server.Close)

	models, err := FetchModels(context.Background(), server.Client(), server.URL, CredentialPair{AccessToken: "tok", ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	want := []string{"claude-opus-4", "gemini-3.7-flash"}
	if len(models) != len(want) {
		t.Fatalf("models = %v, want %v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models = %v, want %v (sorted)", models, want)
		}
	}
}

func TestFetchModelsRequiresCredentialFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request must be made without credential fields")
	}))
	t.Cleanup(server.Close)

	if _, err := FetchModels(context.Background(), server.Client(), server.URL, CredentialPair{}); err == nil {
		t.Fatal("missing token must error")
	}
	if _, err := FetchModels(context.Background(), server.Client(), server.URL, CredentialPair{AccessToken: "tok"}); err == nil {
		t.Fatal("missing project must error")
	}
}

func TestFetchModelsRejectsNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	if _, err := FetchModels(context.Background(), server.Client(), server.URL, CredentialPair{AccessToken: "tok", ProjectID: "p"}); err == nil {
		t.Fatal("non-200 response must error")
	}
}

func TestParseModelIDsRejectsEmptyAndMalformed(t *testing.T) {
	if _, err := parseModelIDs([]byte(`{`)); err == nil {
		t.Fatal("malformed payload must error")
	}
	if _, err := parseModelIDs([]byte(`{"models":{}}`)); err == nil {
		t.Fatal("empty models object must error")
	}
	if _, err := parseModelIDs([]byte(`{"data":[]}`)); err == nil {
		t.Fatal("payload without a models object must error")
	}
}
