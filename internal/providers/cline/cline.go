package cline

import (
	"strings"
)

const DefaultBaseURL = "https://api.cline.bot"


func GatewayBase(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		trimmed = DefaultBaseURL
	}
	return strings.TrimSuffix(strings.TrimRight(trimmed, "/"), "/api/v1")
}

// ProductHeaders are the headers the gateway requires on every call. Without
// them it answers 403 "only available via product surfaces".
func ProductHeaders() map[string]string {
	return map[string]string{
		"User-Agent":         "Cline/3.0.49",
		"HTTP-Referer":       "https://cline.bot",
		"X-Title":            "Cline",
		"X-IS-MULTIROOT":     "false",
		"X-CLIENT-TYPE":      "cline-cli",
		"X-CLIENT-VERSION":   "3.0.49",
		"X-PLATFORM":         "cline",
		"X-PLATFORM-VERSION": "v24.3.0",
	}
}

const workosPrefix = "workos:"

// The token endpoint returns a bare JWT while the gateway expects the
// workos-prefixed bearer the Cline CLI persists.
func EnsureWorkosPrefix(token string) string {
	if strings.HasPrefix(token, workosPrefix) {
		return token
	}
	return workosPrefix + token
}
