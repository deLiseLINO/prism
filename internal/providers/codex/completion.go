package codex

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/deLiseLINO/prism/internal/canon"
)

func validateCustomInput(raw json.RawMessage, old *streamedItem) error {
	var fields struct {
		Type  string          `json:"type"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if fields.Type == "custom_tool_call" {
		if len(fields.Input) == 0 {
			if old == nil || old.customInput == nil {
				return errors.New("finished custom tool lacks string input")
			}
		} else {
			var input *string
			if json.Unmarshal(fields.Input, &input) != nil || input == nil {
				return errors.New("finished custom tool input must be a string")
			}
		}
	}
	return nil
}

func (it *streamedItem) checkReceipt(previous, next *string) error {
	if previous != nil && *previous != *next {
		return errors.New("conflicting final receipt")
	}
	if !strings.HasPrefix(*next, it.prefix.String()) {
		return errors.New("final receipt conflicts with received deltas")
	}
	return nil
}

func (it *streamedItem) reconcile(item canon.Item) error {
	switch next := item.(type) {
	case canon.FunctionCall:
		old, ok := it.item.(canon.FunctionCall)
		if !ok || old.CallID != next.CallID || old.Name != next.Name {
			return errors.New("final function identity changed")
		}
		if !strings.HasPrefix(string(next.Arguments), it.prefix.String()) {
			return errors.New("final arguments conflict with received deltas")
		}
	case canon.CustomToolCall:
		old, ok := it.item.(canon.CustomToolCall)
		if !ok || old.CallID != next.CallID || old.Name != next.Name {
			return errors.New("final custom identity changed")
		}
		if !strings.HasPrefix(next.Input, it.prefix.String()) {
			return errors.New("final input conflicts with received deltas")
		}
	case canon.Message:
		old, ok := it.item.(canon.Message)
		if !ok || old.Role != next.Role {
			return errors.New("final message role changed")
		}
		prefix := it.prefix.String()
		for _, part := range next.Content {
			if text, ok := part.(canon.TextContent); ok {
				if len(prefix) <= len(text.Text) {
					if !strings.HasPrefix(text.Text, prefix) {
						return errors.New("final text conflicts with received deltas")
					}
					prefix = ""
				} else {
					if !strings.HasPrefix(prefix, text.Text) {
						return errors.New("final text conflicts with received deltas")
					}
					prefix = prefix[len(text.Text):]
				}
			}
		}
		if prefix != "" {
			return errors.New("final text truncates received deltas")
		}
	case canon.ReasoningItem:
		prefix := it.prefix.String()
		if next.Content != "" {
			if !strings.HasPrefix(next.Content, prefix) {
				return errors.New("final reasoning conflicts with received deltas")
			}
		} else {
			for _, part := range next.Summary {
				if len(prefix) <= len(part.Text) {
					if !strings.HasPrefix(part.Text, prefix) {
						return errors.New("final summary conflicts with received deltas")
					}
					prefix = ""
				} else {
					if !strings.HasPrefix(prefix, part.Text) {
						return errors.New("final summary conflicts with received deltas")
					}
					prefix = prefix[len(part.Text):]
				}
			}
			if prefix != "" {
				return errors.New("final summary truncates received deltas")
			}
		}
	}
	return nil
}
