package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"prism/internal/account"
	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/provider"
	"prism/internal/providers/anthropic"
	"prism/internal/providers/antigravity"
	"prism/internal/providers/codex"
	"prism/internal/providers/customchat"
	"prism/internal/providers/customresponses"
)

const (
	reconcileInterval  = 10 * time.Second
	discoverRetryAfter = 5 * time.Minute
	discoverTimeout    = 15 * time.Second
	discoverModelCap   = 200
)

type daemonEnv struct {
	credentialPath string
	cfg            *config.Manager
	pool           auth.PoolRegistrar
	quotas         *quotaTable
	registry       *provider.Registry
	creds          credentialStore
	client         *http.Client
	repos          map[account.ProviderID]*account.Repository
	flows          map[account.ProviderID]auth.Flow
	refresher      *auth.Refresher
	wires          map[account.ProviderID]config.Wire
	lastDiscover   map[account.ProviderID]time.Time
	now            func() time.Time
}

func newDaemonEnv(credentialPath string, cfg *config.Manager, pool auth.PoolRegistrar, quotas *quotaTable, registry *provider.Registry, creds credentialStore, client *http.Client) *daemonEnv {
	return &daemonEnv{
		credentialPath: credentialPath,
		cfg:            cfg,
		pool:           pool,
		quotas:         quotas,
		registry:       registry,
		creds:          creds,
		client:         client,
		repos:          map[account.ProviderID]*account.Repository{},
		flows:          map[account.ProviderID]auth.Flow{},
		wires:          map[account.ProviderID]config.Wire{},
		lastDiscover:   map[account.ProviderID]time.Time{},
		now:            time.Now,
	}
}

func customWire(w config.Wire) bool {
	return w == config.WireOpenAIResponses || w == config.WireAnthropicMessages || w == config.WireOpenAIChat
}

func (e *daemonEnv) ensureProvider(ctx context.Context, id string, p config.Provider) error {
	providerID := account.ProviderID(id)
	if _, ok := e.registry.Lookup(providerID); ok {
		return nil
	}
	repo, ok := e.repos[providerID]
	if !ok {
		repoPath := ""
		if p.Pool != nil && p.Pool.AccountsPath != "" {
			repoPath = p.Pool.AccountsPath
		} else {
			repoPath = filepath.Join(e.credentialPath, "accounts", id+".json")
		}
		repo = account.OpenMeta(repoPath)
		e.repos[providerID] = repo
	}
	accounts, err := repo.Load(providerID)
	if err != nil {
		return fmt.Errorf("prismd: provider %s: %w", id, err)
	}
	if len(accounts) == 0 && customWire(p.Wire) {
		defaultID := account.AccountID(id + ":default")
		e.pool.Register(account.Account{ID: defaultID, Provider: providerID, State: account.Active, CredGen: 1, Version: 1})
	}
	for _, a := range accounts {
		e.pool.Register(a)
	}
	e.ensureFlows(providerID, p.Wire)
	runner, err := e.buildRunner(id, p)
	if err != nil {
		return err
	}
	if err := e.registry.Register(providerID, runner); err != nil {
		return err
	}
	e.wires[providerID] = p.Wire
	return nil
}

func (e *daemonEnv) ensureFlows(providerID account.ProviderID, w config.Wire) {
	switch w {
	case config.WireCodex:
		flow, err := auth.NewCodexFlow(auth.CodexProduction, auth.Options{})
		if err != nil {
			log.Printf("prismd: provider %s auth: %v", providerID, err)
			return
		}
		e.flows[providerID] = flow
	case config.WireAntigravity:
		flow, err := auth.NewAntigravityFlow(auth.AntigravityProduction, auth.Options{})
		if err != nil {
			log.Printf("prismd: provider %s auth: %v", providerID, err)
			return
		}
		e.flows[providerID] = flow
	default:
		delete(e.flows, providerID)
	}
}

