package customchat

import "testing"

func TestChatURLKeepsVersionedBase(t *testing.T) {
	cases := map[string]string{
		"https://gw.example/api/paas/v4":                  "https://gw.example/api/paas/v4/chat/completions",
		"https://gw.example/api/paas/v4/":                 "https://gw.example/api/paas/v4/chat/completions",
		"https://gw.example/api/paas/v4/chat/completions": "https://gw.example/api/paas/v4/chat/completions",
		"https://gw.example/v1beta":                       "https://gw.example/v1beta/chat/completions",
		"https://gw.example/v2/":                          "https://gw.example/v2/chat/completions",
		"https://gw.example/v1":                           "https://gw.example/v1/chat/completions",
		"https://gw.example/openai":                       "https://gw.example/openai/v1/chat/completions",
	}
	for base, want := range cases {
		if got := chatURL(base); got != want {
			t.Fatalf("chatURL(%q) = %q, want %q", base, got, want)
		}
	}
}
