package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
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

	"prism/internal/account"
	"prism/internal/agentinstall"
	"prism/internal/auth"
	"prism/internal/buildinfo"
	"prism/internal/canon"
	"prism/internal/config"
	"prism/internal/integrations"
	"prism/internal/management"
	"prism/internal/provider"
	"prism/internal/providers/anthropic"
	"prism/internal/providers/antigravity"
	"prism/internal/providers/codex"
	"prism/internal/quota"
	"prism/internal/requestlog"
	"prism/internal/server"
	"prism/internal/store"
	"prism/internal/usage"
	"prism/internal/webui"
)

type options struct {
	listen         string
	configPath     string
	credentialPath string
	mgmtToken      string
	webuiDir       string
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
	creds  credentialStore
	client *http.Client
}

func (m modelSyncer) RemoteModels(ctx context.Context, id string, p config.Provider) ([]string, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("provider %s has no baseURL to list models from", id)
	}
	blob, err := m.creds.blob(ctx, account.ProviderID(id), account.AccountID(id+":default"), 1)
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
	req.Header.Set("Authorization", "Bearer "+string(blob))
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
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("malformed models response from %s: %w", p.BaseURL, err)
	}
	out := make([]string, 0, len(envelope.Data))
	for _, row := range envelope.Data {
		if row.ID != "" {
			out = append(out, row.ID)
		}
	}
	return out, nil
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
				settings := p.ModelSettings[model]
				out = append(out, integrations.Model{
					ID:            id + "/" + model,
					Name:          id + "/" + model,
					ContextWindow: snap.Config.ResolveContextWindow(id, model),
					ImageInput:    settings.ImageInput || snap.Config.VisionSidecar.Enabled,
				})
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
		log.Printf("prismd: quota warning %s: %s", id, w)
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
		log.Printf("prismd: quota probe %s: %v", id, err)
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

func loadOrCreateSecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) == 0 {
			return nil, fmt.Errorf("prismd: empty secret file %s", path)
		}
		return b, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("prismd: secret file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("prismd: secret dir: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("prismd: affinity secret: %w", err)
	}
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, fmt.Errorf("prismd: secret file: %w", err)
	}
	return secret, nil
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
	fs := flag.NewFlagSet("prismd", flag.ContinueOnError)
	fs.StringVar(&opts.configPath, "config", opts.configPath, "configuration file path")
	fs.StringVar(&opts.credentialPath, "credential-store", opts.credentialPath, "credential store directory")
	fs.StringVar(&opts.listen, "listen", opts.listen, "HTTP listen address")
	fs.StringVar(&opts.webuiDir, "webui", "", "serve the web UI bundle from this directory under /ui")
	fs.StringVar(&opts.mgmtToken, "management-token", opts.mgmtToken, "bearer token required for remote management API access (loopback is exempt)")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintln(out, "prismd is the Prism local proxy daemon.")
		fmt.Fprintln(out, "Usage: prismd [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("prismd: unexpected argument %q", fs.Arg(0))
	}
	if _, _, err := net.SplitHostPort(opts.listen); err != nil {
		return options{}, fmt.Errorf("prismd: invalid --listen %q: %w", opts.listen, err)
	}
	if *showVersion {
		fmt.Println(buildinfo.Version)
		os.Exit(0)
	}
	return opts, nil
}

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Printf("prismd: %v", err)
		os.Exit(1)
	}
	if err := run(opts); err != nil {
		log.Printf("prismd: %v", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	secret, err := loadOrCreateSecret(filepath.Join(opts.credentialPath, "secret.bin"))
	if err != nil {
		return err
	}
	cfg, err := config.Open(opts.configPath)
	if err != nil {
		return err
	}
	d := cfg.Get().Config
	creds := credentialStore{file: store.NewFileCredentialStore(opts.credentialPath)}
	pool := account.New(secret, time.Now)
	registry := provider.NewRegistry()
	client := &http.Client{}
	quotas := newQuotaTable(pool, cfg, client)
	env := newDaemonEnv(opts.credentialPath, cfg, pool, quotas, registry, creds, client)
	refresher, err := auth.NewRefresher(auth.RefresherOptions{File: creds.file, Repos: env.repos, Pool: pool, Flows: env.flows})
	if err != nil {
		return err
	}
	env.refresher = refresher
	quotas.refresher = refresher
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	for _, id := range slices.Sorted(maps.Keys(d.Providers)) {
		if err := env.ensureProvider(ctx, id, d.Providers[id]); err != nil {
			return err
		}
	}
	var authService *auth.Service
	if len(env.flows) > 0 {
		authService, err = auth.New(auth.NewFileSink(creds.file, env.repos, pool), env.flows, auth.Options{})
		if err != nil {
			return fmt.Errorf("prismd: auth service: %w", err)
		}
	}
	_, daemonPort, splitErr := net.SplitHostPort(opts.listen)
	if splitErr != nil {
		return fmt.Errorf("prismd: listen address: %w", splitErr)
	}
	daemonPortNum, convErr := strconv.Atoi(daemonPort)
	if convErr != nil {
		return fmt.Errorf("prismd: listen port: %w", convErr)
	}
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		home = ""
	}
	daemonEnv := integrations.Environ(os.Environ())
	modelsSrc := integrationModels(cfg)
	intg, codexIntegration, err := newIntegrationRegistry(daemonPortNum, daemonEnv, home, nil, modelsSrc)
	if err != nil {
		return err
	}
	env.codex = codexIntegration
	hostTable := management.NewHostRegistries(intg)
	supervisor := newHostSupervisor(ctx, daemonPortNum, modelsSrc, hostTable)
	for id, hostCfg := range d.Hosts {
		hostTable.SetHostConfig(id, hostCfg)
		if hostCfg.DaemonPort > 0 {
			continue
		}
		if err := supervisor.Ensure(ctx, id, hostCfg.Address); err != nil {
			log.Printf("prismd: host %s (%s) unresolved: %v", id, hostCfg.Address, err)
		}
	}


	installer := agentinstall.NewManager(daemonEnv, agentinstall.ExecRunner{}, os.Stat, time.Now, agentinstall.FetchScript)
	planner := server.NewConfigPlanner(cfg)
	usageStore, err := usage.Open(filepath.Join(opts.credentialPath, "usage.db"))
	if err != nil {
		return fmt.Errorf("prismd: usage store: %w", err)
	}
	defer usageStore.Close()
	rlog := requestlog.New(500, time.Now)
	mgmt := management.New(pool, cfg, catalog{cfg}, quotas, creds, authService, intg, modelSyncer{creds: creds, client: client}, installer)
	mgmt.SetAccountStore(durableAccountStore{pool: pool, file: creds.file, repos: env.repos})
	mgmt.SetHostRegistries(hostTable)
	mgmt.SetHostLifecycle(supervisor)
	mgmt.SetUsageStore(usageStore)
	mgmt.SetRequestLog(rlog)
	var webUI http.Handler
	if opts.webuiDir != "" {
		webUI = webui.New(opts.webuiDir)
		log.Printf("prismd: webui serving %s at /ui", opts.webuiDir)
	}
	h := server.New(server.Options{Planner: planner, Registry: registry, Pool: pool, Config: cfg, Management: mgmt.Handler(), ManagementToken: opts.mgmtToken, WebUI: webUI, Usage: usageStore, RequestLog: rlog}).Handler()
	httpServer := &http.Server{Addr: opts.listen, Handler: h}
	go env.loop(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	log.Printf("prismd: listening on %s config=%s credentials=%s", opts.listen, opts.configPath, opts.credentialPath)
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
	log.Printf("prismd: shutdown complete")
	return nil
}
