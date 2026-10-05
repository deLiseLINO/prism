package agentinstall

import (
	"strings"

	"golang.org/x/mod/semver"
)

func parsedVersion(text string, last bool) string {
	values := strings.FieldsFunc(text, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune(".+-", r))
	})
	if last {
		for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
			values[i], values[j] = values[j], values[i]
		}
	}
	for _, value := range values {
		if normalizedVersion(value) != "" {
			return strings.TrimPrefix(value, "v")
		}
	}
	return ""
}
func normalizedVersion(value string) string {
	value = strings.TrimPrefix(value, "v")
	numeric := strings.SplitN(strings.SplitN(value, "-", 2)[0], "+", 2)[0]
	if strings.Count(numeric, ".") != 1 && strings.Count(numeric, ".") != 2 {
		return ""
	}
	if strings.Count(numeric, ".") == 1 {
		value = numeric + ".0" + value[len(numeric):]
	}
	return semver.Canonical("v" + value)
}
func compareVersions(a, b string) int {
	return semver.Compare(normalizedVersion(a), normalizedVersion(b))
}
