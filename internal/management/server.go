package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"prism/internal/account"
	"prism/internal/auth"
	"prism/internal/buildinfo"
	"prism/internal/config"
	"prism/internal/integrations"
	"prism/internal/provider"
	"prism/internal/providers/antigravity"
	"prism/internal/quota"
	"prism/internal/requestlog"
)

type ConfigStore interface {
	Get() config.Snapshot
	Update(next config.Document, expected uint64) (config.Snapshot, error)
}

type Catalog interface {
	Models(ctx context.Context) ([]provider.Model, error)
}

type ModelSyncer interface {
	RemoteModels(ctx context.Context, id string, p config.Provider) ([]string, error)
}

type CredentialStore interface {
	Put(ctx context.Context, id string, secret []byte) error
	Delete(ctx context.Context, id string) error
	Configured(ctx context.Context, id string) (bool, error)
}

type Auth interface {
	Start(ctx context.Context, provider account.ProviderID) (auth.AuthStart, error)
	Complete(ctx context.Context, provider account.ProviderID, cb auth.AuthCallback) error
	Status(ctx context.Context, provider account.ProviderID, session auth.AuthSessionID) (auth.AuthStatus, error)
}

type AccountDeleter interface {
	DeleteAccount(ctx context.Context, id account.AccountID) error
}

type AccountStore interface {
	DeleteDurable(ctx context.Context, id account.AccountID) error
}

type Server struct {
	pool      account.Pool
	cfg       ConfigStore
	catalog   Catalog
	quota     provider.QuotaSource
	creds     CredentialStore
	auth      Auth
	ints      *integrations.Registry
	hosts     *HostRegistries
	lifecycle HostLifecycle
	routes    [][]string
	store     AccountStore
	stats     UsageSource
	syncer    ModelSyncer
	reqlog    *requestlog.Journal
	installer Installer
}

func New(pool account.Pool, cfg ConfigStore, catalog Catalog, qs provider.QuotaSource, creds CredentialStore, auth Auth, ints *integrations.Registry, syncer ModelSyncer, installer Installer) *Server {
	return &Server{pool: pool, cfg: cfg, catalog: catalog, quota: qs, creds: creds, auth: auth, ints: ints, hosts: NewHostRegistries(ints), routes: [][]string{}, syncer: syncer, installer: installer}
}

// SetHostRegistries overrides the host table (tests inject a populated one).
func (s *Server) SetHostRegistries(h *HostRegistries) {
	s.hosts = h
}

// SetHostLifecycle wires the daemon-side host supervisor (probe, registry,
// tunnel) that host mutations trigger after the config write commits.
func (s *Server) SetHostLifecycle(l HostLifecycle) {
	s.lifecycle = l
}

func (s *Server) SetAccountStore(store AccountStore) {
	s.store = store
}

