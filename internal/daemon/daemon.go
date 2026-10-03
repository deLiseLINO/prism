package daemon

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/agentinstall"
	"github.com/deLiseLINO/prism/internal/auth"
	"github.com/deLiseLINO/prism/internal/buildinfo"
	"github.com/deLiseLINO/prism/internal/canon"
	modelcat "github.com/deLiseLINO/prism/internal/catalog"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/integrations"
	"github.com/deLiseLINO/prism/internal/management"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/anthropic"
	"github.com/deLiseLINO/prism/internal/providers/antigravity"
	"github.com/deLiseLINO/prism/internal/providers/cline"
	"github.com/deLiseLINO/prism/internal/providers/codex"
	"github.com/deLiseLINO/prism/internal/quota"
	"github.com/deLiseLINO/prism/internal/requestlog"
	"github.com/deLiseLINO/prism/internal/server"
	"github.com/deLiseLINO/prism/internal/store"
	"github.com/deLiseLINO/prism/internal/usage"
	"github.com/deLiseLINO/prism/internal/webui"
)

type options struct {
	listen         string
	configPath     string
	credentialPath string
	mgmtToken      string
	webuiDir       string
	showVersion    bool
}

type credentialStore struct{ file *store.FileCredentialStore }

type durableAccountStore struct {
	pool  account.Pool
	file  *store.FileCredentialStore
	repos map[account.ProviderID]*account.Repository
}

func (d durableAccountStore) DeleteDurable(ctx context.Context, id account.AccountID) error {
	var provider account.ProviderID
	for _, a := range d.pool.Snapshot().Accounts {
		if a.ID != id {
			continue
		}
		provider = a.Provider
		break
	}
	if provider == "" {
		parts := strings.SplitN(string(id), ":", 2)
		if len(parts) != 2 {
			return nil
		}
		provider = account.ProviderID(parts[0])
	}
	if err := d.file.Delete(ctx, provider, id); err != nil {
		return err
	}
	if repo, ok := d.repos[provider]; ok {
		if err := repo.Delete(provider, id); err != nil && !errors.Is(err, account.ErrNotFound) {
			return err
		}
	}
	return nil
}

type modelSyncer struct {
	creds     credentialStore
	pool      account.Pool
	refresher credentialRefresher
	client    *http.Client
}

type listedModel struct {
	ID            string
	ContextWindow *int
	Image         *bool
}

func (m modelSyncer) RemoteModels(ctx context.Context, id string, p config.Provider) ([]management.ListedModel, error) {
	rows, err := m.remoteModels(ctx, id, p)
	if err != nil {
		return nil, err
	}
	out := make([]management.ListedModel, len(rows))
	for i, row := range rows {
		out[i] = management.ListedModel{ID: row.ID, ContextWindow: row.ContextWindow, Image: row.Image}
	}
	return out, nil
}

func (m modelSyncer) remoteModels(ctx context.Context, id string, p config.Provider) ([]listedModel, error) {
	switch p.Wire {
	case config.WireCodex, config.WireAntigravity, config.WireCline:
		return m.remoteModelsPooled(ctx, id, p)
	}
	return m.remoteModelsCustom(ctx, id, p)
}

// remoteModelsPooled serves the native wires. Their model listings live on
// provider-owned endpoints keyed to a real account, not the config baseURL.
func (m modelSyncer) remoteModelsPooled(ctx context.Context, id string, p config.Provider) ([]listedModel, error) {
	lease, ok := m.leaseFor(id, p)
	if !ok {
		return nil, fmt.Errorf("provider %s has no active account to list models for", id)
	}
	if m.refresher == nil {
		return nil, fmt.Errorf("provider %s model sync is not wired: no credential refresher", id)
	}
	cred, err := m.refresher.Credential(ctx, lease)
	if err != nil {
		return nil, fmt.Errorf("credential for %s: %w", lease.Account, err)
	}
	switch p.Wire {
	case config.WireCodex:
		rows, err := codex.FetchModelList(ctx, m.client, p.BaseURL, codex.Credential{AccessToken: cred.Access, ChatGPTAccountID: cred.AccountID})
		if err != nil {
			return nil, err
		}
		out := make([]listedModel, len(rows))
		for i, row := range rows {
			out[i] = listedModel{ID: row.ID, ContextWindow: row.ContextWindow, Image: row.Image}
		}
		return out, nil
	case config.WireAntigravity:
		ids, err := antigravity.FetchModels(ctx, m.client, p.BaseURL, antigravity.CredentialPair{AccessToken: cred.Access, ProjectID: cred.ProjectID})
		if err != nil {
			return nil, err
		}
		return idsToListed(ids), nil
	case config.WireCline:
		ids, err := cline.FetchModels(ctx, m.client, p.BaseURL, cline.EnsureWorkosPrefix(cred.Access))
		if err != nil {
			return nil, err
		}
		return idsToListed(ids), nil
	}
	return nil, fmt.Errorf("provider %s wire %q does not support model listing", id, p.Wire)
}

