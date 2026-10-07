package daemon

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
	"sync"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/auth"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/integrations"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/anthropic"
	"github.com/deLiseLINO/prism/internal/providers/antigravity"
	"github.com/deLiseLINO/prism/internal/providers/cline"
	"github.com/deLiseLINO/prism/internal/providers/codex"
	"github.com/deLiseLINO/prism/internal/providers/customchat"
	"github.com/deLiseLINO/prism/internal/providers/customresponses"
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
	streamClient   *http.Client
	repos          map[account.ProviderID]*account.Repository
	reposMu        sync.Mutex
	flows          map[account.ProviderID]auth.Flow
	refresher      *auth.Refresher
	wires          map[account.ProviderID]config.Wire
	lastDiscover   map[account.ProviderID]time.Time
	codex          *integrations.CodexIntegration
	now            func() time.Time
}

func newDaemonEnv(credentialPath string, cfg *config.Manager, pool auth.PoolRegistrar, quotas *quotaTable, registry *provider.Registry, creds credentialStore, client, streamClient *http.Client) *daemonEnv {
	return &daemonEnv{
		credentialPath: credentialPath,
		cfg:            cfg,
		pool:           pool,
		quotas:         quotas,
		registry:       registry,
		creds:          creds,
		client:         client,
		streamClient:   streamClient,
		repos:          map[account.ProviderID]*account.Repository{},
		flows:          map[account.ProviderID]auth.Flow{},
		wires:          map[account.ProviderID]config.Wire{},
		lastDiscover:   map[account.ProviderID]time.Time{},
		now:            time.Now,
	}
}

func (e *daemonEnv) ensureProvider(ctx context.Context, id string, p config.Provider) error {
	providerID := account.ProviderID(id)
	if _, ok := e.registry.Lookup(providerID); ok {
		return nil
	}
	repo := e.ensureRepo(providerID, p.Pool)
	accounts, err := repo.Load(providerID)
	if err != nil {
		return fmt.Errorf("prism: provider %s: %w", id, err)
	}
	if p.Wire.Custom() {
		defaultID := account.AccountID(id + ":default")
		gen, err := e.creds.file.CustomDefaultGeneration(ctx, providerID, p.APIKeyRef)
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			e.pool.Register(account.Account{ID: defaultID, Provider: providerID, State: account.Active, CredGen: gen, Version: 1})
		} else {
			for _, a := range accounts {
				if a.ID == defaultID {
					a.CredGen = gen
				}
				e.pool.Register(a)
			}
		}
	} else {
		for _, a := range accounts {
			e.pool.Register(a)
		}
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
	if e.creds.publication != nil && p.Wire.Custom() {
		e.creds.publication.mu.Lock()
		e.creds.publication.refs[id] = p.APIKeyRef
		e.creds.publication.mu.Unlock()
	}
	return nil
}

func (e *daemonEnv) ensureRepo(providerID account.ProviderID, pool *config.PoolSettings) *account.Repository {
	e.reposMu.Lock()
	defer e.reposMu.Unlock()
	if repo, ok := e.repos[providerID]; ok {
		return repo
	}
	repoPath := ""
	if pool != nil && pool.AccountsPath != "" {
		repoPath = pool.AccountsPath
	} else {
		repoPath = filepath.Join(e.credentialPath, "accounts", string(providerID)+".json")
	}
	repo := account.OpenMeta(repoPath)
	e.repos[providerID] = repo
	return repo
}

func (e *daemonEnv) noteStoredAccount(providerID account.ProviderID) {
	w, ok := loginWire(providerID)
	if !ok {
		return
	}
	e.ensureLoginProvider(string(providerID), w)
	snap := e.cfg.Get()
	p, ok := snap.Config.Providers[string(providerID)]
	if !ok {
		return
	}
	if err := e.ensureProvider(context.Background(), string(providerID), p); err != nil {
		log.Printf("prism: login provider %s: %v", providerID, err)
	}
}

func loginWire(providerID account.ProviderID) (config.Wire, bool) {
	switch providerID {
	case "codex":
		return config.WireCodex, true
	case "antigravity":
		return config.WireAntigravity, true
	case "cline":
		return config.WireCline, true
	default:
		return "", false
	}
}

