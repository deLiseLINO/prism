package config

import (
	"fmt"
	"strings"
)

const (
	ClaudeAliasPrefix = "claude-"
	ClaudeSeparator   = "--"
)

func Sanitize(s string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}


func ClaudeAlias(provider, model string) (string, error) {
	p, m := Sanitize(provider), Sanitize(model)
	if p == "" || m == "" {
		return "", fmt.Errorf("%w: empty sanitized part in (%q, %q)", ErrMalformedAlias, provider, model)
	}
	return ClaudeAliasPrefix + p + ClaudeSeparator + m, nil
}

func ParseClaudeAlias(alias string) (provider, model string, err error) {
	rest, ok := strings.CutPrefix(alias, ClaudeAliasPrefix)
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrMalformedAlias, alias)
	}
	provider, model, ok = strings.Cut(rest, ClaudeSeparator)
	if !ok || !isSanitized(provider) || !isSanitized(model) {
		return "", "", fmt.Errorf("%w: %q", ErrMalformedAlias, alias)
	}
	return provider, model, nil
}

func isSanitized(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		switch {
		case alnum:
		case c == '-':
			if i == 0 || i == len(s)-1 || s[i-1] == '-' {
				return false
			}
		default:
			return false
		}
	}
	return true
}