func idsToListed(ids []string) []listedModel {
	out := make([]listedModel, len(ids))
	for i, id := range ids {
		out[i] = listedModel{ID: id}
	}
	return out
}

func (m modelSyncer) leaseFor(id string, p config.Provider) (account.Lease, bool) {
	if m.pool == nil {
		return account.Lease{}, false
	}
	snap := m.pool.Snapshot()
	providerID := account.ProviderID(id)
	selected := ""
	if p.Pool != nil {
		selected = p.Pool.PinnedAccount
	}
	var chosen *account.Account
	for i := range snap.Accounts {
		a := &snap.Accounts[i]
		if a.Provider != providerID {
			continue
		}
		if selected != "" && string(a.ID) != selected {
			continue
		}
		if selected == "" && chosen != nil {
			return account.Lease{}, false
		}
		chosen = a
	}
	if chosen == nil || chosen.State == account.Paused || chosen.State == account.NeedsReauth {
		return account.Lease{}, false
	}
	return account.Lease{Provider: providerID, Account: chosen.ID, CredGen: chosen.CredGen}, true
}

func (m modelSyncer) remoteModelsCustom(ctx context.Context, id string, p config.Provider) ([]listedModel, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("provider %s has no baseURL to list models from", id)
	}
	blob, ok, err := m.creds.file.Get(ctx, account.ProviderID(id), account.AccountID(id+":default"), 1)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	base = strings.TrimSuffix(base, "/responses")
	base = strings.TrimSuffix(base, "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if ok {
		req.Header.Set("Authorization", "Bearer "+string(blob))
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("upstream %s: %s", p.BaseURL, strings.TrimSpace(string(body)))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	rows, err := parseOpenAIModelList(raw)
	if err != nil {
		return nil, fmt.Errorf("%w from %s", err, p.BaseURL)
	}
	return rows, nil
}

func (s credentialStore) blob(ctx context.Context, p account.ProviderID, a account.AccountID, g account.CredentialGeneration) ([]byte, error) {
	b, ok, err := s.file.Get(ctx, p, a, g)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("credential not configured for %s/%s", p, a)
	}
	return b, nil
}
func (s credentialStore) Put(ctx context.Context, id string, secret []byte) error {
	return s.file.PutIdempotent(ctx, account.ProviderID(id), account.AccountID(id+":default"), 1, secret)
}
func (s credentialStore) Delete(ctx context.Context, id string) error {
	return s.file.Delete(ctx, account.ProviderID(id), account.AccountID(id+":default"))
}
func (s credentialStore) Configured(ctx context.Context, id string) (bool, error) {
	_, ok, err := s.file.Get(ctx, account.ProviderID(id), account.AccountID(id+":default"), 1)
	return ok, err
}

type codexCreds struct{ ref *auth.Refresher }

func (c codexCreds) Credential(ctx context.Context, lease account.Lease) (codex.Credential, error) {
	cred, err := c.ref.Credential(ctx, lease)
	if err != nil {
		return codex.Credential{}, err
	}
	// Only the dispatch surface leaves the coordinator: no refresh grant or
	// expiry metadata is exposed to the runner.
	return codex.Credential{AccessToken: cred.Access, ChatGPTAccountID: cred.AccountID}, nil
}

type antigravityCreds struct{ ref *auth.Refresher }

