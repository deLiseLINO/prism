package openaierr

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"prism/internal/provider"
)

func responseWith(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestHTTPErrorShapes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"object", 400, `{"error":{"message":"bad param","code":"invalid_value"}}`, "bad param"},
		{"string", 400, `{"error":"just broken"}`, "just broken"},
		{"message", 400, `{"message":"top level"}`, "top level"},
		{"nonjson", 400, `not json`, "upstream http 400"},
		{"empty", 500, ``, "upstream http 500"},
	}
	for _, tc := range cases {
		err := HTTPError(responseWith(tc.status, tc.body), "test")
		re, ok := err.(provider.RunError)
		if !ok {
			t.Fatalf("%s: error = %T, want provider.RunError", tc.name, err)
		}
		if got := re.Cause.Error(); !strings.Contains(got, tc.want) {
			t.Fatalf("%s: cause = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHTTPErrorContextLengthCode(t *testing.T) {
	err := HTTPError(responseWith(400, `{"error":{"message":"too long","code":"context_length_exceeded"}}`), "test")
	re, ok := err.(provider.RunError)
	if !ok {
		t.Fatalf("error = %T, want provider.RunError", err)
	}
	if re.Class != provider.ClassContextLength {
		t.Fatalf("class = %d, want context length", re.Class)
	}
}
