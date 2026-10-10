package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/deLiseLINO/prism/internal/catalog"
	"github.com/deLiseLINO/prism/internal/integrations"
)

const SchemaVersion = 1

type Wire string

const (
	WireCodex             Wire = "codex"
	WireAntigravity       Wire = "antigravity"
	WireOpenAIResponses   Wire = "responses"
	WireAnthropicMessages Wire = "messages"
	WireOpenAIChat        Wire = "chat"
	WireCline             Wire = "cline"
)

func (w Wire) valid() bool {
	switch w {
	case WireCodex, WireAntigravity, WireOpenAIResponses, WireAnthropicMessages, WireOpenAIChat, WireCline:
		return true
	}
	return false
}

func (w Wire) Custom() bool {
	return w == WireOpenAIResponses || w == WireAnthropicMessages || w == WireOpenAIChat
}

type ComboStrategy string

const (
	ComboFailover   ComboStrategy = "failover"
	ComboRoundRobin ComboStrategy = "round_robin"
)

var (
	ErrStaleGeneration      = errors.New("config: stale generation")
	ErrCorrupt              = errors.New("config: corrupt document")
	ErrUnknownWire          = errors.New("config: unknown wire")
	ErrUnknownComboStrategy = errors.New("config: unknown combo strategy")
	ErrMalformedAlias       = errors.New("config: malformed alias")
	ErrInvalidTarget        = errors.New("config: invalid route target")
	ErrInvalidValue         = errors.New("config: invalid value")
	ErrEmptyField           = errors.New("config: empty field")
)

type Document struct {
	Version       int                            `json:"version"`
	Daemon        Daemon                         `json:"daemon"`
	ContextWindow int                            `json:"contextWindow,omitempty"`
	Providers     map[string]Provider            `json:"providers"`
	Combos        map[string]Combo               `json:"combos"`
	Routes        map[string]string              `json:"routes"`
	Aliases       map[string]string              `json:"aliases"`
	Hosts         map[string]Host                `json:"hosts,omitempty"`
	Integrations  map[string]IntegrationSettings `json:"integrations,omitempty"`
	VisionSidecar VisionSidecarSettings          `json:"visionSidecar,omitempty"`
	catalog       catalogLookup                  `json:"-"`
}

type IntegrationSettings struct {
	Enabled bool `json:"enabled"`
}

type Host struct {
	Address    string `json:"address"`
	DaemonPort int    `json:"daemonPort,omitempty"`
}

type Daemon struct {
	Listen string `json:"listen"`
}

type ModelMode string

const (
	// ModelModeLogical stores collapsed family ids; the antigravity runner
	// resolves the wire id per request. ModelModeRaw stores the raw wire ids
	// discovery reported; routing and the request envelope pass them through.
	ModelModeLogical ModelMode = "logical"
	ModelModeRaw     ModelMode = "raw"
)

func (m ModelMode) Valid() bool { return m == "" || m == ModelModeLogical || m == ModelModeRaw }

type ModelCatalog struct {
	BaseURL   string   `json:"baseURL"`
	Account   string   `json:"account"`
	Project   string   `json:"project"`
	RawModels []string `json:"rawModels"`
}

type Provider struct {
	Wire           Wire                       `json:"wire"`
	BaseURL        string                     `json:"baseURL,omitempty"`
	APIKeyRef      string                     `json:"apiKeyRef,omitempty"`
	DefaultModel   string                     `json:"defaultModel,omitempty"`
	ModelMode      ModelMode                  `json:"modelMode,omitempty"`
	ModelCatalogs  []ModelCatalog             `json:"modelCatalogs,omitempty"`
	Models         []string                   `json:"models,omitempty"`
	DisabledModels []string                   `json:"disabledModels,omitempty"`
	SyncedModels   []string                   `json:"syncedModels,omitempty"`
	Discovered     map[string]DiscoveredFacts `json:"discovered,omitempty"`
	ModelSettings  map[string]ModelSettings   `json:"modelSettings,omitempty"`
	Enabled        *bool                      `json:"enabled,omitempty"`
	Pool           *PoolSettings              `json:"pool,omitempty"`
	Wait           *WaitSettings              `json:"wait,omitempty"`
}