func (s *Server) SetRequestLog(j *requestlog.Journal) {
	s.reqlog = j
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/models", s.models)
	mux.HandleFunc("GET /api/v1/providers", s.providersList)
	mux.HandleFunc("POST /api/v1/providers", s.providersCreate)
	mux.HandleFunc("PUT /api/v1/providers/{id}", s.providersReplace)
	mux.HandleFunc("POST /api/v1/providers/{id}/sync-models", s.providersSyncModels)
	mux.HandleFunc("DELETE /api/v1/providers/{id}", s.providersDelete)
	mux.HandleFunc("PUT /api/v1/vision-sidecar", s.visionSidecarPut)
	mux.HandleFunc("PUT /api/v1/context-window", s.contextWindowPut)
	mux.HandleFunc("GET /api/v1/accounts", s.accountsList)
	mux.HandleFunc("DELETE /api/v1/accounts/{id}", s.accountsDelete)
	mux.HandleFunc("POST /api/v1/accounts/{id}/pause", s.accountPause)
	mux.HandleFunc("POST /api/v1/accounts/{id}/resume", s.accountResume)
	mux.HandleFunc("POST /api/v1/accounts/{id}/priority", s.accountPriority)
	mux.HandleFunc("GET /api/v1/accounts/{id}/quota", s.accountQuota)
	mux.HandleFunc("POST /api/v1/accounts/{id}/quota/refresh", s.accountQuotaRefresh)
	mux.HandleFunc("GET /api/v1/combos", s.combosList)
	mux.HandleFunc("PUT /api/v1/combos/{id}", s.combosPut)
	mux.HandleFunc("DELETE /api/v1/combos/{id}", s.combosDelete)
	mux.HandleFunc("GET /api/v1/routes", s.routesList)
	mux.HandleFunc("PUT /api/v1/routes/{key}", s.routesPut)
	mux.HandleFunc("DELETE /api/v1/routes/{key}", s.routesDelete)
	mux.HandleFunc("GET /api/v1/stats", s.statsHandler)
	mux.HandleFunc("GET /api/v1/requests", s.requests)
	mux.HandleFunc("GET /api/v1/usage", s.usage)
	mux.HandleFunc("POST /api/v1/auth/{provider}/start", s.authStart)
	mux.HandleFunc("POST /api/v1/auth/{provider}/callback", s.authCallback)
	mux.HandleFunc("GET /api/v1/auth/{provider}/status", s.authStatus)
	mux.HandleFunc("GET /api/v1/hosts", s.hostsList)
	mux.HandleFunc("POST /api/v1/hosts", s.hostsCreate)
	mux.HandleFunc("PUT /api/v1/hosts/{host}", s.hostsReplace)
	mux.HandleFunc("DELETE /api/v1/hosts/{host}", s.hostsDelete)

	mux.HandleFunc("GET /api/v1/hosts/{host}/integrations", s.hostIntegrationsList)
	mux.HandleFunc("GET /api/v1/hosts/{host}/integrations/{client}", s.hostIntegrationGet)
	mux.HandleFunc("POST /api/v1/hosts/{host}/integrations/{client}/apply", s.hostIntegrationApply)
	mux.HandleFunc("POST /api/v1/hosts/{host}/integrations/{client}/rollback", s.hostIntegrationRollback)
	mux.HandleFunc("GET /api/v1/integrations", s.integrationsList)
	mux.HandleFunc("GET /api/v1/integrations/{client}", s.integrationGet)
	mux.HandleFunc("POST /api/v1/integrations/{client}/apply", s.integrationApply)
	mux.HandleFunc("POST /api/v1/integrations/{client}/rollback", s.integrationRollback)
	mux.HandleFunc("PUT /api/v1/integrations/{client}/enabled", s.integrationToggle)
	mux.HandleFunc("GET /api/v1/agents", s.agentsList)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.agentGet)
	mux.HandleFunc("POST /api/v1/agents/{id}/install", s.agentInstall)
	mux.HandleFunc("POST /api/v1/agents/{id}/update", s.agentUpdate)
	mux.HandleFunc("GET /api/v1/agents/{id}/job", s.agentJob)
	for _, tpl := range routeTemplates {
		s.routes = append(s.routes, splitPath(tpl))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if s.matchesRoute(r.URL.Path) {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method "+r.Method+" not allowed")
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "unknown route")
	})
	return mux
}

var routeTemplates = []string{
	"/api/v1/health",
	"/api/v1/models",
	"/api/v1/providers",
	"/api/v1/providers/{id}",
	"/api/v1/accounts",
	"/api/v1/accounts/{id}",
	"/api/v1/accounts/{id}/pause",
	"/api/v1/accounts/{id}/resume",
	"/api/v1/accounts/{id}/priority",
	"/api/v1/accounts/{id}/quota",
	"/api/v1/accounts/{id}/quota/refresh",
	"/api/v1/combos",
	"/api/v1/combos/{id}",
	"/api/v1/routes",
	"/api/v1/stats",
	"/api/v1/routes/{key}",
	"/api/v1/requests",
	"/api/v1/usage",
	"/api/v1/auth/{provider}/start",
	"/api/v1/auth/{provider}/callback",
	"/api/v1/auth/{provider}/status",
	"/api/v1/hosts",
	"/api/v1/hosts/{host}",
	"/api/v1/hosts/{host}/integrations",
	"/api/v1/hosts/{host}/integrations/{client}",
	"/api/v1/hosts/{host}/integrations/{client}/apply",
	"/api/v1/hosts/{host}/integrations/{client}/rollback",
	"/api/v1/integrations",
	"/api/v1/integrations/{client}",
	"/api/v1/integrations/{client}/apply",
	"/api/v1/integrations/{client}/rollback",
	"/api/v1/integrations/{client}/enabled",
	"/api/v1/agents",
	"/api/v1/agents/{id}",
	"/api/v1/agents/{id}/install",
	"/api/v1/agents/{id}/update",
	"/api/v1/agents/{id}/job",
}

