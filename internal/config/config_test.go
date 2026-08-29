package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validDoc() Document {
	return Document{
		Version: SchemaVersion,
		Daemon:  Daemon{Listen: "127.0.0.1:8787"},
		Providers: map[string]Provider{
			"codex-main": {Wire: WireCodex, DefaultModel: "gpt-5.2-codex", Models: []string{"gpt-5.2-codex", "gpt-5.2"}},
			"ag":         {Wire: WireAntigravity, Pool: &PoolSettings{Strategy: PoolQuota, AutoSwitchThreshold: 0.8, MaxFailovers: 3}},
		},
		Combos: map[string]Combo{
			"primary": {
				Targets:     []Target{{Provider: "codex-main", Model: "gpt-5.2-codex", Weight: 2}, {Provider: "ag", Model: "gemini-3-pro"}},
				Strategy:    ComboFailover,
				StickyLimit: 4,
				DisplayName: "Primary",
				ImageInput:  true,
			},
		},
		Routes:  map[string]string{"primary": "primary", "gpt-5.2": "codex-main/gpt-5.2"},
		Aliases: map[string]string{"prism-primary": "primary", "claude-prism-codex-main--gpt-5-2-codex": "codex-main/gpt-5.2-codex"},
	}
}

func TestValidateAcceptsWellFormedDocument(t *testing.T) {
	if err := validDoc().validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidateRejectsUnknownWire(t *testing.T) {
	d := validDoc()
	p := d.Providers["codex-main"]
	p.Wire = Wire("openai")
	d.Providers["codex-main"] = p
	if err := d.validate(); !errors.Is(err, ErrUnknownWire) {
		t.Fatalf("want ErrUnknownWire, got %v", err)
	}
}

func TestValidateRejectsUnknownComboStrategy(t *testing.T) {
	d := validDoc()
	c := d.Combos["primary"]
	c.Strategy = ComboStrategy("weighted")
	d.Combos["primary"] = c
	if err := d.validate(); !errors.Is(err, ErrUnknownComboStrategy) {
		t.Fatalf("want ErrUnknownComboStrategy, got %v", err)
	}
}

func TestValidateRejectsUnknownPoolStrategy(t *testing.T) {
	d := validDoc()
	p := d.Providers["ag"]
	p.Pool.Strategy = PoolStrategy("random")
	d.Providers["ag"] = p
	if err := d.validate(); !errors.Is(err, ErrUnknownPoolStrategy) {
		t.Fatalf("want ErrUnknownPoolStrategy, got %v", err)
	}
}

func TestValidateRejectsMalformedAliasFamily(t *testing.T) {
	for _, key := range []string{"prism-", "prism-A_B", "prism-a--b", "claude-prism-prov", "claude-prism--model", "claude-prism-p--m--x"} {
		d := validDoc()
		d.Aliases[key] = "primary"
		if err := d.validate(); !errors.Is(err, ErrMalformedAlias) {
			t.Fatalf("alias %q: want ErrMalformedAlias, got %v", key, err)
		}
	}
}

func TestValidateRejectsInvalidRouteTarget(t *testing.T) {
	cases := map[string]Document{
		"unknown combo":         withRoute(validDoc(), "x", "missing"),
		"unknown provider":      withRoute(validDoc(), "x", "missing/gpt"),
		"unknown model":         withRoute(validDoc(), "x", "codex-main/nope"),
		"missing model part":    withRoute(validDoc(), "x", "codex-main/"),
		"neither combo nor ref": withRoute(validDoc(), "x", "gpt-5.2"),
	}
	for name, d := range cases {
		if err := d.validate(); !errors.Is(err, ErrInvalidTarget) {
			t.Fatalf("%s: want ErrInvalidTarget, got %v", name, err)
		}
	}
}

func withRoute(d Document, k, v string) Document {
	if d.Routes == nil {
		d.Routes = map[string]string{}
	}
	d.Routes[k] = v
	return d
}

func TestValidateRejectsResponsesProviderWithoutBaseURL(t *testing.T) {
	d := validDoc()
	d.Providers["custom"] = Provider{Wire: WireOpenAIResponses}
	if err := d.validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("want ErrInvalidValue, got %v", err)
	}
}

func TestValidateRejectsWrongSchemaVersion(t *testing.T) {
	d := validDoc()
	d.Version = 99
	if err := d.validate(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestDocumentHasNoSecretFields(t *testing.T) {
	d := validDoc()
	p := d.Providers["codex-main"]
	p.APIKeyRef = "env:OPENAI_API_KEY"
	d.Providers["codex-main"] = p
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var flat map[string]any
	if err := json.Unmarshal(b, &flat); err != nil {
		t.Fatal(err)
	}
	var keys []string
	collectKeys(flat, &keys)
	for _, k := range keys {
		for _, banned := range []string{"token", "secret", "password", "apikey", "api_key"} {
			if k == banned {
				t.Fatalf("config document contains secret field %q", k)
			}
		}
	}
	if !contains(keys, "apikeyref") {
		t.Fatalf("config document lost apiKeyRef field")
	}
}

func collectKeys(v any, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			*out = append(*out, strings.ToLower(k))
			collectKeys(x, out)
		}
	case []any:
		for _, x := range t {
			collectKeys(x, out)
		}
	}
}
