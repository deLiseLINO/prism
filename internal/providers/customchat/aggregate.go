package customchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
)

type aggregateResponse struct {
	ID      string            `json:"id"`
	Choices []aggregateChoice `json:"choices"`
	Usage   json.RawMessage   `json:"usage"`
	Error   json.RawMessage   `json:"error"`
}

type aggregateChoice struct {
	Index        int              `json:"index"`
	Message      aggregateMessage `json:"message"`
	FinishReason string           `json:"finish_reason"`
}

type aggregateMessage struct {
	Role             string              `json:"role"`
	Content          *string             `json:"content"`
	ReasoningContent string              `json:"reasoning_content"`
	Reasoning        string              `json:"reasoning"`
	ReasoningText    string              `json:"reasoning_text"`
	ToolCalls        []aggregateToolCall `json:"tool_calls"`
}

func (m aggregateMessage) reasoningText() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	if m.Reasoning != "" {
		return m.Reasoning
	}
	return m.ReasoningText
}

type aggregateToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (r *Runner) runAggregate(body io.Reader, sink provider.Sink, custom customTools) error {
	raw, err := io.ReadAll(io.LimitReader(body, 100<<20))
	if err != nil {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, err)
	}
	var payload aggregateResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0,
			fmt.Errorf("customchat: malformed upstream response: %w", err))
	}
	emit := func(ev canon.Event) error {
		if err := sink.Emit(ev); err != nil {
			return runError(provider.UnsafeReplay, provider.ClassTransport, true, false, 0, err)
		}
		return nil
	}
	if e := bytes.TrimSpace(payload.Error); len(e) > 0 && string(e) != "null" {
		parsed, ok := openaierr.Parse(raw)
		if !ok {
			return runError(provider.Retryable, provider.ClassTransport, true, true, 0, errors.New("customchat: error response carried no error value"))
		}
		copied := parsed
		message := openaierr.Text(parsed)
		if err := emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message, Provider: &copied}}); err != nil {
			return err
		}
		cause := message
		if cause == "" {
			cause = "provider error"
		}
		return provider.RunError{Kind: provider.TerminalEmitted, Class: openaierr.ClassForInband(parsed, provider.ClassServer), Accepted: true, Cause: errors.New(cause), Reported: &copied}
	}
	if len(payload.Choices) == 0 {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0,
			errors.New("customchat: upstream response has no choices"))
	}
	if len(payload.Choices) > 1 {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0,
			fmt.Errorf("customchat: upstream response carries %d choices; only one is representable", len(payload.Choices)))
	}
	choice := payload.Choices[0]
	if choice.Index != 0 {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, errors.New("customchat: choice index must be zero"))
	}
	usage, err := decodeChatUsage(payload.Usage)
	if err != nil {
		return runError(provider.Retryable, provider.ClassTransport, true, true, 0, err)
	}
	for i, tc := range choice.Message.ToolCalls {
		if tc.Type != "" && tc.Type != "function" || tc.Function.Name == "" {
			return runError(provider.Retryable, provider.ClassTransport, true, true, 0, fmt.Errorf("customchat: malformed upstream tool call %d", i))
		}
		if custom.has(tc.Function.Name) {
			if _, err := unwrapCustomInput(tc.Function.Arguments); err != nil {
				return runError(provider.Retryable, provider.ClassTransport, true, true, 0, err)
			}
		} else if tc.Function.Arguments != "" && !json.Valid([]byte(tc.Function.Arguments)) {
			return runError(provider.Retryable, provider.ClassTransport, true, true, 0, errors.New("customchat: malformed upstream function arguments"))
		}
	}
	status, known := finishStatus(choice.FinishReason)
	if !known {
		message := fmt.Sprintf("upstream finish reason %q is not representable", choice.FinishReason)
		if err := emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message}}); err != nil {
			return err
		}
		return runError(provider.TerminalEmitted, provider.ClassServer, true, false, 0, errors.New(message))
	}
	responseID := payload.ID
	if responseID == "" {
		responseID = "assistant"
	}
	if reasoning := choice.Message.reasoningText(); reasoning != "" {
		item := canon.ReasoningItem{ID: canon.ItemID(responseID + "-reasoning"), Content: reasoning}
		if err := emit(canon.ItemStarted{Item: item}); err != nil {
			return err
		}
		if err := emit(canon.ItemFinished{Item: item}); err != nil {
			return err
		}
	}
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		msg := canon.Message{
			ID:      canon.ItemID(responseID),
			Role:    canon.RoleAssistant,
			Content: []canon.Content{canon.TextContent{Text: *choice.Message.Content}},
		}
		if err := emit(canon.ItemStarted{Item: msg}); err != nil {
			return err
		}
		if err := emit(canon.TextDelta{ItemID: msg.ID, Text: *choice.Message.Content}); err != nil {
			return err
		}
		if err := emit(canon.ItemFinished{Item: msg}); err != nil {
			return err
		}
	}
	for i, tc := range choice.Message.ToolCalls {
		if tc.Type != "" && tc.Type != "function" {
			return runError(provider.Retryable, provider.ClassTransport, true, true, 0,
				fmt.Errorf("customchat: upstream tool call type %q is not representable", tc.Type))
		}
		if tc.Function.Name == "" {
			return runError(provider.Retryable, provider.ClassTransport, true, true, 0,
				fmt.Errorf("customchat: upstream tool call %d is missing a function name", i))
		}
		callID := tc.ID
		if callID == "" {
			callID = mintCallID()
		}
		if custom.has(tc.Function.Name) {
			input, _ := unwrapCustomInput(tc.Function.Arguments)
			call := canon.CustomToolCall{ID: canon.ItemID(callID), CallID: canon.CallID(callID), Name: canon.ToolName(tc.Function.Name)}
			if err := emit(canon.ItemStarted{Item: call}); err != nil {
				return err
			}
			if input != "" {
				if err := emit(canon.CustomToolInputDelta{ItemID: call.ID, Text: input}); err != nil {
					return err
				}
			}
			call.Input = input
			if err := emit(canon.ItemFinished{Item: call}); err != nil {
				return err
			}
			continue
		}
		fc := canon.FunctionCall{
			ID:     canon.ItemID(callID),
			CallID: canon.CallID(callID),
			Name:   canon.ToolName(tc.Function.Name),
		}
		if tc.Function.Arguments != "" {
			fc.Arguments = []byte(tc.Function.Arguments)
		}
		if err := emit(canon.ItemStarted{Item: fc}); err != nil {
			return err
		}
		if len(fc.Arguments) > 0 {
			if err := emit(canon.ToolArgumentsDelta{ItemID: fc.ID, Bytes: fc.Arguments}); err != nil {
				return err
			}
		}
		if err := emit(canon.ItemFinished{Item: fc}); err != nil {
			return err
		}
	}
	return emit(canon.TurnFinished{Status: status, Usage: usage})
}
