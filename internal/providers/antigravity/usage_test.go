package antigravity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/quota"
)

func TestFetchQuotaPostsProjectAndParsesWindows(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotUA, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{
			"gemini-3.7-flash":{"quotaInfo":{"remainingFraction":0.75,"resetTime":"2026-08-30T00:00:00Z"}},
			"claude-opus-4":{"quotaInfo":{"remainingPercentage":40}}}}`))
	}))
	t.Cleanup(server.Close)

	windows, err := FetchQuota(context.Background(), server.Client(), server.URL, CredentialPair{AccessToken: "tok", ProjectID: "proj-7"})
	if err != nil {
		t.Fatalf("FetchQuota: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1internal:fetchAvailableModels" {
		t.Fatalf("request: %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth: %q", gotAuth)
	}
	if gotUA == "" || len(gotUA) < len("antigravity/ide/") || gotUA[:16] != "antigravity/ide/" {
		t.Fatalf("user agent must be the IDE family: %q", gotUA)
	}
	if gotBody != `{"project":"proj-7"}` {
		t.Fatalf("body: %s", gotBody)
	}
	if windows.Gem == nil || windows.Cla == nil {
		t.Fatalf("windows: %+v", windows)
	}
	if windows.Gem.Used != 2500 || windows.Cla.Used != 6000 {
		t.Fatalf("used = %d/%d, want 2500/6000 basis points", windows.Gem.Used, windows.Cla.Used)
	}

	governing, ok := windows.GoverningSnapshot()
	if !ok || governing.Used != 6000 || governing.Source != quota.SourceEndpoint {
		t.Fatalf("governing = %+v ok=%v, want the higher cla window", governing, ok)
	}
}

func TestFetchQuotaRequiresCredentialFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request must be made without credential fields")
	}))
	t.Cleanup(server.Close)

	if _, err := FetchQuota(context.Background(), server.Client(), server.URL, CredentialPair{}); err == nil {
		t.Fatal("missing token must error")
	}
	if _, err := FetchQuota(context.Background(), server.Client(), server.URL, CredentialPair{AccessToken: "tok"}); err == nil {
		t.Fatal("missing project must error")
	}
}

func TestFetchQuotaRejectsNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	if _, err := FetchQuota(context.Background(), server.Client(), server.URL, CredentialPair{AccessToken: "tok", ProjectID: "p"}); err == nil {
		t.Fatal("non-200 response must error")
	}
}
