package antigravity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestFetchModelsCollapsesVariantFamilies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{
			"gemini-3.7-flash-low":{"quotaInfo":{"remainingPercentage":75}},
			"gemini-3.7-flash-medium":{"quotaInfo":{"remainingPercentage":75}},
			"gemini-3.7-flash-high":{"quotaInfo":{"remainingPercentage":75}},
			"gemini-3.7-flash-tiered":{"quotaInfo":{"remainingPercentage":75}},
			"grok-5":{"quotaInfo":{"remainingPercentage":50}},
			"grok-5-thinking":{"quotaInfo":{"remainingPercentage":50}}}}`))
	}))
	t.Cleanup(server.Close)

	models, err := FetchModels(context.Background(), server.Client(), server.URL, CredentialPair{AccessToken: "tok", ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	want := []string{"gemini-3.7-flash", "grok-5"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %v, want %v", models, want)
	}
	// The snapshot carries the raw ids: wire resolution and RawModels use it.
	if got := RawModels("gemini-3.7-flash"); !reflect.DeepEqual(got, []string{"gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high", "gemini-3.7-flash-tiered"}) {
		t.Fatalf("RawModels = %v", got)
	}
	if got := RawModels("grok-5"); !reflect.DeepEqual(got, []string{"grok-5", "grok-5-thinking"}) {
		t.Fatalf("RawModels(pair) = %v", got)
	}
	if got := resolveWireModel("gemini-3.7-flash", effortHigh, presenceSnapshot()); got != "gemini-3.7-flash-high" {
		t.Fatalf("resolveWireModel high = %q", got)
	}
}

func TestLogicalModelMapsTableOwnedIds(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"gemini-3.7-flash-low", "gemini-3.7-flash"},
		{"gemini-3.7-flash-tiered", "gemini-3.7-flash"},
		{"gemini-3.7-flash", "gemini-3.7-flash"},
		{"gemini-3.6-flash-high", "gemini-3.6-flash"},
		{"gemini-3.5-flash-low", "gemini-3.5-flash"},
		{"gemini-3-flash-agent", "gemini-3.5-flash"},
		{"gemini-3-pro-low", "gemini-3-pro"},
		{"claude-sonnet-4-6-thinking", "claude-sonnet-4-6"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-opus-4-5", "claude-opus-4-5"},
		{"gemini-3-flash", ""},
		{"grok-5-thinking", ""},
		{"claude-opus-4", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := LogicalModel(tc.raw); got != tc.want {
			t.Errorf("LogicalModel(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestLogicalModelBelowTemplateFloorUnmapped(t *testing.T) {
	if got := LogicalModel("gemini-3.4-flash-low"); got != "" {
		t.Fatalf("below-min revision must not map through the template: %q", got)
	}
}

func TestFetchModelsSnapshotReplacedAcrossFetches(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"gemini-3.7-flash-low":{"q":1},"gemini-3.7-flash-medium":{"q":1},"gemini-3.7-flash-high":{"q":1},"gemini-3.7-flash-tiered":{"q":1}}}`))
	}))
	t.Cleanup(first.Close)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"gemini-3.7-flash-low":{"q":1}}}`))
	}))
	t.Cleanup(second.Close)

	cred := CredentialPair{AccessToken: "tok", ProjectID: "p"}
	if _, err := FetchModels(context.Background(), first.Client(), first.URL, cred); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if got := resolveWireModel("gemini-3.7-flash", effortMedium, presenceSnapshot()); got != "gemini-3.7-flash-medium" {
		t.Fatalf("after first fetch, medium = %q", got)
	}
	if _, err := FetchModels(context.Background(), second.Client(), second.URL, cred); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	// Only -low remains live: medium's target is absent, so it falls back to
	// the default wire (first live member).
	if got := resolveWireModel("gemini-3.7-flash", effortMedium, presenceSnapshot()); got != "gemini-3.7-flash-low" {
		t.Fatalf("after second fetch, medium = %q", got)
	}
	// RawModels mirrors the new presence.
	if got := RawModels("not-a-family"); got != nil {
		t.Fatalf("RawModels(unknown) = %v", got)
	}
	if got := RawModels("gemini-3.7-flash"); !reflect.DeepEqual(got, []string{"gemini-3.7-flash-low"}) {
		t.Fatalf("RawModels after second fetch = %v", got)
	}
}
