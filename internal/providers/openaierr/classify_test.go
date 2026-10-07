package openaierr

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/provider"
)

func TestHTTPErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   provider.ErrorClass
	}{
		{"code only", 400, `{"error":{"message":"x","code":"context_length_exceeded"}}`, provider.ClassContextLength},
		{"numeric code with overflow message", 400, `{"error":{"message":"This endpoint's maximum context length is 8192 tokens. However, you requested about 9000 tokens.","code":400}}`, provider.ClassContextLength},
		{"openai message with null code", 400, `{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens.","type":"invalid_request_error","code":null}}`, provider.ClassContextLength},
		{"payload too large", 413, `{"error":{"message":"Request too large"}}`, provider.ClassContextLength},
		{"prompt too long", 400, `{"error":{"message":"prompt is too long: 250000 tokens > 200000 maximum"}}`, provider.ClassContextLength},
		{"unrelated 400", 400, `{"error":{"message":"temperature must be between 0 and 2"}}`, provider.ClassInvalidRequest},
		{"payment required", 402, `{"error":{"message":"insufficient balance"}}`, provider.ClassQuotaExhausted},
		{"html 502", 502, `<html>bad gateway</html>`, provider.ClassServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			re := HTTPError(responseWith(tc.status, tc.body), "test").(provider.RunError)
			if re.Class != tc.want {
				t.Fatalf("class = %d, want %d", re.Class, tc.want)
			}
		})
	}
}