func splitPath(p string) []string {
	return strings.Split(strings.Trim(p, "/"), "/")
}

func (s *Server) matchesRoute(path string) bool {
	segs := splitPath(path)
	for _, tpl := range s.routes {
		if len(tpl) != len(segs) {
			continue
		}
		ok := true
		for i, t := range tpl {
			if strings.HasPrefix(t, "{") {
				if segs[i] == "" {
					ok = false
					break
				}
				continue
			}
			if t != segs[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok", Version: buildinfo.Version})
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	ms, err := s.catalog.Models(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]Model, 0, len(ms))
	for _, m := range ms {
		out = append(out, Model{
			ID:    string(m.ID),
			Alias: m.Alias,
			Caps: Caps{
				Reasoning:     m.Caps.Reasoning,
				CustomTools:   m.Caps.CustomTools,
				LocalShell:    m.Caps.LocalShell,
				ToolSearch:    m.Caps.ToolSearch,
				Compaction:    m.Caps.Compaction,
				CountTokens:   m.Caps.CountTokens,
				ParallelTools: m.Caps.ParallelTools,
			},
		})
	}
	slices.SortFunc(out, func(a, b Model) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	writeJSON(w, http.StatusOK, ModelsResponse{Models: out})
}

func (s *Server) providersList(w http.ResponseWriter, r *http.Request) {
	snap := s.cfg.Get()
	out := make([]Provider, 0, len(snap.Config.Providers))
	for _, id := range sortedKeys(snap.Config.Providers) {
		v, err := s.providerView(r.Context(), id, snap.Config.Providers[id])
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		out = append(out, v)
	}
	globalCtx := snap.Config.ContextWindow
	if globalCtx <= 0 {
		globalCtx = config.DefaultContextWindow
	}
	writeJSON(w, http.StatusOK, ProvidersResponse{Generation: snap.Generation, ContextWindow: globalCtx, VisionSidecar: snap.Config.VisionSidecar, Providers: out})
}

func (s *Server) providersCreate(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[ProviderWrite](w, r)
	if !ok {
		return
	}
	if body.ID == "" {
		writeError(w, http.StatusBadRequest, "invalid_document", "provider id required")
		return
	}
	snap := s.cfg.Get()
	if _, exists := snap.Config.Providers[body.ID]; exists {
		writeError(w, http.StatusConflict, "already_exists", "provider "+body.ID+" exists")
		return
	}
	s.applyProvider(w, r, body.ID, body, body.ExpectedGeneration)
}

func (s *Server) contextWindowPut(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[ContextWindowWrite](w, r)
	if !ok {
		return
	}
	if body.ContextWindow < 0 {
		writeError(w, http.StatusBadRequest, "invalid_value", "contextWindow must be >= 0")
		return
	}
	snap := s.cfg.Get()
	doc := snap.Config
	doc.ContextWindow = body.ContextWindow
	updated, err := s.cfg.Update(doc, body.ExpectedGeneration)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ContextWindowWrite{ContextWindow: updated.Config.ContextWindow, ExpectedGeneration: updated.Generation})
}

func (s *Server) visionSidecarPut(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[VisionSidecarWrite](w, r)
	if !ok {
		return
	}
	snap := s.cfg.Get()
	doc := snap.Config
	doc.VisionSidecar = config.VisionSidecarSettings{Enabled: body.Enabled, Target: body.Target}
	updated, err := s.cfg.Update(doc, body.ExpectedGeneration)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, VisionSidecarWrite{Enabled: updated.Config.VisionSidecar.Enabled, Target: updated.Config.VisionSidecar.Target, ExpectedGeneration: updated.Generation})
}

func (s *Server) providersReplace(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[ProviderWrite](w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	snap := s.cfg.Get()
	if _, exists := snap.Config.Providers[id]; !exists {
		writeError(w, http.StatusNotFound, "not_found", "provider "+id+" not found")
		return
	}
	s.applyProvider(w, r, id, body, body.ExpectedGeneration)
}

func (s *Server) applyProvider(w http.ResponseWriter, r *http.Request, id string, body ProviderWrite, expected uint64) {
	snap := s.cfg.Get()
	doc := snap.Config
	if doc.Providers == nil {
		doc.Providers = map[string]config.Provider{}
	}
	next := doc.Providers[id]
	if body.Wire != "" {
		next.Wire = config.Wire(body.Wire)
	}
	if body.BaseURL != nil {
		next.BaseURL = *body.BaseURL
	}
	if body.APIKeyRef != nil {
		next.APIKeyRef = *body.APIKeyRef
	}
	if body.DefaultModel != nil {
		next.DefaultModel = *body.DefaultModel
	}
	if body.Models != nil {
		next.Models = body.Models
	}
	if body.DisabledModels != nil {
		next.DisabledModels = body.DisabledModels
	}
	if body.SyncedModels != nil {
		next.SyncedModels = *body.SyncedModels
	}
	if body.ModelSettings != nil {
		next.ModelSettings = *body.ModelSettings
	}
	if body.Enabled != nil {
		next.Enabled = body.Enabled
	}
	if body.Pool != nil {
		next.Pool = body.Pool
	}
	doc.Providers[id] = next
	updated, err := s.cfg.Update(doc, expected)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	if body.Credential != "" {
		if err := s.creds.Put(r.Context(), id, []byte(body.Credential)); err != nil {
			writeError(w, http.StatusInternalServerError, "credential_store", err.Error())
			return
		}
	}
	v, err := s.providerView(r.Context(), id, updated.Config.Providers[id])
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ProviderMutationResponse{Generation: updated.Generation, Provider: v})
}

func (s *Server) providersDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	expected, ok := generationFromQuery(w, r)
	if !ok {
		return
	}
	snap := s.cfg.Get()
	if _, exists := snap.Config.Providers[id]; !exists {
		writeError(w, http.StatusNotFound, "not_found", "provider "+id+" not found")
		return
	}
	doc := snap.Config
	delete(doc.Providers, id)
	updated, err := s.cfg.Update(doc, expected)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	if err := s.creds.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "credential_store", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, GenerationResponse{Generation: updated.Generation})
}

