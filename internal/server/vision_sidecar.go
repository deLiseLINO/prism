package server

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"strings"

	"prism/internal/canon"
	"prism/internal/execution"
	"prism/internal/provider"
	"prism/internal/routing"
)

const (
	imageDescribeSystemPrompt = `Image-analysis assistant. Description replaces attached image in downstream model context; downstream relies entirely on text, never sees pixels.

Core behavior:
- Faithful, evidence-first: distinguish direct observations from inferences.
- Transcribe ALL visible text verbatim; preserve casing, punctuation, layout order. Explicitly mark unreadable segments; NEVER guess.
- NEVER fabricate occluded, blurry, or uncertain details; state uncertainty.
- Thorough, compact: dense, information-rich prose; no filler.
- Output description only: no meta commentary, preambles ("This image shows…"), or closing remarks.`

	imageDescribePrompt = `Describe the image in enough detail for a model unable to see it to reason about its content.

Where present, cover: overall scene, subject, action; people and objects—their relationships, positions, colors, counts; all visible text verbatim (OCR); UI/screenshot elements—labels, buttons, inputs, states, errors, highlighted or disabled controls; diagrams, charts, tables—structure, axes, series, encoded values.

Flag anything ambiguous or unreadable. Output plain prose only.`

	descriptionUnavailableNote = "[Image description unavailable: the vision model returned no usable text.]"
)

type visionSidecar struct {
	server *Server
	target provider.Target
}

func (s *Server) visionSidecar(req canon.Request) (*visionSidecar, bool) {
	cfg := s.cfg.Get().Config
	if !cfg.VisionSidecar.Enabled || cfg.VisionSidecar.Target == "" {
		return nil, false
	}
	if !canon.HasImage(req) {
		return nil, false
	}
	plan, ok := s.planner.Plan(req.Model)
	if !ok || len(plan.Targets) == 0 {
		return nil, false
	}
	textOnly := false
	for _, target := range plan.Targets {
		if !target.ImageInput {
			textOnly = true
			break
		}
	}
	if !textOnly {
		return nil, false
	}
	sidecarPlan, ok := s.planner.Plan(canon.ModelID(cfg.VisionSidecar.Target))
	if !ok || len(sidecarPlan.Targets) == 0 {
		return nil, false
	}
	target := sidecarPlan.Targets[0]
	if !target.ImageInput {
		return nil, false
	}
	return &visionSidecar{server: s, target: target}, true
}

func (v *visionSidecar) describeImages(ctx context.Context, req canon.Request) (canon.Request, error) {
	images := canon.CollectImages(req)
	if len(images) == 0 {
		return req, nil
	}
	descriptions := make([]string, len(images))
	for i, img := range images {
		text, err := v.describeImage(ctx, img)
		if err != nil {
			log.Printf("server: vision sidecar provider=%q model=%q image=%d description failed: %v", v.target.Provider, v.target.Model, i, err)
			descriptions[i] = descriptionUnavailableNote
			continue
		}
		descriptions[i] = text
	}
	return applyImageDescriptions(req, descriptions), nil
}

func (v *visionSidecar) describeImage(ctx context.Context, img canon.ImageContent) (string, error) {
	turnReq := canon.Request{
		Model:        canon.ModelID(string(v.target.Provider) + "/" + string(v.target.Model)),
		Stream:       false,
		Instructions: []canon.Content{canon.TextContent{Text: imageDescribeSystemPrompt}},
		Input: []canon.Item{canon.Message{
			Role: canon.RoleUser,
			Content: []canon.Content{
				img,
				canon.TextContent{Text: imageDescribePrompt},
			},
		}},
	}
	sink := &summarySink{}
	res := v.server.router.Turn(ctx, turnReq, execution.Facts{}, notStartedLifecycle{}, sink)
	if failed, ok := res.Terminal.(routing.Failed); ok {
		return "", fmt.Errorf("provider %q description turn failed: %s", v.target.Provider, failed.Event.Failure.Message)
	}
	finished, ok := res.Terminal.(routing.Finished)
	if !ok || finished.Event.Status.Kind() != canon.StatusCompleted {
		return "", fmt.Errorf("provider %q description turn did not complete", v.target.Provider)
	}
	text := strings.TrimSpace(sink.assistantText())
	if text == "" {
		return "", fmt.Errorf("provider %q description turn produced no text", v.target.Provider)
	}
	return text, nil
}

func imageRef(img canon.ImageContent) string {
	h := fnv.New64a()
	_, _ = h.Write(img.Data)
	subtype := ""
	if _, s, ok := strings.Cut(img.MIMEType, "/"); ok {
		subtype = strings.ToLower(s)
	}
	if subtype == "jpeg" {
		subtype = "jpg"
	}
	subtype = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, subtype)
	if subtype == "" {
		subtype = "png"
	}
	return fmt.Sprintf("image-%x.%s", h.Sum64(), subtype)
}

func applyImageDescriptions(req canon.Request, descriptions []string) canon.Request {
	idx := 0
	replace := func(c canon.Content) canon.Content {
		img, ok := c.(canon.ImageContent)
		if !ok {
			return c
		}
		if idx >= len(descriptions) {
			return canon.TextContent{Text: "[image description unavailable]"}
		}
		text := strings.ReplaceAll(descriptions[idx], "</image>", "<\\/image>")
		idx++
		return canon.TextContent{Text: "<image path=\"attachment://" + imageRef(img) + "\">\n" + text + "\n</image>"}
	}
	if len(req.Instructions) > 0 {
		instructions := make([]canon.Content, len(req.Instructions))
		for i, c := range req.Instructions {
			instructions[i] = replace(c)
		}
		req.Instructions = instructions
	}
	input := make([]canon.Item, len(req.Input))
	for i, item := range req.Input {
		switch it := item.(type) {
		case canon.Message:
			content := make([]canon.Content, len(it.Content))
			for j, c := range it.Content {
				content[j] = replace(c)
			}
			input[i] = canon.Message{ID: it.ID, Role: it.Role, Content: content}
		case canon.FunctionOutput:
			output := make([]canon.Content, len(it.Output))
			for j, c := range it.Output {
				output[j] = replace(c)
			}
			input[i] = canon.FunctionOutput{ID: it.ID, CallID: it.CallID, Output: output}
		default:
			input[i] = item
		}
	}
	req.Input = input
	return req
}
