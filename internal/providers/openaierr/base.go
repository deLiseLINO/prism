package openaierr

import (
	"regexp"
	"strings"
)

var (
	endpointSuffixes = []string{"/chat/completions", "/responses", "/messages", "/models"}
	versionSegment   = regexp.MustCompile(`^v\d+([a-z]+\d*)?$`)
)

// APIBase resolves a configured base URL to the API root that endpoint paths
// hang from. A base already ending in an endpoint path loses it; a base whose
// last segment is a version (/v1, /v4, /v1beta) is kept as the root; any other
// base gets /v1 appended.
func APIBase(base string) string {
	u := strings.TrimRight(strings.TrimSpace(base), "/")
	for _, suffix := range endpointSuffixes {
		if trimmed, ok := strings.CutSuffix(u, suffix); ok {
			u = strings.TrimRight(trimmed, "/")
			break
		}
	}
	if i := strings.LastIndex(u, "/"); i >= 0 && !strings.HasSuffix(u, "//") && versionSegment.MatchString(u[i+1:]) {
		return u
	}
	return u + "/v1"
}