// foldRawModelSettings migrates a synced antigravity provider's state off raw
// family member ids: settings keys and disabled entries naming a raw wire id
// (say gemini-3.7-flash-low) fold onto the logical id (gemini-3.7-flash) the
// sync now stores. Validation requires settings keys and disabled entries to
// name configured models, and a synced logical id displacing its raw members
// would strand them. Other wires never fold — their ids only look raw by
// coincidence, and the logical target would not be in their Models list. An
// existing logical-keyed settings entry wins over the folded raw one;
// disabled entries dedupe after folding. Ids the reviewed family table does
// not own stay untouched.
func foldRawModelSettings(doc *config.Document, id string) {
	p := doc.Providers[id]
	if p.Wire != config.WireAntigravity {
		return
	}
	if len(p.ModelSettings) == 0 && len(p.DisabledModels) == 0 {
		return
	}
	if len(p.ModelSettings) > 0 {
		settings := make(map[string]config.ModelSettings, len(p.ModelSettings))
		for model, s := range p.ModelSettings {
			if logical := antigravity.LogicalModel(model); logical != "" && logical != model {
				if _, exists := settings[logical]; !exists {
					settings[logical] = s
				}
				continue
			}
			settings[model] = s
		}
		p.ModelSettings = settings
	}
	if len(p.DisabledModels) > 0 {
		disabled := make([]string, 0, len(p.DisabledModels))
		for _, m := range p.DisabledModels {
			if logical := antigravity.LogicalModel(m); logical != "" && logical != m {
				m = logical
			}
			if !slices.Contains(disabled, m) {
				disabled = append(disabled, m)
			}
		}
		slices.Sort(disabled)
		p.DisabledModels = disabled
	}
	doc.Providers[id] = p
}