func (e *daemonEnv) ensureLoginProvider(id string, w config.Wire) {
	snap := e.cfg.Get()
	if _, ok := snap.Config.Providers[id]; ok {
		return
	}
	doc := snap.Config
	if doc.Providers == nil {
		doc.Providers = map[string]config.Provider{}
	}
	card := config.Provider{Wire: w}
	if w == config.WireCline {
		card.BaseURL = cline.DefaultBaseURL
	}
	doc.Providers[id] = card
	if _, err := e.cfg.Update(doc, snap.Generation); err != nil && !errors.Is(err, config.ErrStaleGeneration) {
		log.Printf("prism: login provider %s: %v", id, err)
	}
}

func (e *daemonEnv) ensureFlows(providerID account.ProviderID, w config.Wire) {
	switch w {
	case config.WireCodex:
		e.ensureRepo(providerID, nil)
		flow, err := auth.NewCodexFlow(auth.CodexProduction, auth.Options{})
		if err != nil {
			log.Printf("prism: provider %s auth: %v", providerID, err)
			return
		}
		e.flows[providerID] = flow
	case config.WireAntigravity:
		e.ensureRepo(providerID, nil)
		flow, err := auth.NewAntigravityFlow(auth.AntigravityProduction, auth.Options{})
		if err != nil {
			log.Printf("prism: provider %s auth: %v", providerID, err)
			return
		}
		e.flows[providerID] = flow
	case config.WireCline:
		e.ensureRepo(providerID, nil)
		e.flows[providerID] = auth.NewClineFlow(auth.Options{})
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
			Client:      e.streamClient,
			Now:         time.Now,
			QuotaSink:   e.quotas.record,
			WarningSink: func(w string) { log.Printf("prism: codex %s warning: %s", id, w) },
		}, nil
	case config.WireAntigravity:
		return antigravity.NewRunner(antigravityCreds{ref: e.refresher}, e.streamClient, p.BaseURL, func() config.Provider { return e.cfg.Get().Config.Providers[id] })
	case config.WireOpenAIResponses, config.WireOpenAIChat, config.WireAnthropicMessages:
		return wireDispatcher{
			creds:     &e.creds,
			responses: customresponses.New(customKey{e.creds}.Resolve, customresponses.Options{}),
			chat:      customchat.New(customKey{e.creds}.Resolve, customchat.Options{}),
			messages:  anthropicRunner{runner: anthropic.New(anthropic.Options{BaseURL: p.BaseURL, HTTP: e.streamClient}), creds: e.creds, provider: providerID},
		}, nil
	case config.WireCline:
		return clineRunner(e, p), nil
	default:
		return nil, fmt.Errorf("prism: provider %q has unknown wire %q", id, p.Wire)
	}
}

func clineRunner(e *daemonEnv, p config.Provider) provider.Runner {
	extra := make([]customchat.Header, 0, len(cline.ProductHeaders()))
	for name, value := range cline.ProductHeaders() {
		extra = append(extra, customchat.Header{Name: name, Value: value})
	}
	resolve := func(_ context.Context, target provider.Target, _ account.Lease) (string, error) {
		return target.APIKeyRef, nil
	}
	inner := customchat.New(resolve, customchat.Options{ExtraHeaders: extra, Client: &http.Client{Transport: cline.UnwrapTransport{}}})
	return clineAuthRunner{inner: inner, chatBase: cline.GatewayBase(p.BaseURL), env: e}
}

type clineAuthRunner struct {
	inner    provider.Runner
	chatBase string
	env      *daemonEnv
}

