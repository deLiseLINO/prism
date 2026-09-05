package auth

import (
	"fmt"
	"net/url"
	"strings"
)

type AuthParam struct {
	Key   string
	Value string
}

type ProviderConfig struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	ProjectAPI   string
	OnboardAPI   string
	APIVersion   string
	UserAgent    string
	CallbackHost string
	BindHost     string
	CallbackPort int
	CallbackPath string
	ExtraParams  []AuthParam
}

func (c ProviderConfig) validate(needGoogleAPI bool) error {
	missing := func(names ...string) error {
		return fmt.Errorf("auth: provider config missing %s", strings.Join(names, ", "))
	}
	absent := []string{}
	for name, val := range map[string]string{
		"clientID":     c.ClientID,
		"authURL":      c.AuthURL,
		"tokenURL":     c.TokenURL,
		"callbackHost": c.CallbackHost,
		"bindHost":     c.BindHost,
		"callbackPath": c.CallbackPath,
	} {
		if val == "" {
			absent = append(absent, name)
		}
	}
	if len(c.Scopes) == 0 {
		absent = append(absent, "scopes")
	}
	if c.CallbackPort < 0 {
		return fmt.Errorf("auth: negative callback port")
	}
	if needGoogleAPI {
		for name, val := range map[string]string{
			"clientSecret": c.ClientSecret,
			"userInfoURL":  c.UserInfoURL,
			"projectAPI":   c.ProjectAPI,
			"onboardAPI":   c.OnboardAPI,
			"apiVersion":   c.APIVersion,
			"userAgent":    c.UserAgent,
		} {
			if val == "" {
				absent = append(absent, name)
			}
		}
	}
	if len(absent) > 0 {
		return missing(absent...)
	}
	return nil
}

func (c ProviderConfig) RedirectURI(port int) string {
	return fmt.Sprintf("http://%s:%d%s", c.CallbackHost, port, c.CallbackPath)
}

func encodeParams(params []AuthParam) string {
	var b strings.Builder
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.Key))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.Value))
	}
	return b.String()
}

func baseParams(c ProviderConfig, state, verifier, redirectURI string) []AuthParam {
	return []AuthParam{
		{Key: "response_type", Value: "code"},
		{Key: "client_id", Value: c.ClientID},
		{Key: "redirect_uri", Value: redirectURI},
		{Key: "scope", Value: strings.Join(c.Scopes, " ")},
		{Key: "code_challenge", Value: s256Challenge(verifier)},
		{Key: "code_challenge_method", Value: "S256"},
		{Key: "state", Value: state},
	}
}