func (s *Server) providersSyncModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	expected, ok := generationFromQuery(w, r)
	if !ok {
		return
	}
	if s.syncer == nil {
		writeError(w, http.StatusNotImplemented, "unsupported", "model sync is not wired")
		return
	}
	snap := s.cfg.Get()
	p, exists := snap.Config.Providers[id]
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "provider "+id+" not found")
		return
	}
	remote, err := s.syncer.RemoteModels(r.Context(), id, p)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	doc := snap.Config
	next := doc.Providers[id]
	merged := make([]string, 0, len(next.Models)+len(remote))
	merged = append(merged, next.Models...)
	for _, m := range remote {
		if !slices.Contains(merged, m) {
			merged = append(merged, m)
		}
	}
	slices.Sort(merged)
	next.Models = merged
	next.SyncedModels = remote
	doc.Providers[id] = next
	foldRawModelSettings(&doc, id)
	updated, err := s.cfg.Update(doc, expected)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	v, err := s.providerView(r.Context(), id, updated.Config.Providers[id])
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ProviderMutationResponse{Generation: updated.Generation, Provider: v})
}

func (s *Server) providerView(ctx context.Context, id string, p config.Provider) (Provider, error) {
	state := "unset"
	set, err := s.creds.Configured(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if set {
		state = "set"
	}
	return Provider{
		ID:             id,
		Wire:           string(p.Wire),
		BaseURL:        p.BaseURL,
		DefaultModel:   p.DefaultModel,
		Models:         p.Models,
		DisabledModels: p.DisabledModels,
		SyncedModels:   p.SyncedModels,
		RawModels:      rawModelsForProvider(p),
		ModelEfforts:   modelEffortsForProvider(p),
		ModelSettings:  p.ModelSettings,
		Enabled:        p.Enabled,
		Pool:           p.Pool,
		Credential:     ProviderCredential{State: state},
	}, nil
}

// rawModelsForProvider flattens the raw wire ids behind a provider's logical
// models. Only the antigravity wire carries families; every other wire leaves
// the field empty so old clients see no change. The logical ids come from the
// stored Models list, so the raw list reflects config state rather than the
// last discovery alone.
func rawModelsForProvider(p config.Provider) []string {
	if p.Wire != config.WireAntigravity {
		return nil
	}
	var out []string
	for _, m := range p.Models {
		for _, raw := range antigravity.RawModels(m) {
			if !slices.Contains(out, raw) {
				out = append(out, raw)
			}
		}
	}
	slices.Sort(out)
	return out
}

// modelEffortsForProvider maps each logical family id on the provider onto
// its supported efforts, the rung ladders the desktop view renders as chips.
// Wire-gated like rawModelsForProvider; families resolve presence-free from
// the reviewed table, so no discovery is required for deterministic output.
// Raw member leftovers in Models fold onto their family id first, so the map
// carries one ladder per family rather than per-member duplicates.
func modelEffortsForProvider(p config.Provider) map[string][]string {
	if p.Wire != config.WireAntigravity {
		return nil
	}
	var out map[string][]string
	for _, m := range p.Models {
		logical := antigravity.LogicalModel(m)
		if logical == "" || out[logical] != nil {
			continue
		}
		efforts := antigravity.ModelEfforts(logical)
		if len(efforts) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string][]string, len(p.Models))
		}
		rungs := make([]string, 0, len(efforts))
		for _, e := range efforts {
			rungs = append(rungs, string(e))
		}
		out[logical] = rungs
	}
	return out
}

