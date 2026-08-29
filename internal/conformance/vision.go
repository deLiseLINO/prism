package conformance

import (
	"fmt"

	"prism/internal/canon"
)

func toolResultRequest(vector map[string]any) (canon.Request, error) {
	callID, _ := vector["callId"].(string)
	if callID == "" {
		return canon.Request{}, fmt.Errorf("tool-result vector: missing callId")
	}
	raw, ok := vector["result"]
	if !ok {
		return canon.Request{}, fmt.Errorf("tool-result vector: missing result")
	}
	parts, ok := raw.([]any)
	if !ok {
		return canon.Request{}, fmt.Errorf("tool-result vector: result must be an array")
	}
	var output []canon.Content
	for _, p := range parts {
		rec, ok := p.(map[string]any)
		if !ok {
			return canon.Request{}, fmt.Errorf("tool-result vector: result part must be an object")
		}
		content, err := responsesPartToContent(rec)
		if err != nil {
			return canon.Request{}, err
		}
		output = append(output, content)
	}
	if len(output) == 0 {
		return canon.Request{}, fmt.Errorf("tool-result vector: result has no content parts")
	}
	return canon.Request{
		Model: "fixture-model",
		Input: []canon.Item{canon.FunctionOutput{CallID: canon.CallID(callID), Output: output}},
	}, nil
}

func modalityPath(vector map[string]any) string {
	hasImage, _ := vector["requestHasImage"].(bool)
	if hasImage {
		modalities, _ := vector["modelInputModalities"].([]any)
		for _, m := range modalities {
			if s, ok := m.(string); ok && s == "image" {
				return "native"
			}
		}
	}
	if sidecar, ok := vector["visionSidecar"].(map[string]any); ok {
		if enabled, ok := sidecar["enabled"].(bool); ok && enabled {
			return "sidecar"
		}
	}
	return "unsupported"
}

func silentImageDrop(vector map[string]any) bool {
	hasImage, _ := vector["requestHasImage"].(bool)
	if !hasImage {
		return false
	}
	path := modalityPath(vector)
	return path != "native" && path != "sidecar" && path != "unsupported"
}