func (c antigravityCreds) Credential(ctx context.Context, lease account.Lease) (antigravity.CredentialPair, error) {
	cred, err := c.ref.Credential(ctx, lease)
	if err != nil {
		return antigravity.CredentialPair{}, err
	}
	return antigravity.CredentialPair{AccessToken: cred.Access, ProjectID: cred.ProjectID}, nil
}

type catalog struct{ cfg *config.Manager }

func (c catalog) Models(ctx context.Context) ([]provider.Model, error) {
	d := c.cfg.Get().Config
	out := make([]provider.Model, 0)
	for id, p := range d.Providers {
		if !p.IsEnabled() {
			continue
		}
		for _, model := range p.Models {
			if slices.Contains(p.DisabledModels, model) {
				continue
			}
			out = append(out, provider.Model{ID: canon.ModelID(id + "/" + model)})
		}
	}
	return out, nil
}

// integrationModels is the live model source for the grok and omp managed
// blocks: every enabled provider model in the daemon config, custom providers
// included, namespaced as "<provider>/<model>".
// defaultReasoningEffort pins the rung a client config advertises as its
// starting selection: medium when the ladder carries it, else high, else the
// first rung.
func defaultReasoningEffort(efforts []string) string {
	for _, want := range []string{"medium", "high"} {
		for _, effort := range efforts {
			if effort == want {
				return effort
			}
		}
	}
	if len(efforts) > 0 {
		return efforts[0]
	}
	return ""
}

func integrationModels(m *config.Manager) func() []integrations.Model {
	return func() []integrations.Model {
		out := make([]integrations.Model, 0)
		snap := m.Get()
		for id, p := range snap.Config.Providers {
			if !p.IsEnabled() {
				continue
			}
			for _, model := range p.Models {
				if slices.Contains(p.DisabledModels, model) {
					continue
				}
				efforts := snap.Config.ResolveReasoningEfforts(id, model)
				entry := integrations.Model{
					ID:                     id + "/" + model,
					Name:                   id + "/" + model,
					ContextWindow:          snap.Config.ResolveContextWindow(id, model),
					ImageInput:             snap.Config.ResolveImageInput(id, model),
					ReasoningEfforts:       efforts,
					DefaultReasoningEffort: defaultReasoningEffort(efforts),
				}
				if snap.Config.ResolveWire(id, model) == config.WireAnthropicMessages {
					entry.API = "anthropic-messages"
				}
				out = append(out, entry)
			}
		}
		return out
	}
}

type anthropicRunner struct {
	runner   *anthropic.Runner
	creds    credentialStore
	provider account.ProviderID
}

func (r anthropicRunner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	b, err := r.creds.blob(ctx, r.provider, req.Lease.Account, req.Lease.CredGen)
	if err != nil {
		return provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassUnauthorized, Cause: err}
	}
	req.Target.APIKeyRef = string(b)
	return r.runner.Run(ctx, req, sink)
}

type customKey struct {
	store credentialStore
}

func (c customKey) Resolve(ctx context.Context, target provider.Target, lease account.Lease) (string, error) {
	b, err := c.store.blob(ctx, target.Provider, lease.Account, lease.CredGen)
	return string(b), err
}

type quotaPool interface {
	UpdateQuota(id account.AccountID, snap quota.Snapshot) error
	Snapshot() account.Snapshot
}

const quotaProbeTTL = 5 * time.Minute

type quotaTable struct {
	pool      quotaPool
	cfg       *config.Manager
	client    *http.Client
	refresher credentialRefresher

	mu        sync.Mutex
	lastProbe map[account.AccountID]time.Time
}

type credentialRefresher interface {
	Credential(ctx context.Context, lease account.Lease) (account.Credential, error)
}

func newQuotaTable(pool quotaPool, cfg *config.Manager, client *http.Client) *quotaTable {
	return &quotaTable{pool: pool, cfg: cfg, client: client, lastProbe: make(map[account.AccountID]time.Time)}
}

func (t *quotaTable) record(id account.AccountID, s quota.Snapshot, warnings []string) {
	if s.Used != 0 || s.Limit != nil || !s.WindowEnd.IsZero() {
		_ = t.pool.UpdateQuota(id, s)
	}
	for _, w := range warnings {
		log.Printf("prism: quota warning %s: %s", id, w)
	}
}

