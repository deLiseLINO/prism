package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/config"
	"prism/internal/provider"
	"prism/internal/routing"
)

func testVisionSidecarDoc() config.Document {
	return config.Document{
		Version: config.SchemaVersion,
		Providers: map[string]config.Provider{
			"p1": {
				Wire:    config.WireOpenAIChat,
				BaseURL: "http://localhost",
				ModelSettings: map[string]config.ModelSettings{
					"vision-model": {ImageInput: true},
				},
			},
		},
		VisionSidecar: config.VisionSidecarSettings{Enabled: true, Target: "p1/vision-model"},
	}
}

func TestApplyImageDescriptionsReplacesImages(t *testing.T) {
	req := canon.Request{
		Instructions: []canon.Content{canon.ImageContent{MIMEType: "image/png", Data: []byte{1}}},
		Input: []canon.Item{
			canon.Message{
				ID:   "m1",
				Role: canon.RoleUser,
				Content: []canon.Content{
					canon.TextContent{Text: "look"},
					canon.ImageContent{MIMEType: "image/png", Data: []byte{2}}},
			},
		},
	}
	out := applyImageDescriptions(req, []string{"first", "second"})
	if _, ok := out.Instructions[0].(canon.ImageContent); ok {
		t.Fatal("instructions image was not replaced")
	}
	m := out.Input[0].(canon.Message)
	if _, ok := m.Content[1].(canon.ImageContent); ok {
		t.Fatal("input image was not replaced")
	}
	text, ok := m.Content[1].(canon.TextContent)
	if !ok || text.Text != "<image>\nsecond\n</image>" {
		t.Fatalf("replacement = %#v, want description block", m.Content[1])
	}
}

func TestApplyImageDescriptionsReplacesFunctionOutputImages(t *testing.T) {
	req := canon.Request{
		Input: []canon.Item{canon.FunctionOutput{
			ID:     "o1",
			CallID: "c1",
			Output: []canon.Content{canon.ImageContent{MIMEType: "image/png"}},
		}},
	}
	out := applyImageDescriptions(req, []string{"tool result"})
	o := out.Input[0].(canon.FunctionOutput)
	text, ok := o.Output[0].(canon.TextContent)
	if !ok || text.Text != "<image>\ntool result\n</image>" {
		t.Fatalf("replacement = %#v, want description block", o.Output[0])
	}
	if o.ID != "o1" || o.CallID != "c1" {
		t.Fatalf("function output identity = %q/%q, want o1/c1", o.ID, o.CallID)
	}
}

func TestCollectImagesIncludesFunctionOutput(t *testing.T) {
	req := canon.Request{
		Input: []canon.Item{canon.FunctionOutput{
			Output: []canon.Content{canon.ImageContent{MIMEType: "image/png"}},
		}},
	}
	if got := canon.CollectImages(req); len(got) != 1 {
		t.Fatalf("images = %d, want 1", len(got))
	}
}

func TestVisionSidecarRequiresImageCapableTarget(t *testing.T) {
	doc := testVisionSidecarDoc()
	doc.Providers["p1"].ModelSettings["vision-model"] = config.ModelSettings{}
	get := func() config.Document { return doc }
	s := &Server{cfg: staticConfig{get: get}, planner: &ConfigPlanner{get: get}}
	req := canon.Request{
		Model: "p2/text-model",
		Input: []canon.Item{canon.Message{Content: []canon.Content{canon.ImageContent{}}}},
	}
	if _, ok := s.visionSidecar(req); ok {
		t.Fatal("sidecar resolved for text-only target")
	}
}

func TestApplyImageDescriptionsEscapesClosingTag(t *testing.T) {
	req := canon.Request{Input: []canon.Item{canon.Message{Content: []canon.Content{canon.ImageContent{}}}}}
	out := applyImageDescriptions(req, []string{"contains </image> marker"})
	text := out.Input[0].(canon.Message).Content[0].(canon.TextContent)
	if strings.Count(text.Text, "</image>") != 1 {
		t.Fatalf("replacement = %q, want escaped description and single closing tag", text.Text)
	}
}

type recordingRunner struct {
	runner *fakeRunner
	reqs   []provider.RunRequest
}

func (r *recordingRunner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	r.reqs = append(r.reqs, req)
	return r.runner.Run(ctx, req, sink)
}

func visionSidecarAssistantEvent(text string) canon.Event {
	return canon.ItemFinished{Item: canon.Message{
		Role:    canon.RoleAssistant,
		Content: []canon.Content{canon.TextContent{Text: text}},
	}}
}

