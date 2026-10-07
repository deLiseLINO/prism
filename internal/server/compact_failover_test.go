package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/routing"
)

// scriptedCompactor answers each Compact call from errs/summary and records
// what it was asked to compact.
type scriptedCompactor struct {
	*fakeRunner
	mu      sync.Mutex
	errs    []error
	summary string
	reqs    []provider.CompactRequest
}

func (c *scriptedCompactor) Compact(_ context.Context, req provider.CompactRequest) (provider.CompactResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := len(c.reqs)
	c.reqs = append(c.reqs, req)
	if i < len(c.errs) && c.errs[i] != nil {
		return provider.CompactResult{}, c.errs[i]
	}
	return provider.CompactResult{Summary: canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: c.summary}}}}, nil
}

func (c *scriptedCompactor) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func twoTargetPlan(first, second string) map[canon.ModelID]routing.Plan {
	return map[canon.ModelID]routing.Plan{"test-model": {Targets: []provider.Target{
		{Provider: account.ProviderID(first), Model: "m1"},
		{Provider: account.ProviderID(second), Model: "m2"},
	}}}
}

func newCompactor(summary string, errs ...error) *scriptedCompactor {
	return &scriptedCompactor{fakeRunner: &fakeRunner{}, errs: errs, summary: summary}
}

func TestCompactFailsOverToNextTargetOnRetryableError(t *testing.T) {
	first := newCompactor("first", provider.RunError{Kind: provider.Retryable, Class: provider.ClassServer, ReplaySafe: true})
	second := newCompactor("second summary")
	h := newTestServer(t, nil, twoTargetPlan("p1", "p2"), func(reg *provider.Registry) {
		_ = reg.Register("p1", first)
		_ = reg.Register("p2", second)
	})
	rec := postJSON(t, h, "/v1/responses/compact", `{"model":"test-model","input":"hi"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "second summary") {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if first.calls() != 1 || second.calls() != 1 {
		t.Fatalf("calls first=%d second=%d, want 1 and 1", first.calls(), second.calls())
	}
	if got := second.reqs[0].Target.Model; got != "m2" {
		t.Fatalf("second target model = %q, want m2", got)
	}
}

func TestCompactDoesNotFailOverOnInvalidRequest(t *testing.T) {
	first := newCompactor("first", provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassInvalidRequest})
	second := newCompactor("second summary")
	h := newTestServer(t, nil, twoTargetPlan("p1", "p2"), func(reg *provider.Registry) {
		_ = reg.Register("p1", first)
		_ = reg.Register("p2", second)
	})
	rec := postJSON(t, h, "/v1/responses/compact", `{"model":"test-model","input":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s, want 400", rec.Code, rec.Body.String())
	}
	if second.calls() != 0 {
		t.Fatal("second target called after a non-failover error")
	}
}

func TestCompactFailsOverFromTurnTargetToNextTarget(t *testing.T) {
	first := &fakeRunner{scripts: []fakeScript{{err: provider.RunError{Kind: provider.Retryable, Class: provider.ClassServer, ReplaySafe: true}}}}
	second := newCompactor("compactor summary")
	second.fakeRunner = &fakeRunner{scripts: []fakeScript{{events: []canon.Event{
		canon.ItemStarted{Item: messageAssistant("m1", "compactor summary")},
		canon.TextDelta{ItemID: "m1", Text: "compactor summary"},
		canon.ItemFinished{Item: messageAssistant("m1", "compactor summary")},
		canon.TurnFinished{Status: canon.Completed()},
	}}}}
	h := newTestServer(t, nil, twoTargetPlan("p1", "p2"), func(reg *provider.Registry) {
		_ = reg.Register("p1", first)
		_ = reg.Register("p2", second)
	})
	rec := postJSON(t, h, "/v1/responses/compact", `{"model":"test-model","input":"hi"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "compactor summary") {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if first.callsMade() != 1 {
		t.Fatalf("turn target calls = %d, want 1", first.callsMade())
	}
}

func TestCompactFailsOverFromCompactorToTurnTarget(t *testing.T) {
	first := newCompactor("first", provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true})
	second := &fakeRunner{scripts: []fakeScript{{events: []canon.Event{
		canon.ItemStarted{Item: messageAssistant("m1", "turn summary")},
		canon.TextDelta{ItemID: "m1", Text: "turn summary"},
		canon.ItemFinished{Item: messageAssistant("m1", "turn summary")},
		canon.TurnFinished{Status: canon.Completed()},
	}}}}
	h := newTestServer(t, nil, twoTargetPlan("p1", "p2"), func(reg *provider.Registry) {
		_ = reg.Register("p1", first)
		_ = reg.Register("p2", second)
	})
	rec := postJSON(t, h, "/v1/responses/compact", `{"model":"test-model","input":"hi"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "turn summary") {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
}

const compactWithImage = `{"model":"test-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}]}]}`

func compactedImageCount(req provider.CompactRequest) int {
	n := 0
	for _, item := range req.Input {
		if m, ok := item.(canon.Message); ok {
			for _, c := range m.Content {
				if _, ok := c.(canon.ImageContent); ok {
					n++
				}
			}
		}
	}
	return n
}

func TestCompactDoesNotSendImagesToTextOnlyTarget(t *testing.T) {
	comp := newCompactor("summary")
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": {Targets: []provider.Target{{Provider: "p1", Model: "m1"}}}}, func(reg *provider.Registry) {
		_ = reg.Register("p1", comp)
	})
	rec := postJSON(t, h, "/v1/responses/compact", compactWithImage)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if got := compactedImageCount(comp.reqs[0]); got != 0 {
		t.Fatalf("compactor received %d images for a text-only target", got)
	}
}

func TestCompactKeepsImagesForImageCapableTarget(t *testing.T) {
	comp := newCompactor("summary")
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": {Targets: []provider.Target{{Provider: "p1", Model: "m1", ImageInput: true}}}}, func(reg *provider.Registry) {
		_ = reg.Register("p1", comp)
	})
	rec := postJSON(t, h, "/v1/responses/compact", compactWithImage)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if got := compactedImageCount(comp.reqs[0]); got != 1 {
		t.Fatalf("compactor received %d images, want 1", got)
	}
}