// Quota answers with the stored snapshot, actively probing the provider's
// quota endpoint when the stored one is stale. A failed probe never lies:
// the caller sees the last known snapshot, or an honest unknown one.
func (t *quotaTable) Quota(ctx context.Context, id account.AccountID) (quota.Snapshot, error) {
	return t.quota(ctx, id, false)
}

func (t *quotaTable) RefreshQuota(ctx context.Context, id account.AccountID) (quota.Snapshot, error) {
	return t.quota(ctx, id, true)
}

func (t *quotaTable) quota(ctx context.Context, id account.AccountID, force bool) (quota.Snapshot, error) {
	var stored quota.Snapshot
	var provider account.ProviderID
	var credGen account.CredentialGeneration
	found := false
	for _, a := range t.pool.Snapshot().Accounts {
		if a.ID == id {
			stored, provider, credGen, found = a.Quota, a.Provider, a.CredGen, true
			break
		}
	}
	if !found {
		return quota.Snapshot{}, account.ErrNotFound
	}
	if !t.probeDue(id, force) {
		return stored, nil
	}
	snap, err := t.probe(ctx, id, provider, credGen)
	if err != nil {
		log.Printf("prism: quota probe %s: %v", id, err)
		if force {
			return quota.Snapshot{}, err
		}
		return stored, nil
	}
	return snap, nil
}

func governingWindowSnapshot(windows []quota.Window) quota.Snapshot {
	var governing *quota.Window
	for i := range windows {
		w := &windows[i]
		if governing == nil || usedWindowPercent(w) > usedWindowPercent(governing) {
			governing = w
		}
	}
	if governing == nil {
		return quota.Snapshot{}
	}
	snap := quota.Snapshot{
		Used:      governing.Used,
		Limit:     governing.Limit,
		WindowEnd: governing.WindowEnd,
		Source:    quota.SourceEndpoint,
		Windows:   append([]quota.Window(nil), windows...),
	}
	return snap
}

func usedWindowPercent(w *quota.Window) float64 {
	limit := 10000.0
	if w.Limit != nil && *w.Limit > 0 {
		limit = float64(*w.Limit)
	}
	return float64(w.Used) / limit * 100
}

func (t *quotaTable) probeDue(id account.AccountID, force bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.lastProbe[id]; !force && ok && time.Since(last) < quotaProbeTTL {
		return false
	}
	t.lastProbe[id] = time.Now()
	return true
}

func (t *quotaTable) probe(ctx context.Context, id account.AccountID, provider account.ProviderID, credGen account.CredentialGeneration) (quota.Snapshot, error) {
	if t.cfg == nil || t.client == nil || t.refresher == nil {
		return quota.Snapshot{}, fmt.Errorf("quota provider is not configured")
	}
	p, ok := t.cfg.Get().Config.Providers[string(provider)]
	if !ok {
		return quota.Snapshot{}, fmt.Errorf("quota provider %s not found", provider)
	}
	if p.Wire != config.WireCodex && p.Wire != config.WireAntigravity {
		return quota.Snapshot{}, fmt.Errorf("provider %s does not support quota refresh", provider)
	}
	lease := account.Lease{Provider: provider, Account: id, CredGen: credGen}
	cred, err := t.refresher.Credential(ctx, lease)
	if err != nil {
		return quota.Snapshot{}, fmt.Errorf("credential: %w", err)
	}
	var snap quota.Snapshot
	switch p.Wire {
	case config.WireCodex:
		result, err := codex.FetchUsage(ctx, t.client, codex.Credential{AccessToken: cred.Access, ChatGPTAccountID: cred.AccountID})
		if err != nil {
			return quota.Snapshot{}, err
		}
		if !result.OK {
			return quota.Snapshot{}, fmt.Errorf("provider %s returned no quota", provider)
		}
		snap = result.Snapshot
	case config.WireAntigravity:
		credPair := antigravity.CredentialPair{AccessToken: cred.Access, ProjectID: cred.ProjectID}
		summary, err := antigravity.FetchQuotaSummary(ctx, t.client, p.BaseURL, credPair)
		if err != nil {
			return quota.Snapshot{}, err
		}
		if summaryWindows := summary.Windows(); len(summaryWindows) > 0 {
			snap = governingWindowSnapshot(summaryWindows)
		} else {
			windows, err := antigravity.FetchQuota(ctx, t.client, p.BaseURL, credPair)
			if err != nil {
				return quota.Snapshot{}, err
			}
			snap, ok = windows.DetailedSnapshot()
			if !ok {
				return quota.Snapshot{}, fmt.Errorf("provider %s returned no quota", provider)
			}
			snap.Windows = windows.QuotaWindows()
		}
	}
	if err := t.pool.UpdateQuota(id, snap); err != nil {
		return quota.Snapshot{}, err
	}
	return snap, nil
}

