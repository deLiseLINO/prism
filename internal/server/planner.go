package server

import (
	"slices"
	"strings"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/config"
	ingressmessages "github.com/deLiseLINO/prism/internal/ingress/messages"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/routing"
)

type ConfigPlanner struct {
	get func() config.Document
}

func NewConfigPlanner(m *config.Manager) *ConfigPlanner {
	return &ConfigPlanner{get: func() config.Document { return m.Get().Config }}
}

func (p *ConfigPlanner) Plan(model canon.ModelID) (routing.Plan, bool) {
	d := p.get()
	key := string(model)
	if v, ok := d.Routes[key]; ok {
		return p.planFor(d, v)
	}
	if v, ok := d.Aliases[key]; ok {
		return p.planFor(d, v)
	}
	if c, ok := d.Combos[key]; ok {
		return comboPlan(d, c), true
	}
	if providerID, modelName, err := parseAliasKey(d, key); err == nil {
		if t, err := targetFor(d, providerID, modelName); err == nil {
			return routing.Plan{Targets: []provider.Target{t}}, true
		}
		return routing.Plan{}, false
	}
	providerID, modelName, ok := strings.Cut(key, "/")
	if !ok {
		return routing.Plan{}, false
	}
	t, err := targetFor(d, providerID, modelName)
	if err != nil {
		return routing.Plan{}, false
	}
	return routing.Plan{Targets: []provider.Target{t}}, true
}

// parseAliasKey splits a claude-<provider>--<model> alias. Provider and model
// ids may themselves contain "--", so the first separator is not always the
// right one: a split naming a configured model wins, and otherwise the first
// split is used.
func parseAliasKey(d config.Document, key string) (providerID, modelName string, err error) {
	providerID, modelName, err = ingressmessages.ParseModelAlias(key)
	if err != nil {
		return "", "", err
	}
	rest := strings.TrimPrefix(key, config.ClaudeAliasPrefix)
	for from := 0; ; {
		i := strings.Index(rest[from:], config.ClaudeSeparator)
		if i < 0 {
			break
		}
		cut := from + i
		from = cut + 1
		candidate, name := rest[:cut], rest[cut+len(config.ClaudeSeparator):]
		if candidate == "" || name == "" {
			continue
		}
		if p, ok := d.Providers[candidate]; ok && slices.Contains(p.Models, name) {
			return candidate, name, nil
		}
	}
	return providerID, modelName, nil
}

func (p *ConfigPlanner) planFor(d config.Document, v string) (routing.Plan, bool) {
	if c, ok := d.Combos[v]; ok {
		return comboPlan(d, c), true
	}
	providerID, modelName, ok := strings.Cut(v, "/")
	if !ok {
		return routing.Plan{}, false
	}
	t, err := targetFor(d, providerID, modelName)
	if err != nil {
		return routing.Plan{}, false
	}
	return routing.Plan{Targets: []provider.Target{t}}, true
}

func comboPlan(d config.Document, c config.Combo) routing.Plan {
	targets := make([]provider.Target, 0, len(c.Targets))
	for _, t := range c.Targets {
		if targetDisabled(d, t.Provider, t.Model) {
			continue
		}
		pt, err := targetFor(d, t.Provider, t.Model)
		if err != nil {
			return routing.Plan{}
		}
		targets = append(targets, pt)
	}
	return routing.Plan{Targets: targets}
}

func targetFor(d config.Document, providerID, model string) (provider.Target, error) {
	p, ok := d.Providers[providerID]
	if !ok {
		return provider.Target{}, config.ErrInvalidTarget
	}
	if !p.IsEnabled() || slices.Contains(p.DisabledModels, model) {
		return provider.Target{}, config.ErrInvalidTarget
	}
	return provider.Target{
		Provider:   account.ProviderID(providerID),
		Wire:       wireFor(d.ResolveWire(providerID, model)),
		BaseURL:    p.BaseURL,
		APIKeyRef:  p.APIKeyRef,
		Model:      canon.ModelID(model),
		ImageInput: d.ResolveImageInput(providerID, model),
		Policy:     selectionPolicy(p.Pool),
		Wait:       waitPolicy(p.Wait),
	}, nil
}

func waitPolicy(w *config.WaitSettings) provider.WaitPolicy {
	if w == nil {
		return provider.WaitPolicy{}
	}
	return provider.WaitPolicy{FirstProgress: waitBudget(w.FirstProgressMs), Idle: waitBudget(w.IdleMs)}
}

func waitBudget(ms *int64) time.Duration {
	switch {
	case ms == nil:
		return 0
	case *ms == 0:
		return provider.WaitOff
	default:
		return time.Duration(*ms) * time.Millisecond
	}
}

func selectionPolicy(ps *config.PoolSettings) account.SelectionPolicy {
	if ps == nil {
		return account.SelectionPolicy{}
	}
	return account.SelectionPolicy{PinnedAccount: account.AccountID(ps.PinnedAccount)}
}

func targetDisabled(d config.Document, providerID, model string) bool {
	p, ok := d.Providers[providerID]
	if !ok {
		return false
	}
	return !p.IsEnabled() || slices.Contains(p.DisabledModels, model)
}

func wireFor(w config.Wire) provider.Wire {
	switch w {
	case config.WireCodex:
		return provider.WireCodex
	case config.WireAntigravity:
		return provider.WireAntigravity
	case config.WireAnthropicMessages:
		return provider.WireMessages
	case config.WireOpenAIChat, config.WireCline:
		return provider.WireChat
	default:
		return provider.WireResponses
	}
}
