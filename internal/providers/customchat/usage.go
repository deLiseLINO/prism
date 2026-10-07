package customchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/providers/usagewire"
)

func decodeChatUsage(raw json.RawMessage) (canon.Usage, error) {
	if len(raw) == 0 {
		return canon.Usage{}, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return canon.Usage{}, errors.New("malformed usage")
	}
	return mergeChatUsage(value, canon.Usage{})
}

func mergeChatUsage(value any, previous canon.Usage) (canon.Usage, error) {
	if value == nil {
		return previous, nil
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return previous, errors.New("usage must be an object")
	}
	next := previous
	for _, field := range []struct {
		name   string
		target *int64
	}{{"prompt_tokens", &next.InputTokens}, {"completion_tokens", &next.OutputTokens}, {"total_tokens", &next.TotalTokens}} {
		if value, supplied := fields[field.name]; supplied {
			count, err := usagewire.Number(value)
			if err != nil {
				return previous, fmt.Errorf("usage.%s: %w", field.name, err)
			}
			*field.target = count
		}
	}
	for _, detail := range []struct {
		name, token string
		target      *int64
	}{{"prompt_tokens_details", "cached_tokens", &next.CachedInputTokens}, {"completion_tokens_details", "reasoning_tokens", &next.ReasoningTokens}} {
		if value, supplied := fields[detail.name]; supplied && value != nil {
			values, ok := value.(map[string]any)
			if !ok {
				return previous, errors.New("usage detail must be an object")
			}
			if value, supplied := values[detail.token]; supplied {
				count, err := usagewire.Number(value)
				if err != nil {
					return previous, fmt.Errorf("usage.%s.%s: %w", detail.name, detail.token, err)
				}
				*detail.target = count
			}
		}
	}
	if next.InputTokens > math.MaxInt64-next.OutputTokens {
		return previous, errors.New("usage token sum exceeds int64")
	}
	total := next.InputTokens + next.OutputTokens
	if _, supplied := fields["total_tokens"]; !supplied {
		next.TotalTokens = total
	}
	if next.TotalTokens != total {
		return previous, errors.New("usage total does not match token counts")
	}
	if next.CachedInputTokens > next.InputTokens || next.ReasoningTokens > next.OutputTokens {
		return previous, errors.New("usage detail exceeds inclusive count")
	}
	return next, nil
}
