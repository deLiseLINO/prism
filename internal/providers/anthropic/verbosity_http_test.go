package anthropic

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeVerbosityRejectsBeforeDispatch(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer up.Close()
	req := baseRequest()
	req.Text.Verbosity = canon.VerbosityHigh
	err := New(Options{HTTP: up.Client()}).Run(t.Context(), provider.RunRequest{Request: req, Target: provider.Target{BaseURL: up.URL}}, &collectingSink{})
	var re *provider.RunError
	if !errors.As(err, &re) || re.Class != provider.ClassInvalidRequest || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestNativeThinkingSamplingRejectsBeforeDispatch(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer up.Close()
	req := baseRequest()
	req.Reasoning.Effort = canon.EffortHigh
	err := New(Options{HTTP: up.Client()}).Run(t.Context(), provider.RunRequest{Request: req, Target: provider.Target{BaseURL: up.URL}}, &collectingSink{})
	var re *provider.RunError
	if !errors.As(err, &re) || re.Class != provider.ClassInvalidRequest || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}