func (s *Server) accountsList(w http.ResponseWriter, r *http.Request) {
	snap := s.pool.Snapshot()
	out := make([]Account, 0, len(snap.Accounts))
	for _, a := range snap.Accounts {
		out = append(out, accountView(a))
	}
	slices.SortFunc(out, func(a, b Account) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	writeJSON(w, http.StatusOK, AccountsResponse{Accounts: out})
}

func (s *Server) accountPause(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[VersionWrite](w, r)
	if !ok {
		return
	}
	id := account.AccountID(r.PathValue("id"))
	if err := s.pool.Pause(r.Context(), id, account.StateVersion(body.Version)); err != nil {
		writeAccountError(w, err)
		return
	}
	s.writeAccountAfter(w, id)
}

func (s *Server) accountResume(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[VersionWrite](w, r)
	if !ok {
		return
	}
	id := account.AccountID(r.PathValue("id"))
	if err := s.pool.Resume(r.Context(), id, account.StateVersion(body.Version)); err != nil {
		writeAccountError(w, err)
		return
	}
	s.writeAccountAfter(w, id)
}

func (s *Server) accountPriority(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[PriorityWrite](w, r)
	if !ok {
		return
	}
	id := account.AccountID(r.PathValue("id"))
	if err := s.pool.UpdatePriority(r.Context(), id, body.Priority, account.StateVersion(body.Version)); err != nil {
		writeAccountError(w, err)
		return
	}
	s.writeAccountAfter(w, id)
}

func (s *Server) writeAccountAfter(w http.ResponseWriter, id account.AccountID) {
	snap := s.pool.Snapshot()
	for _, a := range snap.Accounts {
		if a.ID == id {
			writeJSON(w, http.StatusOK, accountView(a))
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "account missing after mutation")
}

func (s *Server) accountsDelete(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.pool.(AccountDeleter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported", "account deletion not supported by pool")
		return
	}
	id := account.AccountID(r.PathValue("id"))
	if err := ds.DeleteAccount(r.Context(), id); err != nil {
		writeAccountError(w, err)
		return
	}
	if s.store != nil {
		if err := s.store.DeleteDurable(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "durable_delete", err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) accountQuota(w http.ResponseWriter, r *http.Request) {
	id := account.AccountID(r.PathValue("id"))
	q, err := s.quota.Quota(r.Context(), id)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, QuotaResponse{Account: string(id), Quota: quotaView(q)})
}

func (s *Server) accountQuotaRefresh(w http.ResponseWriter, r *http.Request) {
	id := account.AccountID(r.PathValue("id"))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	q, err := s.quota.RefreshQuota(ctx, id)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "quota_refresh_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, QuotaResponse{Account: string(id), Quota: quotaView(q)})
}

func (s *Server) combosList(w http.ResponseWriter, r *http.Request) {
	snap := s.cfg.Get()
	out := make([]Combo, 0, len(snap.Config.Combos))
	for _, id := range sortedKeys(snap.Config.Combos) {
		out = append(out, comboView(id, snap.Config.Combos[id]))
	}
	writeJSON(w, http.StatusOK, CombosResponse{Generation: snap.Generation, Combos: out})
}

func (s *Server) combosPut(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[ComboWrite](w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	snap := s.cfg.Get()
	doc := snap.Config
	if doc.Combos == nil {
		doc.Combos = map[string]config.Combo{}
	}
	var imageInput bool
	if body.ImageInput != nil {
		imageInput = *body.ImageInput
	}
	doc.Combos[id] = config.Combo{
		Targets:     targetsFrom(body.Targets),
		Strategy:    config.ComboStrategy(body.Strategy),
		StickyLimit: body.StickyLimit,
		Alias:       body.Alias,
		NativeAlias: body.NativeAlias,
		DisplayName: body.DisplayName,
		ImageInput:  imageInput,
	}
	updated, err := s.cfg.Update(doc, body.ExpectedGeneration)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	out := make([]Combo, 0, len(updated.Config.Combos))
	for _, cid := range sortedKeys(updated.Config.Combos) {
		out = append(out, comboView(cid, updated.Config.Combos[cid]))
	}
	writeJSON(w, http.StatusOK, CombosResponse{Generation: updated.Generation, Combos: out})
}

func (s *Server) combosDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	expected, ok := generationFromQuery(w, r)
	if !ok {
		return
	}
	snap := s.cfg.Get()
	if _, exists := snap.Config.Combos[id]; !exists {
		writeError(w, http.StatusNotFound, "not_found", "combo "+id+" not found")
		return
	}
	doc := snap.Config
	delete(doc.Combos, id)
	updated, err := s.cfg.Update(doc, expected)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	out := make([]Combo, 0, len(updated.Config.Combos))
	for _, cid := range sortedKeys(updated.Config.Combos) {
		out = append(out, comboView(cid, updated.Config.Combos[cid]))
	}
	writeJSON(w, http.StatusOK, CombosResponse{Generation: updated.Generation, Combos: out})
}

func (s *Server) routesList(w http.ResponseWriter, r *http.Request) {
	snap := s.cfg.Get()
	routes := snap.Config.Routes
	if routes == nil {
		routes = map[string]string{}
	}
	writeJSON(w, http.StatusOK, RoutesResponse{Generation: snap.Generation, Routes: routes})
}

func (s *Server) routesPut(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[RouteWrite](w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	snap := s.cfg.Get()
	doc := snap.Config
	if doc.Routes == nil {
		doc.Routes = map[string]string{}
	}
	doc.Routes[key] = body.Value
	updated, err := s.cfg.Update(doc, body.ExpectedGeneration)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RoutesResponse{Generation: updated.Generation, Routes: updated.Config.Routes})
}

func (s *Server) routesDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	expected, ok := generationFromQuery(w, r)
	if !ok {
		return
	}
	snap := s.cfg.Get()
	if _, exists := snap.Config.Routes[key]; !exists {
		writeError(w, http.StatusNotFound, "not_found", "route "+key+" not found")
		return
	}
	doc := snap.Config
	delete(doc.Routes, key)
	updated, err := s.cfg.Update(doc, expected)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RoutesResponse{Generation: updated.Generation, Routes: updated.Config.Routes})
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	snap := s.pool.Snapshot()
	out := make([]UsageAccount, 0, len(snap.Accounts))
	for _, a := range snap.Accounts {
		out = append(out, UsageAccount{
			Account:  string(a.ID),
			Provider: string(a.Provider),
			State:    stateName(a.State),
			Email:    a.Email,
			Quota:    quotaView(a.Quota),
		})
	}
	slices.SortFunc(out, func(a, b UsageAccount) int {
		switch {
		case a.Account < b.Account:
			return -1
		case a.Account > b.Account:
			return 1
		}
		return 0
	})
	writeJSON(w, http.StatusOK, UsageResponse{Accounts: out})
}

func (s *Server) requests(w http.ResponseWriter, r *http.Request) {
	if s.reqlog == nil {
		writeError(w, http.StatusServiceUnavailable, "not_available", "request journal not configured")
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a non-negative integer")
			return
		}
		limit = n
	}
	entries := s.reqlog.Snapshot()
	views := make([]RequestView, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		views = append(views, requestView(entries[i]))
	}
	if limit > 0 && limit < len(views) {
		views = views[:limit]
	}
	writeJSON(w, http.StatusOK, RequestsResponse{Requests: views, Dropped: s.reqlog.Dropped()})
}

