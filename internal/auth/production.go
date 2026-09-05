package auth

// Production OAuth client configuration. The client identifiers (and the
// Google desktop client secret) are embedded public values of the shipped
// applications, not user secrets.

var CodexProduction = ProviderConfig{
	ClientID: "app_EMoamEEZ73f0CkXaXp7hrann",
	AuthURL:  "https://auth.openai.com/oauth/authorize",
	TokenURL: "https://auth.openai.com/oauth/token",
	Scopes: []string{
		"openid",
		"profile",
		"email",
		"offline_access",
		"api.connectors.read",
		"api.connectors.invoke",
	},
	CallbackHost: "localhost",
	BindHost:     "127.0.0.1",
	CallbackPort: 1455,
	CallbackPath: "/auth/callback",
	ExtraParams: []AuthParam{
		{Key: "codex_cli_simplified_flow", Value: "true"},
		{Key: "originator", Value: "prism"},
		{Key: "id_token_add_organizations", Value: "true"},
	},
}

var AntigravityProduction = ProviderConfig{
	ClientID:     "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com",
	ClientSecret: "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",
	AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:     "https://oauth2.googleapis.com/token",
	UserInfoURL:  "https://www.googleapis.com/oauth2/v2/userinfo",
	ProjectAPI:   "https://cloudcode-pa.googleapis.com",
	OnboardAPI:   "https://daily-cloudcode-pa.googleapis.com",
	APIVersion:   "v1internal",
	Scopes: []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/cclog",
		"https://www.googleapis.com/auth/experimentsandconfigs",
	},
	CallbackHost: "127.0.0.1",
	BindHost:     "127.0.0.1",
	CallbackPort: 51121,
	CallbackPath: "/callback",
	ExtraParams: []AuthParam{
		{Key: "access_type", Value: "offline"},
		{Key: "prompt", Value: "consent"},
	},
	UserAgent: "antigravity/ide/2.5.5 (os_type=windows; arch=amd64; aidev_client; auth_method=oauth)",
}
