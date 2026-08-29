package conformance

import (
	"context"
	"errors"

	"prism/internal/canon"
)

var errUnhandledAdapterVector = errors.New("adapter vector not handled by the reasoning router")

func (r *Runner) buildReasoningVector(ctx context.Context, c Case, vector map[string]any, buildOpts BuildOptions) ([]*UpstreamRequest, error) {
	switch c.ID {
	case "reasoning-core.protocol.effort-mapping":
		return r.buildEffortMapping(ctx, vector, buildOpts)
	case "reasoning-core.protocol.replay":
		return r.buildReasoningReplay(ctx, vector, buildOpts)
	case "reasoning-core.protocol.private-content-isolation":
		return r.buildPrivateIsolation(ctx, vector, buildOpts)
	}
	return nil, errUnhandledAdapterVector
}

func (r *Runner) buildEffortMapping(ctx context.Context, vector map[string]any, buildOpts BuildOptions) ([]*UpstreamRequest, error) {
	if r.Build == nil {
		return nil, errors.New("effort-mapping scenario requires the openai-chat builder")
	}
	req := VectorToRequest(vector)
	buildOpts.BaseURL = "http://127.0.0.1:1/v1"
	buildOpts.ReasoningWireFormat, _ = vector["reasoningWireFormat"].(string)
	if m, ok := vector["reasoningEffortMap"].(map[string]any); ok {
		buildOpts.ReasoningEffortMap = map[string]string{}
		for k, v := range m {
			if s, ok := v.(string); ok {
				buildOpts.ReasoningEffortMap[k] = s
			}
		}
	}
	built, err := r.Build.Build(ctx, req, buildOpts)
	if err != nil {
		return nil, err
	}
	return []*UpstreamRequest{built}, nil
}

func (r *Runner) buildReasoningReplay(ctx context.Context, vector map[string]any, buildOpts BuildOptions) ([]*UpstreamRequest, error) {
	if r.ResponsesBuild == nil {
		return nil, errors.New("replay scenario requires the openai-responses builder")
	}
	turn1, ok := vector["turn1"].(map[string]any)
	if !ok {
		return nil, errors.New("replay vector missing turn1")
	}
	turn2, ok := vector["turn2"].(map[string]any)
	if !ok {
		return nil, errors.New("replay vector missing turn2")
	}
	reasoning, ok := turn1["reasoning"].(map[string]any)
	if !ok {
		return nil, errors.New("replay turn1 missing reasoning")
	}
	toolResult, ok := turn2["toolResult"].(map[string]any)
	if !ok {
		return nil, errors.New("replay turn2 missing toolResult")
	}
	id, _ := reasoning["id"].(string)
	text, _ := reasoning["text"].(string)
	signature, _ := reasoning["signature"].(string)
	callID, _ := toolResult["callId"].(string)
	output, _ := toolResult["output"].(string)
	first, err := r.ResponsesBuild.Build(ctx, canon.Request{
		Model:  "fixture-model",
		Stream: false,
		Input:  []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "PING"}}}},
	}, buildOpts)
	if err != nil {
		return nil, err
	}
	second, err := r.ResponsesBuild.Build(ctx, canon.Request{
		Model:  "fixture-model",
		Stream: false,
		Input: []canon.Item{
			canon.ReasoningItem{ID: canon.ItemID(id), Content: text, Signature: signature},
			canon.FunctionOutput{CallID: canon.CallID(callID), Output: []canon.Content{canon.TextContent{Text: output}}},
		},
	}, buildOpts)
	if err != nil {
		return nil, err
	}
	return []*UpstreamRequest{first, second}, nil
}

func (r *Runner) buildPrivateIsolation(ctx context.Context, vector map[string]any, buildOpts BuildOptions) ([]*UpstreamRequest, error) {
	if r.Build == nil {
		return nil, errors.New("private-content-isolation scenario requires the openai-chat builder")
	}
	req := canon.Request{
		Model:  "fixture-model",
		Stream: false,
		Input: []canon.Item{
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "PING"}}},
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "NEXT"}}},
		},
	}
	built, err := r.Build.Build(ctx, req, buildOpts)
	if err != nil {
		return nil, err
	}
	return []*UpstreamRequest{built}, nil
}