func (s *Server) resolveAuthProvider(raw string) account.ProviderID {
	switch raw {
	case "codex", "antigravity":
	default:
		return account.ProviderID(raw)
	}
	d := s.cfg.Get().Config
	match := ""
	for id, p := range d.Providers {
		var wire string
		switch p.Wire {
		case config.WireCodex:
			wire = "codex"
		case config.WireAntigravity:
			wire = "antigravity"
		}
		if wire == raw && match == "" {
			match = id
		}
	}
	if match == "" {
		return account.ProviderID(raw)
	}
	return account.ProviderID(match)
}

func (s *Server) authStart(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusNotImplemented, "unsupported", "auth not configured")
		return
	}
	start, err := s.auth.Start(r.Context(), s.resolveAuthProvider(r.PathValue("provider")))
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, AuthStartResponse{Session: string(start.Session), URL: start.URL})
}

func (s *Server) authCallback(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusNotImplemented, "unsupported", "auth not configured")
		return
	}
	body, ok := decodeJSON[AuthCallbackWrite](w, r)
	if !ok {
		return
	}
	cb := auth.AuthCallback{
		Session: auth.AuthSessionID(body.Session),
		Code:    body.Code,
		State:   body.State,
	}
	if err := s.auth.Complete(r.Context(), s.resolveAuthProvider(r.PathValue("provider")), cb); err != nil {
		writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusNotImplemented, "unsupported", "auth not configured")
		return
	}
	session := auth.AuthSessionID(r.URL.Query().Get("session"))
	st, err := s.auth.Status(r.Context(), s.resolveAuthProvider(r.PathValue("provider")), session)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, AuthStatusResponse{Provider: r.PathValue("provider"), State: st.State})
}

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUnknownProvider):
		writeError(w, http.StatusBadRequest, "unknown_provider", "unknown auth provider")
	case errors.Is(err, auth.ErrUnknownSession):
		writeError(w, http.StatusBadRequest, "unknown_session", "unknown or consumed auth session")
	case errors.Is(err, auth.ErrStateMismatch):
		writeError(w, http.StatusBadRequest, "state_mismatch", "callback state mismatch")
	case errors.Is(err, auth.ErrSessionExpired):
		writeError(w, http.StatusBadRequest, "session_expired", "auth session expired")
	case errors.Is(err, auth.ErrInvalidCallback):
		writeError(w, http.StatusBadRequest, "invalid_callback", "callback missing code")
	case errors.Is(err, auth.ErrLoopbackBind):
		writeError(w, http.StatusServiceUnavailable, "loopback_unavailable", "callback port is occupied or unavailable")
	case errors.Is(err, auth.ErrAuthFailed):
		writeError(w, http.StatusBadGateway, "auth_failed", "authentication failed")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func comboView(id string, c config.Combo) Combo {
	out := Combo{
		ID:          id,
		Strategy:    string(c.Strategy),
		StickyLimit: c.StickyLimit,
		Alias:       c.Alias,
		NativeAlias: c.NativeAlias,
		DisplayName: c.DisplayName,
	}
	if c.ImageInput {
		v := true
		out.ImageInput = &v
	}
	out.Targets = make([]Target, 0, len(c.Targets))
	for _, t := range c.Targets {
		out.Targets = append(out.Targets, Target{Provider: t.Provider, Model: t.Model, Weight: t.Weight})
	}
	return out
}