func TestVisionSidecarTurnReplacesImagesForTextOnlyMainModel(t *testing.T) {
	visionRunner := &recordingRunner{runner: &fakeRunner{scripts: []fakeScript{{events: []canon.Event{
		visionSidecarAssistantEvent("a red square"),
		canon.TurnFinished{Status: canon.Completed()},
	}}}}}
	mainRunner := &recordingRunner{runner: &fakeRunner{scripts: []fakeScript{{events: []canon.Event{
		canon.TurnFinished{Status: canon.Completed()},
	}}}}}
	plans := map[canon.ModelID]routing.Plan{
		"p1/vision-model": {Targets: []provider.Target{{Provider: "p1", Model: "vision-model", ImageInput: true}}},
		"main/text-model": {Targets: []provider.Target{{Provider: "p2", Model: "text-model"}}},
	}
	h := newTestServer(t, nil, plans, func(reg *provider.Registry) {
		if err := reg.Register("p1", visionRunner); err != nil {
			t.Fatal(err)
		}
		if err := reg.Register("p2", mainRunner); err != nil {
			t.Fatal(err)
		}
	})
	setConfig(t, h, testVisionSidecarDoc())
	img := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	body := fmt.Sprintf(`{"model":"main/text-model","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]}`, img)
	rec := postJSON(t, h, "/v1/chat/completions", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(visionRunner.reqs) != 1 {
		t.Fatalf("vision calls = %d, want 1", len(visionRunner.reqs))
	}
	if len(mainRunner.reqs) != 1 {
		t.Fatalf("main calls = %d, want 1", len(mainRunner.reqs))
	}
	contents := mainRunner.reqs[0].Request.Input[0].(canon.Message).Content
	text, ok := contents[1].(canon.TextContent)
	if !ok || text.Text != "<image>\na red square\n</image>" {
		t.Fatalf("main content = %#v, want sidecar description", contents[1])
	}
}
func TestVisionSidecarSkippedForVisionMainModel(t *testing.T) {
	sidecarRunner := &recordingRunner{runner: &fakeRunner{}}
	mainRunner := &recordingRunner{runner: &fakeRunner{scripts: []fakeScript{{events: []canon.Event{
		canon.TurnFinished{Status: canon.Completed()},
	}}}}}
	plans := map[canon.ModelID]routing.Plan{
		"p1/vision-model": {Targets: []provider.Target{{Provider: "p1", Model: "vision-model", ImageInput: true}}},
		"p2/vision-model": {Targets: []provider.Target{{Provider: "p2", Model: "vision-model", ImageInput: true}}},
	}
	h := newTestServer(t, nil, plans, func(reg *provider.Registry) {
		if err := reg.Register("p1", sidecarRunner); err != nil {
			t.Fatal(err)
		}
		if err := reg.Register("p2", mainRunner); err != nil {
			t.Fatal(err)
		}
	})
	setConfig(t, h, testVisionSidecarDoc())
	img := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	body := fmt.Sprintf(`{"model":"p2/vision-model","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]}`, img)
	rec := postJSON(t, h, "/v1/chat/completions", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(sidecarRunner.reqs) != 0 {
		t.Fatalf("sidecar calls = %d, want 0", len(sidecarRunner.reqs))
	}
	if len(mainRunner.reqs) != 1 {
		t.Fatalf("main calls = %d, want 1", len(mainRunner.reqs))
	}
	if _, ok := mainRunner.reqs[0].Request.Input[0].(canon.Message).Content[1].(canon.ImageContent); !ok {
		t.Fatal("main request lost original image")
	}
}

func TestVisionSidecarTurnFailsClosed(t *testing.T) {
	visionRunner := &recordingRunner{runner: &fakeRunner{scripts: []fakeScript{{err: errors.New("vision upstream down")}}}}
	mainRunner := &recordingRunner{runner: &fakeRunner{}}
	plans := map[canon.ModelID]routing.Plan{
		"p1/vision-model": {Targets: []provider.Target{{Provider: "p1", Model: "vision-model", ImageInput: true}}},
		"main/text-model": {Targets: []provider.Target{{Provider: "p2", Model: "text-model"}}},
	}
	h := newTestServer(t, nil, plans, func(reg *provider.Registry) {
		if err := reg.Register("p1", visionRunner); err != nil {
			t.Fatal(err)
		}
		if err := reg.Register("p2", mainRunner); err != nil {
			t.Fatal(err)
		}
	})
	setConfig(t, h, testVisionSidecarDoc())
	img := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	body := fmt.Sprintf(`{"model":"main/text-model","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]}`, img)
	rec := postJSON(t, h, "/v1/chat/completions", body)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "vision_sidecar_failed" || !strings.Contains(envelope.Error.Message, "vision upstream down") {
		t.Fatalf("error = %#v, want sidecar failure", envelope.Error)
	}
	if len(mainRunner.reqs) != 0 {
		t.Fatalf("main calls = %d, want 0", len(mainRunner.reqs))
	}
}

func TestVisionSidecarTurnRejectsTooManyImages(t *testing.T) {
	var images strings.Builder
	images.WriteString(`{"model":"main/text-model","stream":false,"messages":[{"role":"user","content":[`)
	for i := 0; i < maxSidecarImages+1; i++ {
		if i > 0 {
			images.WriteByte(',')
		}
		images.WriteString(`{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}`)
	}
	images.WriteString(`]}]}`)
	visionRunner := &recordingRunner{runner: &fakeRunner{}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{
		"p1/vision-model": {Targets: []provider.Target{{Provider: "p1", Model: "vision-model", ImageInput: true}}},
		"main/text-model": {Targets: []provider.Target{{Provider: "p2", Model: "text-model"}}},
	}, func(reg *provider.Registry) {
		if err := reg.Register("p1", visionRunner); err != nil {
			t.Fatal(err)
		}
	})
	setConfig(t, h, testVisionSidecarDoc())
	rec := postJSON(t, h, "/v1/chat/completions", images.String())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(visionRunner.reqs) != 0 {
		t.Fatalf("vision calls = %d, want 0", len(visionRunner.reqs))
	}
}

func setConfig(t *testing.T, h *harness, doc config.Document) {
	t.Helper()
	snap := h.cfg.Get()
	if _, err := h.cfg.Update(doc, snap.Generation); err != nil {
		t.Fatalf("config update: %v", err)
	}
}

type staticConfig struct{ get func() config.Document }

func (c staticConfig) Get() config.Snapshot { return config.Snapshot{Config: c.get()} }
