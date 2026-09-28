package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"prism/internal/catalog"
)

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }

func validDoc() Document {
	return Document{
		Version: SchemaVersion,
		Daemon:  Daemon{Listen: "127.0.0.1:8787"},
		Providers: map[string]Provider{
			"codex-main": {Wire: WireCodex, DefaultModel: "gpt-5.2-codex", Models: []string{"gpt-5.2-codex", "gpt-5.2"}},
			"ag":         {Wire: WireAntigravity, Pool: &PoolSettings{AccountsPath: "accounts.json"}},
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
		Aliases: map[string]string{"flash": "primary", "claude-codex-main--gpt-5-2-codex": "codex-main/gpt-5.2-codex"},
	}
}

func TestValidateAcceptsWellFormedDocument(t *testing.T) {
	if err := validDoc().validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidateRejectsHostWithEmptyAddress(t *testing.T) {
	d := validDoc()
	d.Hosts = map[string]Host{"workmac": {Address: "  "}}
	if err := d.validate(); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("want ErrEmptyField, got %v", err)
	}
	d.Hosts = map[string]Host{"workmac": {Address: "user@workmac"}}
	if err := d.validate(); err != nil {
		t.Fatalf("valid host address rejected: %v", err)
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


func TestValidateRejectsMalformedAliasFamily(t *testing.T) {
	for _, key := range []string{"claude-prov", "claude--model", "claude-p--m--x"} {
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

func TestValidateIntegrationKeys(t *testing.T) {
	for id := range map[string]bool{"codex": true, "grok": true, "omp": true, "claude": true, "pi": true, "opencode": true, "hermes": true} {
		d := validDoc()
		d.Integrations = map[string]IntegrationSettings{id: {Enabled: true}}
		if err := d.validate(); err != nil {
			t.Fatalf("integration %s rejected: %v", id, err)
		}
	}
	d := validDoc()
	d.Integrations = map[string]IntegrationSettings{"codexx": {Enabled: true}}
	if err := d.validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unknown integration id: want ErrInvalidValue, got %v", err)
	}
	d = validDoc()
	d.Integrations = map[string]IntegrationSettings{"": {Enabled: true}}
	if err := d.validate(); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("empty integration id: want ErrEmptyField, got %v", err)
	}
}

func TestIntegrationSectionOmittedWhenEmpty(t *testing.T) {
	d := validDoc()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"integrations"`) {
		t.Fatalf("empty integrations section serialized: %s", b)
	}
	d.Integrations = map[string]IntegrationSettings{"codex": {Enabled: false}}
	b, err = json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"integrations"`) {
		t.Fatalf("populated integrations section omitted: %s", b)
	}
	if !strings.Contains(string(b), `"enabled":false`) {
		t.Fatalf("toggle-off integration lost its enabled:false key: %s", b)
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

func TestResolveContextWindowFallbackChain(t *testing.T) {
	doc := Document{
		Providers: map[string]Provider{
			"codex": {
				Models:        []string{"gpt-5.2", "gpt-5.2-codex"},
				ModelSettings: map[string]ModelSettings{"gpt-5.2": {ContextWindow: 200000}},
			},
			"ag": {Models: []string{"gemini-3-pro"}},
		},
	}
	cases := []struct {
		provider, model string
		want            int
	}{
		{"codex", "gpt-5.2", 200000},
		{"codex", "gpt-5.2-codex", DefaultContextWindow},
		{"ag", "gemini-3-pro", DefaultContextWindow},
	}
	for _, c := range cases {
		if got := doc.ResolveContextWindow(c.provider, c.model); got != c.want {
			t.Fatalf("ResolveContextWindow(%s, %s) = %d, want %d", c.provider, c.model, got, c.want)
		}
	}
	doc.ContextWindow = 400000
	if got := doc.ResolveContextWindow("ag", "gemini-3-pro"); got != 400000 {
		t.Fatalf("global fallback = %d, want 400000", got)
	}
}

func TestResolveContextWindowUsesDiscoveredBeforeGlobal(t *testing.T) {
	window := 128000
	bad := 0
	doc := Document{
		ContextWindow: 400000,
		Providers: map[string]Provider{
			"codex": {
				Models: []string{"over", "listed", "bad", "plain"},
				ModelSettings: map[string]ModelSettings{
					"over": {ContextWindow: 200000},
				},
				Discovered: map[string]DiscoveredFacts{
					"over":   {ContextWindow: &window},
					"listed": {ContextWindow: &window},
					"bad":    {ContextWindow: &bad},
				},
			},
		},
	}
	if got := doc.ResolveContextWindow("codex", "over"); got != 200000 {
		t.Fatalf("override = %d, want 200000", got)
	}
	if got := doc.ResolveContextWindow("codex", "listed"); got != 128000 {
		t.Fatalf("discovered = %d, want 128000", got)
	}
	if got := doc.ResolveContextWindow("codex", "bad"); got != 400000 {
		t.Fatalf("non-positive discovered = %d, want global 400000", got)
	}
	if got := doc.ResolveContextWindow("codex", "plain"); got != 400000 {
		t.Fatalf("missing discovered = %d, want global 400000", got)
	}
	doc.ContextWindow = 0
	if got := doc.ResolveContextWindow("codex", "plain"); got != DefaultContextWindow {
		t.Fatalf("empty chain = %d, want %d", got, DefaultContextWindow)
	}
}

func TestResolveImageInputPrecedence(t *testing.T) {
	on := true
	off := false
	doc := Document{Providers: map[string]Provider{
		"p": {
			Models: []string{"listed", "forced-off", "override-on", "missing", "old-on", "old-off"},
			Discovered: map[string]DiscoveredFacts{
				"listed":      {Image: &on},
				"forced-off":  {Image: &on},
				"override-on": {Image: &off},
			},
			ModelSettings: map[string]ModelSettings{
				"forced-off":  {ImageInput: &off},
				"override-on": {ImageInput: &on},
				"old-on":      {ImageInput: &on},
			},
		},
	}}
	cases := []struct {
		model string
		want  bool
	}{
		{"listed", true},
		{"forced-off", false},
		{"override-on", true},
		{"missing", false},
		{"old-on", true},
		{"old-off", false},
	}
	for _, c := range cases {
		if got := doc.ResolveImageInput("p", c.model); got != c.want {
			t.Fatalf("ResolveImageInput(%s) = %v, want %v", c.model, got, c.want)
		}
	}
}

type staticCatalog map[string]catalog.Facts

func (c staticCatalog) Lookup(id string) catalog.Facts {
	if c == nil {
		return catalog.Facts{}
	}
	return c[id]
}

func TestNilCatalogPreservesResolverChain(t *testing.T) {
	window := 128000
	on := true
	doc := Document{
		ContextWindow: 400000,
		Providers: map[string]Provider{
			"p": {
				Discovered: map[string]DiscoveredFacts{
					"listed": {ContextWindow: &window, Image: &on},
				},
			},
		},
	}
	if got := doc.ResolveContextWindow("p", "listed"); got != 128000 {
		t.Fatalf("listed window = %d", got)
	}
	if got := doc.ResolveContextWindow("p", "plain"); got != 400000 {
		t.Fatalf("global window = %d", got)
	}
	if !doc.ResolveImageInput("p", "listed") || doc.ResolveImageInput("p", "plain") {
		t.Fatal("nil catalog changed image resolution")
	}
}

func TestCatalogFillsUnknownAndLosesToListing(t *testing.T) {
	listedWindow := 128000
	off := false
	on := true
	doc := Document{
		ContextWindow: 400000,
		Providers: map[string]Provider{
			"p": {
				Models: []string{"manual", "listed", "text", "bare"},
				ModelSettings: map[string]ModelSettings{
					"manual": {ContextWindow: 200000, ImageInput: &off},
				},
				Discovered: map[string]DiscoveredFacts{
					"listed": {ContextWindow: &listedWindow},
					"text":   {Image: &off},
				},
			},
		},
	}
	doc.setCatalog(staticCatalog{
		"manual": {ContextWindow: 500000, Image: &on},
		"listed": {ContextWindow: 500000, Image: &on},
		"text":   {ContextWindow: 500000, Image: &on},
		"bare":   {ContextWindow: 500000, Image: &on},
	})
	if got := doc.ResolveContextWindow("p", "manual"); got != 200000 {
		t.Fatalf("manual window = %d, want 200000", got)
	}
	if got := doc.ResolveContextWindow("p", "listed"); got != 128000 {
		t.Fatalf("listing window = %d, want 128000", got)
	}
	if got := doc.ResolveContextWindow("p", "bare"); got != 500000 {
		t.Fatalf("catalog window = %d, want 500000", got)
	}
	if doc.ResolveImageInput("p", "manual") {
		t.Fatal("manual image false lost to catalog")
	}
	if doc.ResolveImageInput("p", "text") {
		t.Fatal("listing image false lost to catalog")
	}
	if !doc.ResolveImageInput("p", "bare") {
		t.Fatal("catalog image true did not fill a silent model")
	}
	src := doc.ResolveContextSource("p", "bare")
	if src.Window != 500000 || src.Source != "catalog" {
		t.Fatalf("context source = %+v", src)
	}
	img := doc.ResolveImageSource("p", "text")
	if img.Image || img.Source != "listing" {
		t.Fatalf("image source = %+v", img)
	}
	global := doc.ResolveContextSource("p", "missing")
	if global.Window != 400000 || global.Source != "global" {
		t.Fatalf("global source = %+v", global)
	}
}

func TestDiscoveredFactsDropsNonPositiveWindow(t *testing.T) {
	var facts DiscoveredFacts
	if err := json.Unmarshal([]byte(`{"contextWindow":0,"image":false}`), &facts); err != nil {
		t.Fatal(err)
	}
	if facts.ContextWindow != nil {
		t.Fatalf("zero window stored: %+v", facts)
	}
	if facts.Image == nil || *facts.Image {
		t.Fatalf("image false not stored: %+v", facts)
	}
	if err := json.Unmarshal([]byte(`{"contextWindow":-3}`), &facts); err != nil {
		t.Fatal(err)
	}
	if facts.ContextWindow != nil || facts.Image != nil {
		t.Fatalf("negative window or stale image stored: %+v", facts)
	}
	if err := json.Unmarshal([]byte(`{"contextWindow":128000,"image":true}`), &facts); err != nil {
		t.Fatal(err)
	}
	if facts.ContextWindow == nil || *facts.ContextWindow != 128000 || facts.Image == nil || !*facts.Image {
		t.Fatalf("positive facts = %+v", facts)
	}
}

func TestModelSettingsImageInputOmittedIsNil(t *testing.T) {
	var s ModelSettings
	if err := json.Unmarshal([]byte(`{"contextWindow":100}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.ImageInput != nil {
		t.Fatal("omitted imageInput decoded as an override")
	}
	if err := json.Unmarshal([]byte(`{"imageInput":true}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.ImageInput == nil || !*s.ImageInput {
		t.Fatalf("imageInput true = %v", s.ImageInput)
	}
	if err := json.Unmarshal([]byte(`{"imageInput":false}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.ImageInput == nil || *s.ImageInput {
		t.Fatalf("imageInput false = %v", s.ImageInput)
	}
}

func TestValidateRejectsBadContextWindows(t *testing.T) {
	base := func() Document {
		return Document{
			Version:   SchemaVersion,
			Providers: map[string]Provider{"codex": {Wire: WireCodex, Models: []string{"gpt-5.2"}}},
		}
	}
	doc := base()
	p := doc.Providers["codex"]
	p.ModelSettings = map[string]ModelSettings{"gpt-5.2": {ContextWindow: -1}}
	doc.Providers["codex"] = p
	if err := doc.validate(); err == nil {
		t.Fatal("negative model contextWindow accepted")
	}
	doc = base()
	p = doc.Providers["codex"]
	p.ModelSettings = map[string]ModelSettings{"nope": {ContextWindow: 100}}
	doc.Providers["codex"] = p
	if err := doc.validate(); err == nil {
		t.Fatal("model contextWindow for unknown model accepted")
	}
	doc = base()
	doc.ContextWindow = -5
	if err := doc.validate(); err == nil {
		t.Fatal("negative global contextWindow accepted")
	}
	doc = base()
	p = doc.Providers["codex"]
	p.ModelSettings = map[string]ModelSettings{"gpt-5.2": {ReasoningEfforts: []string{"turbo"}}}
	doc.Providers["codex"] = p
	if err := doc.validate(); err == nil {
		t.Fatal("invalid reasoning effort accepted")
	}
	doc = base()
	p = doc.Providers["codex"]
	p.ModelSettings = map[string]ModelSettings{"gpt-5.2": {ContextWindow: 96000, ReasoningEfforts: []string{"high"}}}
	doc.Providers["codex"] = p
	if err := doc.validate(); err != nil {
		t.Fatalf("valid model override rejected: %v", err)
	}
	doc = base()
	p = doc.Providers["codex"]
	p.ModelSettings = map[string]ModelSettings{"gpt-5.2": {ReasoningEfforts: []string{"max"}}}
	doc.Providers["codex"] = p
	if err := doc.validate(); err != nil {
		t.Fatalf("max reasoning effort rejected: %v", err)
	}
}

func TestCloneDocumentKeepsVisionSidecar(t *testing.T) {
	m := &Manager{snap: Snapshot{Config: Document{
		Version:       SchemaVersion,
		VisionSidecar: VisionSidecarSettings{Enabled: true, Target: "router/gpt-5.6-luna"},
	}}}
	got := m.Get().Config.VisionSidecar
	if !got.Enabled || got.Target != "router/gpt-5.6-luna" {
		t.Fatalf("VisionSidecar = %+v, want enabled router target", got)
	}
}

func TestValidateVisionSidecarTarget(t *testing.T) {
	doc := func(mutate func(*Document)) Document {
		d := Document{
			Version: SchemaVersion,
			Providers: map[string]Provider{
				"p": {
					Wire:    WireOpenAIChat,
					BaseURL: "http://localhost",
					Models:  []string{"vision", "text"},
					ModelSettings: map[string]ModelSettings{
						"vision": {ImageInput: boolPtr(true)},
					},
				},
			},
			VisionSidecar: VisionSidecarSettings{Enabled: true, Target: "p/vision"},
		}
		mutate(&d)
		return d
	}
	cases := []struct {
		name   string
		mutate func(*Document)
		valid  bool
	}{
		{"valid", func(*Document) {}, true},
		{"disabled", func(d *Document) { d.VisionSidecar.Enabled = false }, true},
		{"empty target", func(d *Document) { d.VisionSidecar.Target = "" }, false},
		{"combo", func(d *Document) {
			d.Combos = map[string]Combo{"c": {Targets: []Target{{Provider: "p", Model: "vision"}}}}
			d.VisionSidecar.Target = "c"
		}, false},
		{"unknown provider", func(d *Document) { d.VisionSidecar.Target = "x/vision" }, false},
		{"unknown model", func(d *Document) { d.VisionSidecar.Target = "p/missing" }, false},
		{"disabled model", func(d *Document) {
			p := d.Providers["p"]
			p.DisabledModels = []string{"vision"}
			d.Providers["p"] = p
		}, false},
		{"disabled provider", func(d *Document) {
			off := false
			p := d.Providers["p"]
			p.Enabled = &off
			d.Providers["p"] = p
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := doc(tc.mutate).validate()
			if tc.valid && err != nil {
				t.Fatalf("validate: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("validate accepted invalid sidecar target")
			}
		})
	}
}
