package openaierr

import (
	"testing"

	"prism/internal/provider"
)

func TestClassForStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		want   provider.ErrorClass
	}{
		{"unauthorized", 401, "", provider.ClassUnauthorized},
		{"forbidden", 403, "", provider.ClassForbidden},
		{"not found", 404, "", provider.ClassNotFound},
		{"rate limited", 429, "", provider.ClassRateLimited},
		{"server", 500, "", provider.ClassServer},
		{"invalid", 400, "", provider.ClassInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassForStatus(tc.status, tc.code); got != tc.want {
				t.Fatalf("ClassForStatus(%d, %q) = %d, want %d", tc.status, tc.code, got, tc.want)
			}
		})
	}
}