func (e *daemonEnv) buildRunner(id string, p config.Provider) (provider.Runner, error) {
	providerID := account.ProviderID(id)
	switch p.Wire {
	case config.WireCodex:
		return &codex.Runner{
			Creds:       codexCreds{ref: e.refresher},
			Client:      e.client,
			Now:         time.Now,
			QuotaSink:   e.quotas.record,
			WarningSink: func(w string) { log.Printf("prismd: codex %s warning: %s", id, w) },
		}, nil
	case config.WireAntigravity:
		return antigravity.NewRunner(antigravityCreds{ref: e.refresher}, e.client, p.BaseURL)
	case config.WireOpenAIResponses:
		return customresponses.New(customKey{e.creds}.Resolve, customresponses.Options{}), nil
	case config.WireOpenAIChat:
		return customchat.New(customKey{e.creds}.Resolve, customchat.Options{}), nil
	case config.WireAnthropicMessages:
		return anthropicRunner{runner: anthropic.New(anthropic.Options{BaseURL: p.BaseURL, HTTP: e.client}), creds: e.creds, provider: providerID}, nil
	default:
		return nil, fmt.Errorf("prismd: provider %q has unknown wire %q", id, p.Wire)
	}
}

func (e *daemonEnv) reconcileOnce(ctx context.Context) {
	snap := e.cfg.Get()
	for _, id := range slices.Sorted(maps.Keys(snap.Config.Providers)) {
		p := snap.Config.Providers[id]
		providerID := account.ProviderID(id)
		if _, ok := e.registry.Lookup(providerID); !ok {
			if err := e.ensureProvider(ctx, id, p); err != nil {
				log.Printf("prismd: reconcile provider %s: %v", id, err)
				continue
			}
			log.Printf("prismd: provider %s applied without restart", id)
		}
		if known, ok := e.wires[providerID]; ok && known != p.Wire {
			runner, err := e.buildRunner(id, p)
			if err != nil {
				log.Printf("prismd: provider %s wire %q: %v", id, p.Wire, err)
				continue
			}
			if err := e.registry.Replace(providerID, runner); err != nil {
				log.Printf("prismd: provider %s wire swap: %v", id, err)
				continue
			}
			e.ensureFlows(providerID, p.Wire)
			e.wires[providerID] = p.Wire
			log.Printf("prismd: provider %s wire %q applied without restart", id, p.Wire)
		}
		if customWire(p.Wire) && p.BaseURL != "" {
			e.syncCustomProvider(ctx, id, p)
		}
	}
}

func (e *daemonEnv) syncCustomProvider(ctx context.Context, id string, p config.Provider) {
	pid := account.ProviderID(id)
	snap := e.cfg.Get()
	current, ok := snap.Config.Providers[id]
	if !ok {
		return
	}
	doc := snap.Config
	if current.APIKeyRef == "" && e.storedKey(ctx, pid) != "" {
		current.APIKeyRef = id + ":default"
		doc.Providers[id] = current
	}
	if len(current.Models) == 0 {
		if since := e.now().Sub(e.lastDiscover[pid]); since < discoverRetryAfter {
			e.saveDoc(id, p, doc, snap.Generation)
			return
		}
		e.lastDiscover[pid] = e.now()
		models, err := fetchModels(ctx, e.client, current.BaseURL, e.storedKey(ctx, pid))
		if err != nil {
			log.Printf("prismd: discover models for %s: %v", id, err)
			e.saveDoc(id, p, doc, snap.Generation)
			return
		}
		current.Models = models
		doc.Providers[id] = current
		log.Printf("prismd: provider %s models discovered (%d)", id, len(models))
	}
	e.saveDoc(id, p, doc, snap.Generation)
}

func (e *daemonEnv) saveDoc(id string, prev config.Provider, doc config.Document, generation uint64) {
	current := doc.Providers[id]
	if current.APIKeyRef == prev.APIKeyRef && len(current.Models) == len(prev.Models) {
		return
	}
	if _, err := e.cfg.Update(doc, generation); err != nil {
		if !errors.Is(err, config.ErrStaleGeneration) {
			log.Printf("prismd: provider %s sync: %v", id, err)
		}
	}
}

func (e *daemonEnv) storedKey(ctx context.Context, providerID account.ProviderID) string {
	b, err := e.creds.blob(ctx, providerID, account.AccountID(string(providerID)+":default"), 1)
	if err != nil {
		return ""
	}
	return string(b)
}

func fetchModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	url := strings.TrimRight(baseURL, "/") + "/models"
	reqCtx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models endpoint returned %s", resp.Status)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m.ID)
		if len(out) >= discoverModelCap {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("models endpoint returned no models")
	}
	return out, nil
}

func (e *daemonEnv) loop(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.reconcileOnce(ctx)
		}
	}
}