func defaultStateDir() string {
	if dir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(dir, ".prism")
	}
	return ".prism"
}

func parseFlags(args []string) (options, error) {
	stateDir := defaultStateDir()
	opts := options{
		listen:         "127.0.0.1:10200",
		configPath:     filepath.Join(stateDir, "prism.json"),
		credentialPath: filepath.Join(stateDir, "credentials"),
		mgmtToken:      os.Getenv("PRISM_MGMT_TOKEN"),
	}
	fs := flag.NewFlagSet("prism daemon", flag.ContinueOnError)
	fs.StringVar(&opts.configPath, "config", opts.configPath, "configuration file path")
	fs.StringVar(&opts.credentialPath, "credential-store", opts.credentialPath, "credential store directory")
	fs.StringVar(&opts.listen, "listen", opts.listen, "HTTP listen address")
	fs.StringVar(&opts.webuiDir, "webui", "", "serve the web UI bundle from this directory under /ui")
	fs.StringVar(&opts.mgmtToken, "management-token", opts.mgmtToken, "bearer token required for remote management API access (loopback is exempt)")
	fs.BoolVar(&opts.showVersion, "version", false, "print version and exit")
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintln(out, "prism daemon runs the Prism local proxy in the foreground.")
		fmt.Fprintln(out, "Usage: prism daemon [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if _, _, err := net.SplitHostPort(opts.listen); err != nil {
		return options{}, fmt.Errorf("invalid --listen %q: %w", opts.listen, err)
	}
	return opts, nil
}

func Run(args []string) int {
	opts, err := parseFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		log.Printf("prism daemon: %v", err)
		return 1
	}
	if opts.showVersion {
		fmt.Println(buildinfo.Version)
		return 0
	}
	if err := run(opts); err != nil {
		log.Printf("prism daemon: %v", err)
		return 1
	}
	return 0
}