func (r clineAuthRunner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	if r.env.refresher == nil {
		return provider.CredentialRunError(fmt.Errorf("credential refresher is not configured"))
	}
	cred, err := r.env.refresher.Credential(ctx, req.Lease)
	if err != nil {
		return provider.CredentialRunError(err)
	}
	req.Target.APIKeyRef = cred.Access
	if r.chatBase != "" {
		req.Target.BaseURL = r.chatBase + "/api/v1"
	}
	if req.CredentialObserver != nil {
		req.CredentialObserver(cred.Generation)
	}
	err = r.inner.Run(ctx, req, sink)
	var runErr provider.RunError
	if !errors.As(err, &runErr) || runErr.Class != provider.ClassUnauthorized || runErr.Kind == provider.TerminalEmitted || runErr.Kind == provider.UnsafeReplay {
		return err
	}
	cred, err = r.env.refresher.RefreshRejected(ctx, req.Lease, cred.Access)
	if err != nil {
		return provider.CredentialRunError(err)
	}
	req.Target.APIKeyRef = cred.Access
	if req.CredentialObserver != nil {
		req.CredentialObserver(cred.Generation)
	}
	return r.inner.Run(ctx, req, sink)
}

func (e *daemonEnv) reconcileOnce(ctx context.Context) {
	snap := e.cfg.Get()
	if e.codex != nil && snap.Config.Integrations["codex"].Enabled && e.codex.ManagedBinding() {
		if err := e.codex.RefreshCatalog(); err != nil {
			log.Printf("prism: reconcile codex catalog: %v", err)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(snap.Config.Providers)) {
		p := snap.Config.Providers[id]
		providerID := account.ProviderID(id)
		if _, ok := e.registry.Lookup(providerID); !ok {
			if err := e.ensureProvider(ctx, id, p); err != nil {
				log.Printf("prism: reconcile provider %s: %v", id, err)
				continue
			}
			log.Printf("prism: provider %s applied without restart", id)
		}
		if known, ok := e.wires[providerID]; ok && known != p.Wire {
			runner, err := e.buildRunner(id, p)
			if err != nil {
				log.Printf("prism: provider %s wire %q: %v", id, p.Wire, err)
				continue
			}
			if err := e.registry.Replace(providerID, runner); err != nil {
				log.Printf("prism: provider %s wire swap: %v", id, err)
				continue
			}
			e.ensureFlows(providerID, p.Wire)
			e.wires[providerID] = p.Wire
			log.Printf("prism: provider %s wire %q applied without restart", id, p.Wire)
		}
		if p.Wire.Custom() && p.BaseURL != "" {
			e.creds.Committed(context.Background(), id, p, p)
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
	if current.APIKeyRef == "" && e.storedKey(ctx, pid, current.APIKeyRef) != "" {
		current.APIKeyRef = id + ":default"
		doc.Providers[id] = current
	}
	if len(current.Models) == 0 {
		if since := e.now().Sub(e.lastDiscover[pid]); since < discoverRetryAfter {
			e.saveDoc(id, p, doc, snap.Generation)
			return
		}
		e.lastDiscover[pid] = e.now()
		models, err := fetchModels(ctx, e.client, current, e.storedKey(ctx, pid, current.APIKeyRef))
		if err != nil {
			log.Printf("prism: discover models for %s: %v", id, err)
			e.saveDoc(id, p, doc, snap.Generation)
			return
		}
		current.Models = models
		current.SyncedModels = models
		doc.Providers[id] = current
		log.Printf("prism: provider %s models discovered (%d)", id, len(models))
	}
	e.saveDoc(id, p, doc, snap.Generation)
}

func (e *daemonEnv) saveDoc(id string, prev config.Provider, doc config.Document, generation uint64) {
	current := doc.Providers[id]
	if current.APIKeyRef == prev.APIKeyRef && len(current.Models) == len(prev.Models) {
		return
	}
	updated, err := e.cfg.Update(doc, generation)
	if err != nil {
		if !errors.Is(err, config.ErrStaleGeneration) {
			log.Printf("prism: provider %s sync: %v", id, err)
		}
		return
	}
	e.creds.Committed(context.Background(), id, prev, updated.Config.Providers[id])
}

func (e *daemonEnv) storedKey(ctx context.Context, providerID account.ProviderID, ref string) string {
	b, _, err := e.creds.file.GetCustomDefaultSecret(ctx, providerID, ref)
	if err != nil {
		return ""
	}
	return string(b)
}

func fetchModels(ctx context.Context, client *http.Client, p config.Provider, apiKey string) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	reqCtx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	req, err := modelsRequest(reqCtx, p, apiKey)
	if err != nil {
		return nil, err
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