func targetsFrom(ts []Target) []config.Target {
	out := make([]config.Target, 0, len(ts))
	for _, t := range ts {
		out = append(out, config.Target{Provider: t.Provider, Model: t.Model, Weight: t.Weight})
	}
	return out
}

func accountView(a account.Account) Account {
	out := Account{
		ID:                   string(a.ID),
		Provider:             string(a.Provider),
		State:                stateName(a.State),
		Email:                a.Email,
		Priority:             a.Priority,
		Version:              uint64(a.Version),
		CredentialGeneration: uint64(a.CredGen),
		Quota:                quotaView(a.Quota),
		InFlight:             a.InFlight,
	}
	if !a.CooldownUntil.IsZero() {
		t := a.CooldownUntil
		out.CooldownUntil = &t
	}
	if !a.SoftAvoidUntil.IsZero() {
		t := a.SoftAvoidUntil
		out.SoftAvoidUntil = &t
	}
	return out
}

func quotaView(q quota.Snapshot) QuotaView {
	out := QuotaView{Used: q.Used, Limit: q.Limit, WindowEnd: q.WindowEnd, Source: sourceName(q.Source)}
	for _, w := range q.Windows {
		out.Windows = append(out.Windows, QuotaWindowView{Label: w.Label, Used: w.Used, Limit: w.Limit, WindowEnd: w.WindowEnd})
	}
	return out
}

func stateName(s account.State) string {
	switch s {
	case account.Active:
		return "active"
	case account.CoolingDown:
		return "cooling_down"
	case account.NeedsReauth:
		return "needs_reauth"
	case account.SoftAvoid:
		return "soft_avoid"
	case account.Paused:
		return "paused"
	}
	return "unknown"
}

func sourceName(s quota.Source) string {
	switch s {
	case quota.SourceHeader:
		return "header"
	case quota.SourceEndpoint:
		return "endpoint"
	case quota.SourceReport:
		return "report"
	case quota.SourceProbe:
		return "probe"
	}
	return "unknown"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func writeConfigError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, config.ErrStaleGeneration):
		writeError(w, http.StatusConflict, "stale_generation", err.Error())
	case errors.Is(err, config.ErrCorrupt),
		errors.Is(err, config.ErrUnknownWire),
		errors.Is(err, config.ErrUnknownComboStrategy),
		errors.Is(err, config.ErrUnknownPoolStrategy),
		errors.Is(err, config.ErrMalformedAlias),
		errors.Is(err, config.ErrInvalidTarget),
		errors.Is(err, config.ErrInvalidValue),
		errors.Is(err, config.ErrUnknownAffinity),
		errors.Is(err, config.ErrEmptyField):
		writeError(w, http.StatusBadRequest, "invalid_document", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

func writeAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, account.ErrStale):
		writeError(w, http.StatusConflict, "stale_version", err.Error())
	case errors.Is(err, account.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

func generationFromQuery(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	raw := r.URL.Query().Get("expectedGeneration")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "invalid_generation", "expectedGeneration query parameter required")
		return 0, false
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_generation", err.Error())
		return 0, false
	}
	return v, true
}

func decodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	err := json.NewDecoder(r.Body).Decode(&v)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
			return v, false
		}
		writeError(w, http.StatusBadRequest, "malformed_json", err.Error())
		return v, false
	}
	return v, true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