func run(opts options) error {
	cfg, err := config.Open(opts.configPath)
	if err != nil {
		return err
	}
	modelCatalog, err := modelcat.Open(filepath.Dir(opts.configPath))
	if err != nil {
		return err
	}
	cfg.SetCatalog(modelCatalog)
	d := cfg.Get().Config
	creds := credentialStore{file: store.NewFileCredentialStore(opts.credentialPath)}
	pool := account.New()
	registry := provider.NewRegistry()
	client := &http.Client{Timeout: 30 * time.Second}
	quotas := newQuotaTable(pool, cfg, client)
	env := newDaemonEnv(opts.credentialPath, cfg, pool, quotas, registry, creds, client, &http.Client{})
	refresher, err := auth.NewRefresher(auth.RefresherOptions{File: creds.file, Repos: env.repos, Pool: pool, Flows: env.flows})
	if err != nil {
		return err
	}
	env.refresher = refresher
	quotas.refresher = refresher
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	env.ensureFlows("codex", config.WireCodex)
	env.ensureFlows("antigravity", config.WireAntigravity)
	env.ensureFlows("cline", config.WireCline)
	for _, id := range slices.Sorted(maps.Keys(d.Providers)) {
		if err := env.ensureProvider(ctx, id, d.Providers[id]); err != nil {
			return err
		}
	}
	var authService *auth.Service
	if len(env.flows) > 0 {
		sink := auth.NewFileSink(creds.file, env.repos, pool)
		sink.OnStored(env.noteStoredAccount)
		authService, err = auth.New(sink, env.flows, auth.Options{})
		if err != nil {
			return fmt.Errorf("prism: auth service: %w", err)
		}
	}
	_, daemonPort, splitErr := net.SplitHostPort(opts.listen)
	if splitErr != nil {
		return fmt.Errorf("prism: listen address: %w", splitErr)
	}
	daemonPortNum, convErr := strconv.Atoi(daemonPort)
	if convErr != nil {
		return fmt.Errorf("prism: listen port: %w", convErr)
	}
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		home = ""
	}
	daemonEnv := agentinstall.WithUserPath(integrations.Environ(os.Environ()))
	modelsSrc := integrationModels(cfg)
	intg, codexIntegration, err := newIntegrationRegistry(daemonPortNum, daemonEnv, home, nil, modelsSrc)
	if err != nil {
		return err
	}
	env.codex = codexIntegration
	intg.SetEnabledSource(func() map[integrations.ID]bool {
		out := make(map[integrations.ID]bool)
		for id, settings := range cfg.Get().Config.Integrations {
			if settings.Enabled {
				out[integrations.ID(id)] = true
			}
		}
		return out
	})
	hostTable := management.NewHostRegistries(intg)
	supervisor := newHostSupervisor(ctx, daemonPortNum, modelsSrc, hostTable)
	for id, hostCfg := range d.Hosts {
		hostTable.SetHostConfig(id, hostCfg)
		if hostCfg.DaemonPort > 0 {
			continue
		}
		if err := supervisor.Ensure(ctx, id, hostCfg.Address); err != nil {
			log.Printf("prism: host %s (%s) unresolved: %v", id, hostCfg.Address, err)
		}
	}

	installer := agentinstall.NewManager(daemonEnv, agentinstall.ExecRunner{}, os.Stat, time.Now, agentinstall.FetchScript)
	management.AgentActions = management.ParseAgentActionsEnv(os.Getenv("PRISM_AGENT_ACTIONS"))
	if management.AgentActions {
		log.Printf("prism: agent install and update actions enabled via PRISM_AGENT_ACTIONS")
	}
	planner := server.NewConfigPlanner(cfg)
	if err := os.MkdirAll(opts.credentialPath, 0o700); err != nil {
		return fmt.Errorf("prism: credential store directory: %w", err)
	}
	usageStore, err := usage.Open(filepath.Join(opts.credentialPath, "usage.db"))
	if err != nil {
		return fmt.Errorf("prism: usage store: %w", err)
	}
	defer usageStore.Close()
	rlog := requestlog.New(500, time.Now)
	mgmt := management.New(pool, cfg, catalog{cfg}, quotas, creds, authService, intg, modelSyncer{creds: creds, pool: pool, refresher: refresher, client: client}, installer)
	mgmt.SetAccountStore(durableAccountStore{pool: pool, file: creds.file, repos: env.repos})
	mgmt.SetHostRegistries(hostTable)
	mgmt.SetHostLifecycle(supervisor)
	mgmt.SetUsageStore(usageStore)
	mgmt.SetRequestLog(rlog)
	var webUI http.Handler
	if opts.webuiDir != "" {
		webUI = webui.New(opts.webuiDir)
		log.Printf("prism: webui serving %s at /ui", opts.webuiDir)
	}
	h := server.New(server.Options{Planner: planner, Registry: registry, Pool: pool, Config: cfg, Management: mgmt.Handler(), ManagementToken: opts.mgmtToken, WebUI: webUI, Usage: usageStore, RequestLog: rlog}).Handler()
	httpServer := &http.Server{Addr: opts.listen, Handler: h}
	go env.loop(ctx)
	go watchIntegrations(ctx, cfg, intg)
	go refreshModelCatalog(ctx, modelCatalog, client)
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Cancel in-flight installs first so their runners stop within the same
	// shutdown window; jobs record their own interrupted state.
	stopDone := make(chan struct{})
	go func() {
		installer.Stop(shutdownCtx)
		close(stopDone)
	}()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if authService != nil {
		if err := authService.Close(shutdownCtx); err != nil {
			return err
		}
	}
	<-stopDone
	log.Printf("prism: shutdown complete")
	return nil
}

func refreshModelCatalog(ctx context.Context, index *modelcat.Index, client *http.Client) {
	refresh := func() {
		if err := index.Refresh(ctx, client); err != nil && ctx.Err() == nil {
			log.Printf("prism: model catalog refresh: %v", err)
		}
	}
	refresh()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}
