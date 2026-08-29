package server

import (
	"strings"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/config"
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
	t := provider.Target{
		Provider:  account.ProviderID(providerID),
		Wire:      wireFor(p.Wire),
		BaseURL:   p.BaseURL,
		APIKeyRef: p.APIKeyRef,
		Model:     canon.ModelID(model),
	}
	if p.Pool != nil {
		t.MaxFailovers = p.Pool.MaxFailovers
	}
	return t, nil
}

func wireFor(w config.Wire) provider.Wire {
	switch w {
	case config.WireCodex:
		return provider.WireCodex
	case config.WireAntigravity:
		return provider.WireAntigravity
	case config.WireAnthropicMessages:
		return provider.WireMessages
	default:
		return provider.WireResponses
	}
}
