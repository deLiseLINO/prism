package server

import (
	"slices"
	"strings"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/config"
	ingressmessages "prism/internal/ingress/messages"
	"prism/internal/provider"
	"prism/internal/routing"
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
	if providerID, modelName, err := ingressmessages.ParseModelAlias(key); err == nil {
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
	t := provider.Target{
		Provider:   account.ProviderID(providerID),
		Wire:       wireFor(p.Wire),
		BaseURL:    p.BaseURL,
		APIKeyRef:  p.APIKeyRef,
		Model:      canon.ModelID(model),
		ImageInput: modelImageInput(p, model),
		Policy:     selectionPolicy(p.Pool),
	}
	if p.Pool != nil {
		t.MaxFailovers = p.Pool.MaxFailovers
	}
	return t, nil
}

func selectionPolicy(ps *config.PoolSettings) account.SelectionPolicy {
	if ps == nil {
		return account.SelectionPolicy{}
	}
	pol := account.SelectionPolicy{
		Strategy:            account.StrategyQuota,
		AutoSwitch:          account.AutoSwitchOn,
		AutoSwitchThreshold: ps.AutoSwitchThreshold,
		Affinity:            account.AffinitySticky,
		CooldownDefault:     ps.CooldownDefault,
		CooldownMax:         ps.CooldownMax,
	}
	switch ps.Strategy {
	case config.PoolRoundRobin:
		pol.Strategy = account.StrategyRoundRobin
	case config.PoolFillFirst:
		pol.Strategy = account.StrategyFillFirst
	}
	if !ps.AutoSwitchEnabled() {
		pol.AutoSwitch = account.AutoSwitchOff
	}
	if ps.Affinity == config.AffinityOff {
		pol.Affinity = account.AffinityOff
	}
	if ps.PinnedAccount != "" {
		pol.PinnedAccount = account.AccountID(ps.PinnedAccount)
	}
	return pol
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
	case config.WireOpenAIChat:
		return provider.WireChat
	default:
		return provider.WireResponses
	}
}

func modelImageInput(p config.Provider, model string) bool {
	if s, ok := p.ModelSettings[model]; ok && s.ImageInput {
		return true
	}
	return false
}
