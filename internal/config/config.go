package config

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"
)

const SchemaVersion = 1

type Wire string

const (
	WireCodex             Wire = "codex"
	WireAntigravity       Wire = "antigravity"
	WireOpenAIResponses   Wire = "responses"
	WireAnthropicMessages Wire = "messages"
	WireOpenAIChat        Wire = "chat"
)

func (w Wire) valid() bool {
	switch w {
	case WireCodex, WireAntigravity, WireOpenAIResponses, WireAnthropicMessages, WireOpenAIChat:
		return true
	}
	return false
}

type ComboStrategy string

const (
	ComboFailover   ComboStrategy = "failover"
	ComboRoundRobin ComboStrategy = "round_robin"
)

type PoolStrategy string

const (
	PoolQuota      PoolStrategy = "quota"
	PoolRoundRobin PoolStrategy = "round_robin"
	PoolFillFirst  PoolStrategy = "fill_first"
)

type PoolAffinity string

const (
	AffinitySticky PoolAffinity = "sticky"
	AffinityOff    PoolAffinity = "off"
)

var (
	ErrStaleGeneration      = errors.New("config: stale generation")
	ErrCorrupt              = errors.New("config: corrupt document")
	ErrUnknownWire          = errors.New("config: unknown wire")
	ErrUnknownComboStrategy = errors.New("config: unknown combo strategy")
	ErrUnknownPoolStrategy  = errors.New("config: unknown pool strategy")
	ErrUnknownAffinity      = errors.New("config: unknown affinity")
	ErrMalformedAlias       = errors.New("config: malformed alias")
	ErrInvalidTarget        = errors.New("config: invalid route target")
	ErrInvalidValue         = errors.New("config: invalid value")
	ErrEmptyField           = errors.New("config: empty field")
)

type Document struct {
	Version   int                 `json:"version"`
	Daemon    Daemon              `json:"daemon"`
	Providers map[string]Provider `json:"providers"`
	Combos    map[string]Combo    `json:"combos"`
	Routes    map[string]string   `json:"routes"`
	Aliases   map[string]string   `json:"aliases"`
}

type Daemon struct {
	Listen string `json:"listen"`
}

type Provider struct {
	Wire           Wire          `json:"wire"`
	BaseURL        string        `json:"baseURL,omitempty"`
	APIKeyRef      string        `json:"apiKeyRef,omitempty"`
	DefaultModel   string        `json:"defaultModel,omitempty"`
	Models         []string      `json:"models,omitempty"`
	DisabledModels []string      `json:"disabledModels,omitempty"`
	Enabled        *bool         `json:"enabled,omitempty"`
	Pool           *PoolSettings `json:"pool,omitempty"`
}

func (p Provider) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

type PoolSettings struct {
	Strategy            PoolStrategy  `json:"strategy"`
	AutoSwitch          *bool         `json:"autoSwitch,omitempty"`
	AutoSwitchThreshold float64       `json:"autoSwitchThreshold"`
	Affinity            PoolAffinity  `json:"affinity,omitempty"`
	PinnedAccount       string        `json:"pinnedAccount,omitempty"`
	AccountsPath        string        `json:"accountsPath"`
	MaxFailovers        int           `json:"maxFailovers"`
	CooldownDefault     time.Duration `json:"cooldownDefault"`
	CooldownMax         time.Duration `json:"cooldownMax"`
	ProbeEvery          time.Duration `json:"probeEvery"`
}

func (p PoolSettings) AutoSwitchEnabled() bool { return p.AutoSwitch == nil || *p.AutoSwitch }

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
	for id, p := range d.Providers {
		if id == "" {
			return fmt.Errorf("%w: provider id", ErrEmptyField)
		}
		if !p.Wire.valid() {
			return fmt.Errorf("%w: providers.%s.wire %q", ErrUnknownWire, id, p.Wire)
		}
		if (p.Wire == WireOpenAIResponses || p.Wire == WireOpenAIChat) && p.BaseURL == "" {
			return fmt.Errorf("%w: providers.%s.baseURL required for wire %q", ErrInvalidValue, id, p.Wire)
		}
		for _, m := range p.DisabledModels {
			if !contains(p.Models, m) {
				return fmt.Errorf("%w: providers.%s.disabledModels %q does not name a configured model", ErrInvalidValue, id, m)
			}
		}
		if p.Pool != nil {
			if err := p.Pool.validate(); err != nil {
				return fmt.Errorf("providers.%s.pool: %w", id, err)
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
	return nil
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
	case strings.HasPrefix(k, PrismAliasPrefix):
		if _, err := ParsePrismAlias(k); err != nil {
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
	switch p.Strategy {
	case PoolQuota, PoolRoundRobin, PoolFillFirst:
	default:
		return fmt.Errorf("%w: strategy %q", ErrUnknownPoolStrategy, p.Strategy)
	}
	if p.AutoSwitchThreshold < 0 || p.AutoSwitchThreshold > 1 {
		return fmt.Errorf("%w: autoSwitchThreshold %f", ErrInvalidValue, p.AutoSwitchThreshold)
	}
	switch p.Affinity {
	case "", AffinitySticky, AffinityOff:
	default:
		return fmt.Errorf("%w: affinity %q", ErrUnknownAffinity, p.Affinity)
	}
	if p.PinnedAccount != "" {
		if strings.ContainsFunc(p.PinnedAccount, unicode.IsSpace) {
			return fmt.Errorf("%w: pinnedAccount %q", ErrInvalidValue, p.PinnedAccount)
		}
	}
	if p.MaxFailovers < 0 {
		return fmt.Errorf("%w: maxFailovers %d", ErrInvalidValue, p.MaxFailovers)
	}
	if p.CooldownDefault < 0 || p.CooldownMax < 0 || p.ProbeEvery < 0 {
		return fmt.Errorf("%w: negative duration", ErrInvalidValue)
	}
	if p.CooldownMax > 0 && p.CooldownDefault > p.CooldownMax {
		return fmt.Errorf("%w: cooldownDefault exceeds cooldownMax", ErrInvalidValue)
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
