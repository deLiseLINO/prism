package antigravity

import (
	"strings"
	"unicode"
)

const signatureSentinel = "skip_thought_signature_validator"

var signatureForeignPrefixes = []string{
	"fc", "ctc", "tsc", "call", "msg", "rs", "resp", "reasoning",
	"item", "ws", "toolu", "tool", "func", "function",
}

func likelyRealSignature(sig string) bool {
	if len(sig) < 16 {
		return false
	}
	if sig == signatureSentinel {
		return false
	}
	lower := strings.ToLower(sig)
	for _, prefix := range signatureForeignPrefixes {
		if strings.HasPrefix(lower, prefix) {
			rest := lower[len(prefix):]
			if rest[0] == '-' || rest[0] == '_' {
				return false
			}
		}
	}
	for _, r := range sig {
		if r == '+' || r == '/' || r == '_' || r == '=' || r == '-' {
			continue
		}
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return true
}
