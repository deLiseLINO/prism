package daemon

import "testing"

func TestCustomModelsURLFollowsRunnerBaseShapes(t *testing.T) {
	cases := map[string]string{
		"https://gw.example":                      "https://gw.example/v1/models",
		"https://gw.example/":                     "https://gw.example/v1/models",
		"https://gw.example/v1":                   "https://gw.example/v1/models",
		"https://gw.example/v1/":                  "https://gw.example/v1/models",
		"https://gw.example/v1/chat/completions":  "https://gw.example/v1/models",
		"https://gw.example/chat/completions":     "https://gw.example/v1/models",
		"https://gw.example/v1/responses":         "https://gw.example/v1/models",
		"https://gw.example/v1/messages":          "https://gw.example/v1/models",
		"https://gw.example/openai/v1/":           "https://gw.example/openai/v1/models",
		" https://gw.example/openai/v1/responses": "https://gw.example/openai/v1/models",
	}
	for base, want := range cases {
		if got := customModelsURL(base); got != want {
			t.Fatalf("customModelsURL(%q) = %q, want %q", base, got, want)
		}
	}
}
