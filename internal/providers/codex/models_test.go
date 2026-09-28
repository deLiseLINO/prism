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

func TestParseModelListReadsWindowAndImage(t *testing.T) {
	body := []byte(`{"models":[
		{"slug":"gpt-vision","visibility":"list","context_window":128000,"max_context_window":400000,"input_modalities":["text","image"]},
		{"slug":"gpt-text","visibility":"list","context_window":32000,"input_modalities":["text"]},
		{"slug":"gpt-unknown","visibility":"list","context_window":0,"max_context_window":999999,"input_modalities":null},
		{"slug":"gpt-bad","visibility":"list","context_window":"wide","input_modalities":"image"},
		{"slug":"hidden","visibility":"hide","context_window":1000,"input_modalities":["image"]},
		{"slug":"","visibility":"list","context_window":1000}
	]}`)
	rows, err := parseModelList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4 visible slugs", len(rows))
	}
	byID := map[string]ListedModel{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	vision := byID["gpt-vision"]
	if vision.ContextWindow == nil || *vision.ContextWindow != 128000 {
		t.Fatalf("vision window = %v, want 128000 (max_context_window ignored)", vision.ContextWindow)
	}
	if vision.Image == nil || !*vision.Image {
		t.Fatalf("vision image = %v, want true", vision.Image)
	}
	textOnly := byID["gpt-text"]
	if textOnly.Image == nil || *textOnly.Image {
		t.Fatalf("text image = %v, want false", textOnly.Image)
	}
	unknown := byID["gpt-unknown"]
	if unknown.ContextWindow != nil || unknown.Image != nil {
		t.Fatalf("absent facts stored: %+v", unknown)
	}
	bad := byID["gpt-bad"]
	if bad.ContextWindow != nil || bad.Image != nil {
		t.Fatalf("bad fields stored: %+v", bad)
	}
	if _, err := parseModelList([]byte(`[]`)); err == nil {
		t.Fatal("malformed envelope must error")
	}
}
