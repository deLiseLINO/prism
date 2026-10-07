package openaierr

import "testing"

func TestAPIBase(t *testing.T) {
	cases := map[string]string{
		"https://gw.example":                      "https://gw.example/v1",
		"https://gw.example/":                     "https://gw.example/v1",
		"http://127.0.0.1:1234":                   "http://127.0.0.1:1234/v1",
		"https://gw.example/v1/":                  "https://gw.example/v1",
		"https://gw.example/v1/chat/completions":  "https://gw.example/v1",
		"https://gw.example/chat/completions":     "https://gw.example/v1",
		"https://gw.example/v1/responses":         "https://gw.example/v1",
		"https://gw.example/v1/messages":          "https://gw.example/v1",
		"https://gw.example/openai":               "https://gw.example/openai/v1",
		"https://gw.example/api/paas/v4":          "https://gw.example/api/paas/v4",
		"https://gw.example/api/paas/v4/":         "https://gw.example/api/paas/v4",
		"https://gw.example/v1beta/openai":        "https://gw.example/v1beta/openai/v1",
		"https://gw.example/v1beta":               "https://gw.example/v1beta",
		" https://gw.example/openai/v1/responses": "https://gw.example/openai/v1",
	}
	for in, want := range cases {
		if got := APIBase(in); got != want {
			t.Fatalf("APIBase(%q) = %q, want %q", in, got, want)
		}
	}
}
