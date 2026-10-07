package customresponses

import (
	"math"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/providers/usagewire"
)

func decodeResponseUsage(response map[string]any, previous canon.Usage) (canon.Usage, error) {
	raw, supplied := response["usage"]
	if !supplied || raw == nil {
		return previous, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return previous, malformedWire("usage must be an object")
	}
	next := previous
	for _, field := range []struct {
		name   string
		target *int64
	}{{"input_tokens", &next.InputTokens}, {"output_tokens", &next.OutputTokens}, {"total_tokens", &next.TotalTokens}} {
		if value, supplied := fields[field.name]; supplied {
			count, err := usagewire.Number(value)
			if err != nil {
				return previous, malformedWire("invalid usage." + field.name)
			}
			*field.target = count
		}
	}
	for _, detail := range []struct {
		name, token string
		target      *int64
	}{{"input_tokens_details", "cached_tokens", &next.CachedInputTokens}, {"output_tokens_details", "reasoning_tokens", &next.ReasoningTokens}} {
		if raw, supplied := fields[detail.name]; supplied && raw != nil {
			values, ok := raw.(map[string]any)
			if !ok {
				return previous, malformedWire("usage detail must be an object")
			}
			if value, supplied := values[detail.token]; supplied {
				count, err := usagewire.Number(value)
				if err != nil {
					return previous, malformedWire("invalid usage." + detail.name + "." + detail.token)
				}
				*detail.target = count
			}
		}
	}
	if next.InputTokens > math.MaxInt64-next.OutputTokens {
		return previous, malformedWire("usage token sum exceeds int64")
	}
	total := next.InputTokens + next.OutputTokens
	if _, supplied := fields["total_tokens"]; !supplied {
		next.TotalTokens = total
	}
	if next.TotalTokens != total {
		return previous, malformedWire("usage total does not match token counts")
	}
	if next.CachedInputTokens > next.InputTokens || next.ReasoningTokens > next.OutputTokens {
		return previous, malformedWire("usage detail exceeds inclusive token count")
	}
	return next, nil
}