func (p Provider) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

const DefaultContextWindow = 256000

type ModelSettings struct {
	ContextWindow    int      `json:"contextWindow,omitempty"`
	ImageInput       *bool    `json:"imageInput,omitempty"`
	ReasoningEfforts []string `json:"reasoningEfforts,omitempty"`
	Wire             Wire     `json:"wire,omitempty"`
}

type DiscoveredFacts struct {
	ContextWindow *int  `json:"contextWindow,omitempty"`
	Image         *bool `json:"image,omitempty"`
}

func (d *DiscoveredFacts) UnmarshalJSON(b []byte) error {
	var raw struct {
		ContextWindow *int  `json:"contextWindow"`
		Image         *bool `json:"image"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	d.Image = raw.Image
	if raw.ContextWindow != nil && *raw.ContextWindow > 0 {
		d.ContextWindow = raw.ContextWindow
	}
	return nil
}

type catalogLookup interface {
	Lookup(modelID string) catalog.Facts
}

func (d *Document) setCatalog(c catalogLookup) {
	if d == nil {
		return
	}
	d.catalog = c
}

func (d Document) catalogFacts(model string) catalog.Facts {
	if d.catalog == nil {
		return catalog.Facts{}
	}
	return d.catalog.Lookup(model)
}

func (d Document) ResolveContextWindow(providerID, model string) int {
	n, _ := d.contextFallback(providerID, model, true)
	return n
}

func (d Document) ResolveMaxOutput(model string) int {
	return d.catalogFacts(model).MaxOutput
}

func (d Document) contextFallback(providerID, model string, includeManual bool) (int, string) {
	if p, ok := d.Providers[providerID]; ok {
		if includeManual {
			if s, ok := p.ModelSettings[model]; ok && s.ContextWindow > 0 {
				return s.ContextWindow, "manual"
			}
		}
		if facts, ok := p.Discovered[model]; ok && facts.ContextWindow != nil && *facts.ContextWindow > 0 {
			return *facts.ContextWindow, "listing"
		}
	}
	if n := d.catalogFacts(model).ContextWindow; n > 0 {
		return n, "catalog"
	}
	if d.ContextWindow > 0 {
		return d.ContextWindow, "global"
	}
	return DefaultContextWindow, "global"
}

func (d Document) ResolveImageInput(providerID, model string) bool {
	on, _ := d.imageResolution(providerID, model)
	return on
}

func (d Document) ResolveWire(providerID, model string) Wire {
	p := d.Providers[providerID]
	if s, ok := p.ModelSettings[model]; ok && s.Wire != "" {
		return s.Wire
	}
	return p.Wire
}

func (d Document) imageResolution(providerID, model string) (bool, string) {
	if p, ok := d.Providers[providerID]; ok {
		if s, ok := p.ModelSettings[model]; ok && s.ImageInput != nil {
			return *s.ImageInput, "manual"
		}
		if facts, ok := p.Discovered[model]; ok && facts.Image != nil {
			return *facts.Image, "listing"
		}
	}
	if img := d.catalogFacts(model).Image; img != nil && *img {
		return true, "catalog"
	}
	return false, "none"
}

type ContextSource struct {
	Window int
	Source string
}

type ImageSource struct {
	Image  bool
	Source string
}

func (d Document) ResolveContextSource(providerID, model string) ContextSource {
	n, src := d.contextFallback(providerID, model, false)
	return ContextSource{Window: n, Source: src}
}

func (d Document) ResolveImageSource(providerID, model string) ImageSource {
	on, src := d.imageResolution(providerID, model)
	return ImageSource{Image: on, Source: src}
}

type EffortsSource struct {
	Efforts []string
	Source  string
}

func (d Document) ResolveReasoningEfforts(providerID, model string) []string {
	return d.ResolveEffortsSource(providerID, model).Efforts
}

func (d Document) ResolveEffortsSource(providerID, model string) EffortsSource {
	if p, ok := d.Providers[providerID]; ok {
		if s, ok := p.ModelSettings[model]; ok && len(s.ReasoningEfforts) > 0 {
			return EffortsSource{Efforts: slices.Clone(s.ReasoningEfforts), Source: "manual"}
		}
	}
	if efforts := d.catalogFacts(model).Efforts; len(efforts) > 0 {
		return EffortsSource{Efforts: efforts, Source: "catalog"}
	}
	return EffortsSource{Source: "none"}
}

type PoolSettings struct {
	PinnedAccount string `json:"pinnedAccount,omitempty"`
	AccountsPath  string `json:"accountsPath,omitempty"`
}

// WaitSettings bounds how long a provider may stay silent during a streamed
// response. A nil field takes the runtime default, an explicit zero disables
// that budget, and a positive value is milliseconds.
type WaitSettings struct {
	FirstProgressMs *int64 `json:"firstProgressMs,omitempty"`
	IdleMs          *int64 `json:"idleMs,omitempty"`
}

const maxWaitMs = int64(math.MaxInt64 / int64(time.Millisecond))

func (w WaitSettings) IsZero() bool { return w.FirstProgressMs == nil && w.IdleMs == nil }

func (w WaitSettings) validate() error {
	if w.FirstProgressMs != nil && (*w.FirstProgressMs < 0 || *w.FirstProgressMs > maxWaitMs) {
		return fmt.Errorf("%w: firstProgressMs %d", ErrInvalidValue, *w.FirstProgressMs)
	}
	if w.IdleMs != nil && (*w.IdleMs < 0 || *w.IdleMs > maxWaitMs) {
		return fmt.Errorf("%w: idleMs %d", ErrInvalidValue, *w.IdleMs)
	}
	return nil
}

func (w *WaitSettings) clone() *WaitSettings {
	if w == nil {
		return nil
	}
	out := WaitSettings{}
	if w.FirstProgressMs != nil {
		v := *w.FirstProgressMs
		out.FirstProgressMs = &v
	}
	if w.IdleMs != nil {
		v := *w.IdleMs
		out.IdleMs = &v
	}
	return &out
}

type Target struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Weight   int    `json:"weight"`
}

type Combo struct {
	Targets     []Target      `json:"targets"`
	Strategy    ComboStrategy `json:"strategy"`
	StickyLimit int           `json:"stickyLimit"`
	Alias       string        `json:"alias,omitempty"`
	NativeAlias string        `json:"nativeAlias,omitempty"`
	DisplayName string        `json:"displayName,omitempty"`
	ImageInput  bool          `json:"imageInput,omitempty"`
}

func (d Document) validate() error {
	if d.Version != SchemaVersion {
		return fmt.Errorf("%w: version %d", ErrCorrupt, d.Version)
	}
	if d.Daemon.Listen != "" {
		if _, _, err := net.SplitHostPort(d.Daemon.Listen); err != nil {
			return fmt.Errorf("%w: daemon.listen %q", ErrInvalidValue, d.Daemon.Listen)
		}
	}
	if d.ContextWindow < 0 {
		return fmt.Errorf("%w: contextWindow %d", ErrInvalidValue, d.ContextWindow)
	}
	for id, p := range d.Providers {
		if id == "" {
			return fmt.Errorf("%w: provider id", ErrEmptyField)
		}
		if !p.ModelMode.Valid() {
			return fmt.Errorf("%w: providers.%s.modelMode %q", ErrInvalidValue, id, p.ModelMode)
		}
		if !p.Wire.valid() {
			return fmt.Errorf("%w: providers.%s.wire %q", ErrUnknownWire, id, p.Wire)
		}
		if needsBaseURL(p.Wire) && p.BaseURL == "" {
			return fmt.Errorf("%w: providers.%s.baseURL required for wire %q", ErrInvalidValue, id, p.Wire)
		}
		for _, m := range p.DisabledModels {
			if !contains(p.Models, m) {
				return fmt.Errorf("%w: providers.%s.disabledModels %q does not name a configured model", ErrInvalidValue, id, m)
			}
		}
		if err := p.validateModelSettings(id); err != nil {
			return err
		}
		if p.Pool != nil {
			if err := p.Pool.validate(); err != nil {
				return fmt.Errorf("providers.%s.pool: %w", id, err)
			}
		}
		if p.Wait != nil {
			if err := p.Wait.validate(); err != nil {
				return fmt.Errorf("providers.%s.wait: %w", id, err)
			}
		}
	}
	for id, c := range d.Combos {
		if id == "" {
			return fmt.Errorf("%w: combo id", ErrEmptyField)
		}
		switch c.Strategy {
		case ComboFailover, ComboRoundRobin:
		default:
			return fmt.Errorf("%w: combos.%s.strategy %q", ErrUnknownComboStrategy, id, c.Strategy)
		}
		if c.StickyLimit < 0 {
			return fmt.Errorf("%w: combos.%s.stickyLimit %d", ErrInvalidValue, id, c.StickyLimit)
		}
		if len(c.Targets) == 0 {
			return fmt.Errorf("%w: combos.%s.targets", ErrEmptyField, id)
		}
		for _, t := range c.Targets {
			if err := d.validateTarget(t.Provider, t.Model); err != nil {
				return fmt.Errorf("combos.%s.targets: %w", id, err)
			}
			if t.Weight < 0 {
				return fmt.Errorf("%w: combos.%s.targets.weight %d", ErrInvalidValue, id, t.Weight)
			}
		}
	}
	if d.VisionSidecar.Enabled {
		if err := d.validateVisionSidecar(); err != nil {
			return err
		}
	}
	for k, v := range d.Routes {
		if err := d.validateAliasKey(k); err != nil {
			return err
		}
		if err := d.validateRouteValue(v); err != nil {
			return fmt.Errorf("routes.%s: %w", k, err)
		}
	}
	for k, v := range d.Aliases {
		if err := d.validateAliasKey(k); err != nil {
			return err
		}
		if err := d.validateRouteValue(v); err != nil {
			return fmt.Errorf("aliases.%s: %w", k, err)
		}
	}
	for id, h := range d.Hosts {
		if id == "" {
			return fmt.Errorf("%w: host id", ErrEmptyField)
		}
		if strings.TrimSpace(h.Address) == "" {
			return fmt.Errorf("%w: hosts.%s.address", ErrEmptyField, id)
		}
		if h.DaemonPort < 0 || h.DaemonPort > 65535 {
			return fmt.Errorf("%w: hosts.%s.daemonPort", ErrInvalidValue, id)
		}
	}
	for id := range d.Integrations {
		if id == "" {
			return fmt.Errorf("%w: integration id", ErrEmptyField)
		}
		if _, ok := integrations.ValidID(id); !ok {
			return fmt.Errorf("%w: integrations.%s", ErrInvalidValue, id)
		}
	}
	return nil
}

func (p Provider) validateModelSettings(providerID string) error {
	for model, s := range p.ModelSettings {
		if model == "" {
			return fmt.Errorf("%w: providers.%s.modelSettings model key", ErrEmptyField, providerID)
		}
		if len(p.Models) > 0 && !slices.Contains(p.Models, model) {
			return fmt.Errorf("%w: providers.%s.modelSettings[%s] does not name a configured model", ErrInvalidValue, providerID, model)
		}

		if s.ContextWindow < 0 {
			return fmt.Errorf("%w: providers.%s.modelSettings[%s].contextWindow", ErrInvalidValue, providerID, model)
		}

		for _, e := range s.ReasoningEfforts {
			if !validReasoningEffort(e) {
				return fmt.Errorf("%w: providers.%s.modelSettings[%s].reasoningEfforts %q", ErrInvalidValue, providerID, model, e)
			}
		}

		if s.Wire != "" {
			if !s.Wire.Custom() {
				return fmt.Errorf("%w: providers.%s.modelSettings[%s].wire %q", ErrUnknownWire, providerID, model, s.Wire)
			}
			if !p.Wire.Custom() {
				return fmt.Errorf("%w: providers.%s.modelSettings[%s].wire not allowed for provider wire %q", ErrInvalidValue, providerID, model, p.Wire)
			}
			if needsBaseURL(s.Wire) && p.BaseURL == "" {
				return fmt.Errorf("%w: providers.%s.baseURL required for modelSettings[%s].wire %q", ErrInvalidValue, providerID, model, s.Wire)
			}
		}
	}
	return nil
}

func needsBaseURL(w Wire) bool {
	return w == WireOpenAIResponses || w == WireOpenAIChat
}

func validReasoningEffort(e string) bool {
	switch e {
	case "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

func (d Document) validateAliasKey(k string) error {
	if k == "" {
		return fmt.Errorf("%w: alias key", ErrEmptyField)
	}
	switch {
	case strings.HasPrefix(k, ClaudeAliasPrefix):
		if _, _, err := ParseClaudeAlias(k); err != nil {
			return fmt.Errorf("%w: %q", ErrMalformedAlias, k)
		}
	}
	return nil
}

func (d Document) validateRouteValue(v string) error {
	if v == "" {
		return fmt.Errorf("%w: empty target", ErrInvalidTarget)
	}
	if _, ok := d.Combos[v]; ok {
		return nil
	}
	provider, model, ok := strings.Cut(v, "/")
	if !ok || provider == "" || model == "" {
		return fmt.Errorf("%w: %q", ErrInvalidTarget, v)
	}
	p, ok := d.Providers[provider]
	if !ok {
		return fmt.Errorf("%w: unknown provider %q in %q", ErrInvalidTarget, provider, v)
	}
	if len(p.Models) > 0 && !contains(p.Models, model) {
		return fmt.Errorf("%w: unknown model %q for provider %q", ErrInvalidTarget, model, provider)
	}
	return nil
}

func (d Document) validateTarget(provider, model string) error {
	if provider == "" || model == "" {
		return fmt.Errorf("%w: target %q/%q", ErrEmptyField, provider, model)
	}
	p, ok := d.Providers[provider]
	if !ok {
		return fmt.Errorf("%w: unknown provider %q", ErrInvalidTarget, provider)
	}
	if len(p.Models) > 0 && !contains(p.Models, model) {
		return fmt.Errorf("%w: unknown model %q for provider %q", ErrInvalidTarget, model, provider)
	}
	return nil
}

func (p PoolSettings) validate() error {
	if p.PinnedAccount != "" && strings.ContainsFunc(p.PinnedAccount, unicode.IsSpace) {
		return fmt.Errorf("%w: pinnedAccount %q", ErrInvalidValue, p.PinnedAccount)
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

type VisionSidecarSettings struct {
	Enabled bool   `json:"enabled,omitempty"`
	Target  string `json:"target,omitempty"`
}

func (d Document) validateVisionSidecar() error {
	v := d.VisionSidecar.Target
	if v == "" {
		return fmt.Errorf("%w: visionSidecar.target", ErrEmptyField)
	}
	if _, ok := d.Combos[v]; ok {
		return fmt.Errorf("%w: visionSidecar.target %q must name a provider/model", ErrInvalidTarget, v)
	}
	providerID, model, ok := strings.Cut(v, "/")
	if !ok || providerID == "" || model == "" {
		return fmt.Errorf("%w: visionSidecar.target %q", ErrInvalidTarget, v)
	}
	p, ok := d.Providers[providerID]
	if !ok {
		return fmt.Errorf("%w: visionSidecar.target unknown provider %q", ErrInvalidTarget, providerID)
	}
	if !p.IsEnabled() {
		return fmt.Errorf("%w: visionSidecar.target provider %q is disabled", ErrInvalidTarget, providerID)
	}
	if len(p.Models) > 0 && !contains(p.Models, model) {
		return fmt.Errorf("%w: visionSidecar.target unknown model %q for provider %q", ErrInvalidTarget, model, providerID)
	}
	if contains(p.DisabledModels, model) {
		return fmt.Errorf("%w: visionSidecar.target model %q is disabled", ErrInvalidTarget, model)
	}
	if !d.ResolveImageInput(providerID, model) {
		return fmt.Errorf("%w: visionSidecar.target model %q requires imageInput", ErrInvalidTarget, model)
	}
	return nil
}
