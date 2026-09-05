package customchat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"prism/internal/canon"
	"prism/internal/provider"
)

type aggregateResponse struct {
	ID      string            `json:"id"`
	Choices []aggregateChoice `json:"choices"`
	Usage   *usageJSON        `json:"usage"`
	Error   *aggregateError   `json:"error"`
}

type aggregateError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
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
	ToolCalls        []aggregateToolCall `json:"tool_calls"`
}

type aggregateToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type usageJSON struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u *usageJSON) canon() canon.Usage {
	if u == nil {
		return canon.Usage{}
	}
	var out canon.Usage
	out.InputTokens = u.PromptTokens
	out.OutputTokens = u.CompletionTokens
	out.TotalTokens = u.TotalTokens
	if u.PromptTokensDetails != nil {
		out.CachedInputTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

func (r *Runner) runAggregate(body io.Reader, sink provider.Sink) error {
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
	if payload.Error != nil {
		message := payload.Error.Message
		if message == "" {
			message = "upstream request failed"
		}
		if err := emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message}}); err != nil {
			return err
		}
		return runError(provider.TerminalEmitted, provider.ClassServer, true, false, 0, errors.New(message))
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
	if choice.Message.ReasoningContent != "" {
		reasoning := canon.ReasoningItem{ID: canon.ItemID(responseID + "-reasoning"), Content: choice.Message.ReasoningContent}
		if err := emit(canon.ItemStarted{Item: reasoning}); err != nil {
			return err
		}
		if err := emit(canon.ItemFinished{Item: reasoning}); err != nil {
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
		itemID := tc.ID
		if itemID == "" {
			itemID = fmt.Sprintf("tool-call-%d", i)
		}
		fc := canon.FunctionCall{
			ID:     canon.ItemID(itemID),
			CallID: canon.CallID(tc.ID),
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
	return emit(canon.TurnFinished{Status: status, Usage: payload.Usage.canon()})
}
