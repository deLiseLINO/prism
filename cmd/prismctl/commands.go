package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"prism/internal/config"
	"prism/internal/management"
)

// run executes the parsed command against the injected runtime. Handlers
// return typed errors; nothing here calls os.Exit.
func (c *command) run(ctx context.Context, rt *cliRuntime) error {
	p := printer{w: rt.stdout, json: c.json}
	switch c.verb {
	case "help":
		return p.text(helpTop)
	case "status":
		return c.runStatus(ctx, rt, p)
	case "doctor":
		return c.runDoctor(ctx, rt, p)
	case "auth-login":
		return c.runAuthLogin(ctx, rt, p)
	case "auth-status":
		return c.runAuthStatus(ctx, rt, p)
	case "accounts-list":
		return c.runAccountsList(ctx, rt, p)
	case "accounts-pause":
		return c.runAccountAction(ctx, rt, p, "pause")
	case "accounts-resume":
		return c.runAccountAction(ctx, rt, p, "resume")
	case "accounts-priority":
		return c.runAccountPriority(ctx, rt, p)
	case "accounts-quota":
		return c.runAccountQuota(ctx, rt, p)
	case "accounts-remove":
		return c.runAccountRemove(ctx, rt, p)
	case "accounts-select":
		return c.runAccountsSelect(ctx, rt, p)
	case "accounts-auto-switch":
		return c.runAccountsAutoSwitch(ctx, rt, p)
	case "accounts-distribute":
		return c.runAccountsDistribute(ctx, rt, p)
	case "accounts-affinity":
		return c.runAccountsAffinity(ctx, rt, p)
	case "providers-list":
		return c.runProvidersList(ctx, rt, p)
	case "providers-add", "providers-edit":
		return c.runProviderWrite(ctx, rt, p)
	case "providers-enable", "providers-disable":
		return c.runProviderEnable(ctx, rt, p)
	case "providers-remove":
		return c.runProviderRemove(ctx, rt, p)
	case "models-list":
		return c.runModelsList(ctx, rt, p)
	case "models-enable", "models-disable":
		return c.runModelsToggle(ctx, rt, p)
	case "combos-list":
		return c.runCombosList(ctx, rt, p)
	case "combos-set":
		return c.runCombosSet(ctx, rt, p)
	case "combos-remove":
		return c.runCombosRemove(ctx, rt, p)
	case "routes-list":
		return c.runRoutesList(ctx, rt, p)
	case "routes-set":
		return c.runRoutesSet(ctx, rt, p)
	case "routes-remove":
		return c.runRoutesRemove(ctx, rt, p)
	case "integrations-status":
		return c.runIntegrationsStatus(ctx, rt, p)
	case "integrations-apply":
		return c.runIntegrationsAction(ctx, rt, p, "apply")
	case "integrations-rollback":
		return c.runIntegrationsAction(ctx, rt, p, "rollback")
	case "usage":
		return c.runUsage(ctx, rt, p)
	case "stats":
		return c.runStats(ctx, rt, p)
	}
	return fmt.Errorf("internal: unhandled verb %q", c.verb)
}

func (c *command) runStatus(ctx context.Context, rt *cliRuntime, p printer) error {
	h, err := rt.client.health(ctx)
	if err != nil {
		return err
	}
	accounts, aerr := rt.client.accountsList(ctx)
	providers, perr := rt.client.providersList(ctx)
	if aerr != nil {
		return aerr
	}
	if perr != nil {
		return perr
	}
	if c.json {
		return p.printJSON(map[string]any{
			"health":     h,
			"providers":  len(providers.Providers),
			"accounts":   len(accounts.Accounts),
			"generation": providers.Generation,
		})
	}
	return p.kv([][2]string{
		{"status", h.Status},
		{"providers", fmt.Sprint(len(providers.Providers))},
		{"accounts", fmt.Sprint(len(accounts.Accounts))},
		{"generation", fmt.Sprint(providers.Generation)},
	})
}

func (c *command) runDoctor(ctx context.Context, rt *cliRuntime, p printer) error {
	h, err := rt.client.health(ctx)
	if err != nil {
		return err
	}
	models, merr := rt.client.models(ctx)
	if merr != nil {
		return merr
	}
	providers, perr := rt.client.providersList(ctx)
	if perr != nil {
		return perr
	}
	var problems []string
	for _, pr := range providers.Providers {
		switch pr.Wire {
		case "responses", "chat":
			if pr.BaseURL == "" {
				problems = append(problems, fmt.Sprintf("provider %s: wire %s requires an endpoint", pr.ID, pr.Wire))
			}
		}
		if pr.Credential.State == "unset" && pr.Wire != "responses" && pr.Wire != "chat" && pr.Wire != "messages" {
			problems = append(problems, fmt.Sprintf("provider %s: credential unset", pr.ID))
		}
	}
	if c.json {
		return p.printJSON(map[string]any{
			"daemon":   h.Status,
			"version":  prismctlVersion(),
			"models":   len(models.Models),
			"problems": problems,
		})
	}
	var lines []string
	lines = append(lines, "daemon: "+h.Status)
	lines = append(lines, "cli version: "+prismctlVersion())
	lines = append(lines, fmt.Sprintf("models: %d", len(models.Models)))
	if len(problems) == 0 {
		lines = append(lines, "config: ok")
	} else {
		lines = append(lines, fmt.Sprintf("config: %d problem(s)", len(problems)))
		for _, pr := range problems {
			lines = append(lines, "  - "+pr)
		}
	}
	return p.text(lines...)
}

func prismctlVersion() string { return "0.1.0" }

// --- auth ---

const authPollInterval = time.Second / 2

func (c *command) runAuthLogin(ctx context.Context, rt *cliRuntime, p printer) error {
	start, err := rt.client.authStart(ctx, c.provider)
	if err != nil {
		return err
	}
	if c.authNoOpen {
		if err := p.text("open this URL to continue login:", start.URL); err != nil {
			return err
		}
	} else {
		if err := openBrowser(ctx, rt, start.URL); err != nil {
			// Failure to open the browser is recoverable: print the URL
			// so the login can still complete.
			if err := p.text("could not open a browser; open this URL manually:", start.URL); err != nil {
				return err
			}
		}
	}
	return c.pollAuth(ctx, rt, p, start.Session)
}

// pollAuth polls the session status until terminal, cancellable via ctx.
// Only the opaque session ID and the sanitized state ever appear in output.
func (c *command) pollAuth(ctx context.Context, rt *cliRuntime, p printer, session string) error {
	if !c.json {
		if err := p.text("waiting for login to complete (Ctrl-C to cancel)..."); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(authPollInterval)
	defer ticker.Stop()
	for {
		st, err := rt.client.authStatus(ctx, c.provider, session)
		if err != nil {
			return err
		}
		switch st.State {
		case "authorized", "complete":
			return p.kv([][2]string{
				{"provider", c.provider},
				{"state", st.State},
			})
		case "failed":
			return exitErr(exitFailure, "login failed for %s", c.provider)
		case "expired":
			return exitErr(exitFailure, "login session expired for %s; re-run prismctl auth login", c.provider)
		}
		select {
		case <-ctx.Done():
			return exitErr(exitFailure, "login polling cancelled")
		case <-ticker.C:
		}
	}
}

func (c *command) runAuthStatus(ctx context.Context, rt *cliRuntime, p printer) error {
	if c.authSession != "" {
		st, err := rt.client.authStatus(ctx, c.provider, c.authSession)
		if err != nil {
			return err
		}
		return p.authStatus(st)
	}
	// No session: report provider-level status for both providers.
	codex, err1 := rt.client.authStatus(ctx, "codex", "")
	anti, err2 := rt.client.authStatus(ctx, "antigravity", "")
	if err1 != nil && err2 != nil {
		return err1
	}
	if c.json {
		return p.printJSON([]management.AuthStatusResponse{codex, anti})
	}
	var lines []string
	if err1 == nil {
		lines = append(lines, "codex: "+codex.State)
	} else {
		lines = append(lines, "codex: "+err1.Error())
	}
	if err2 == nil {
		lines = append(lines, "antigravity: "+anti.State)
	} else {
		lines = append(lines, "antigravity: "+err2.Error())
	}
	return p.text(lines...)
}

// openBrowser launches the system browser. The platform command is chosen
// without consulting the environment; only the URL crosses the boundary.
func openBrowser(ctx context.Context, rt *cliRuntime, url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// --- accounts ---

func (c *command) runAccountsList(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.accountsList(ctx)
	if err != nil {
		return err
	}
	return p.accounts(list)
}

func (c *command) runAccountAction(ctx context.Context, rt *cliRuntime, p printer, action string) error {
	// Version comes from the last observed snapshot; requiring it keeps the
	// daemon's CAS semantics visible at the CLI surface.
	if c.version == 0 {
		list, err := rt.client.accountsList(ctx)
		if err != nil {
			return err
		}
		for _, a := range list.Accounts {
			if a.ID == c.account {
				c.version = a.Version
			}
		}
		if c.version == 0 {
			return exitErr(exitFailure, "account %s not found", c.account)
		}
	}
	var out management.Account
	body := management.VersionWrite{Version: c.version}
	if err := rt.client.accountAction(ctx, c.account, action, body, &out); err != nil {
		return err
	}
	return p.accountOne(out)
}

func (c *command) runAccountPriority(ctx context.Context, rt *cliRuntime, p printer) error {
	if c.version == 0 {
		list, err := rt.client.accountsList(ctx)
		if err != nil {
			return err
		}
		for _, a := range list.Accounts {
			if a.ID == c.account {
				c.version = a.Version
			}
		}
		if c.version == 0 {
			return exitErr(exitFailure, "account %s not found", c.account)
		}
	}
	var out management.Account
	body := management.PriorityWrite{Version: c.version, Priority: c.priority}
	if err := rt.client.accountAction(ctx, c.account, "priority", body, &out); err != nil {
		return err
	}
	return p.accountOne(out)
}

func (c *command) runAccountQuota(ctx context.Context, rt *cliRuntime, p printer) error {
	q, err := rt.client.accountQuota(ctx, c.account)
	if err != nil {
		return err
	}
	return p.quota(q)
}

func (c *command) runAccountRemove(ctx context.Context, rt *cliRuntime, p printer) error {
	if err := rt.client.accountsDelete(ctx, c.account); err != nil {
		return err
	}
	if c.json {
		return p.printJSON(map[string]string{"removed": c.account})
	}
	return p.text("account removed: " + c.account)
}

// --- account policy (select / auto-switch / distribute / affinity) ---
// Pool policy fields live on the provider's PoolSettings; the CLI reads the
// current provider, mutates only the requested field, and writes the whole
// pool back under the generation CAS — absent fields are preserved by
// sending the full observed pool.

func (c *command) providerPool(ctx context.Context, rt *cliRuntime) (management.ProvidersResponse, management.Provider, error) {
	list, err := rt.client.providersList(ctx)
	if err != nil {
		return list, management.Provider{}, err
	}
	for _, pr := range list.Providers {
		if pr.ID == c.id {
			return list, pr, nil
		}
	}
	return list, management.Provider{}, exitErr(exitFailure, "provider %s not found", c.id)
}

// writePool mutates one field of the provider's pool and PUTs the provider
// back with every observed field present, so the daemon's absent=preserve
// merge keeps unrelated fields intact.
func (c *command) writePool(ctx context.Context, rt *cliRuntime, mutate func(pool *config.PoolSettings)) error {
	list, pr, err := c.providerPool(ctx, rt)
	if err != nil {
		return err
	}
	pool := config.PoolSettings{
		Strategy:        config.PoolQuota,
		Affinity:        config.AffinitySticky,
		MaxFailovers:    3,
		CooldownDefault: 300_000_000_000,
		CooldownMax:     900_000_000_000,
		ProbeEvery:      60_000_000_000,
	}
	if pr.Pool != nil {
		pool = *pr.Pool
	}
	mutate(&pool)
	w := providerWriteFrom(pr)
	w.Pool = &pool
	w.ExpectedGeneration = list.Generation
	_, err = rt.client.providersReplace(ctx, pr.ID, w)
	return err
}

// providerWriteFrom copies every observed provider field into a write body so
// a policy mutation cannot erase models, credentials state, or other fields.
// Credential bytes are never copied — only the credential file/stdin paths.
func providerWriteFrom(pr management.Provider) management.ProviderWrite {
	w := management.ProviderWrite{
		ID:             pr.ID,
		Wire:           pr.Wire,
		Models:         pr.Models,
		DisabledModels: pr.DisabledModels,
		Enabled:        pr.Enabled,
	}
	if pr.BaseURL != "" {
		w.BaseURL = &pr.BaseURL
	}
	if pr.DefaultModel != "" {
		w.DefaultModel = &pr.DefaultModel
	}
	return w
}

func (c *command) runAccountsSelect(ctx context.Context, rt *cliRuntime, p printer) error {
	pin := c.selectValue
	if pin != "auto" {
		// Validate the account belongs to the provider before pinning.
		list, err := rt.client.accountsList(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, a := range list.Accounts {
			if a.ID == pin {
				found = true
				if a.Provider != c.id {
					return exitErr(exitFailure, "account %s belongs to provider %s, not %s", pin, a.Provider, c.id)
				}
			}
		}
		if !found {
			return exitErr(exitFailure, "account %s not found", pin)
		}
	} else {
		pin = ""
	}
	err := c.writePool(ctx, rt, func(pool *config.PoolSettings) {
		pool.PinnedAccount = pin
	})
	if err != nil {
		return err
	}
	if c.json {
		return p.printJSON(map[string]string{"provider": c.id, "pinnedAccount": pin})
	}
	if pin == "" {
		return p.text("provider " + c.id + ": pin cleared (auto selection)")
	}
	return p.text("provider " + c.id + ": pinned to " + pin)
}

func (c *command) runAccountsAutoSwitch(ctx context.Context, rt *cliRuntime, p printer) error {
	err := c.writePool(ctx, rt, func(pool *config.PoolSettings) {
		v := c.autoSwitch
		pool.AutoSwitch = &v
		if c.thresholdSet {
			pool.AutoSwitchThreshold = c.threshold
		}
	})
	if err != nil {
		return err
	}
	state := "off"
	if c.autoSwitch {
		state = "on"
	}
	return p.kv([][2]string{
		{"provider", c.id},
		{"autoSwitch", state},
		{"threshold", fmt.Sprintf("%.4g", c.threshold)},
	})
}

func (c *command) runAccountsDistribute(ctx context.Context, rt *cliRuntime, p printer) error {
	err := c.writePool(ctx, rt, func(pool *config.PoolSettings) {
		pool.Strategy = config.PoolStrategy(c.strategy)
	})
	if err != nil {
		return err
	}
	return p.kv([][2]string{
		{"provider", c.id},
		{"strategy", c.strategy},
	})
}

func (c *command) runAccountsAffinity(ctx context.Context, rt *cliRuntime, p printer) error {
	err := c.writePool(ctx, rt, func(pool *config.PoolSettings) {
		pool.Affinity = config.PoolAffinity(c.affinity)
	})
	if err != nil {
		return err
	}
	return p.kv([][2]string{
		{"provider", c.id},
		{"affinity", c.affinity},
	})
}

// --- providers ---

func (c *command) runProvidersList(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.providersList(ctx)
	if err != nil {
		return err
	}
	return p.providers(list)
}

func (c *command) runProviderWrite(ctx context.Context, rt *cliRuntime, p printer) error {
	// Credential bytes are read once here, from a file or stdin, and sent in
	// the request body; they never enter argv and are never printed.
	credential := ""
	if c.credFile != "" {
		b, err := os.ReadFile(c.credFile) // #nosec G304 -- user-supplied path by design
		if err != nil {
			return exitErr(exitFailure, "read credential file: %v", err)
		}
		credential = strings.TrimSpace(string(b))
		if credential == "" {
			return exitErr(exitFailure, "credential file is empty")
		}
	} else if c.credStdin {
		b, err := readAllStdin()
		if err != nil {
			return exitErr(exitFailure, "read credential from stdin: %v", err)
		}
		credential = strings.TrimSpace(string(b))
		if credential == "" {
			return exitErr(exitFailure, "credential input on stdin is empty")
		}
	}
	w := management.ProviderWrite{
		ID:                 c.id,
		Wire:               c.wire,
		Models:             c.models,
		Credential:         credential,
		ExpectedGeneration: 0,
	}
	if c.baseURL != "" {
		w.BaseURL = &c.baseURL
	}
	if c.defaultModel != "" {
		w.DefaultModel = &c.defaultModel
	}
	var resp management.ProviderMutationResponse
	var err error
	if c.verb == "providers-add" {
		// add must not race an existing document: read generation first.
		list, lerr := rt.client.providersList(ctx)
		if lerr != nil {
			return lerr
		}
		for _, pr := range list.Providers {
			if pr.ID == c.id {
				return exitErr(exitFailure, "provider %s already exists", c.id)
			}
		}
		w.ExpectedGeneration = list.Generation
		resp, err = rt.client.providersCreate(ctx, w)
	} else {
		list, lerr := rt.client.providersList(ctx)
		if lerr != nil {
			return lerr
		}
		found := false
		for _, pr := range list.Providers {
			if pr.ID == c.id {
				found = true
			}
		}
		if !found {
			return exitErr(exitFailure, "provider %s not found", c.id)
		}
		w.ExpectedGeneration = list.Generation
		resp, err = rt.client.providersReplace(ctx, c.id, w)
	}
	if err != nil {
		return err
	}
	return p.providerOne(resp)
}

func readAllStdin() (string, error) {
	b, err := io.ReadAll(os.Stdin)
	return string(b), err
}

func (c *command) runProviderEnable(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.providersList(ctx)
	if err != nil {
		return err
	}
	var pr management.Provider
	found := false
	for _, cand := range list.Providers {
		if cand.ID == c.id {
			pr = cand
			found = true
		}
	}
	if !found {
		return exitErr(exitFailure, "provider %s not found", c.id)
	}
	w := providerWriteFrom(pr)
	v := c.enable
	w.Enabled = &v
	w.ExpectedGeneration = list.Generation
	resp, err := rt.client.providersReplace(ctx, c.id, w)
	if err != nil {
		return err
	}
	return p.providerOne(resp)
}

func (c *command) runProviderRemove(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.providersList(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, pr := range list.Providers {
		if pr.ID == c.id {
			found = true
		}
	}
	if !found {
		return exitErr(exitFailure, "provider %s not found", c.id)
	}
	resp, err := rt.client.providersDelete(ctx, c.id, list.Generation)
	if err != nil {
		return err
	}
	return p.generation(resp, "provider removed: "+c.id)
}

// --- models ---

func (c *command) runModelsList(ctx context.Context, rt *cliRuntime, p printer) error {
	models, err := rt.client.models(ctx)
	if err != nil {
		return err
	}
	return p.models(models)
}

// runModelsToggle flips one model's enablement on its provider by editing
// the DisabledModels list. Absent models are preserved; enabling a model
// removes it from the disabled list, disabling appends it.
func (c *command) runModelsToggle(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.providersList(ctx)
	if err != nil {
		return err
	}
	var pr management.Provider
	found := false
	for _, cand := range list.Providers {
		if cand.ID == c.id {
			pr = cand
			found = true
		}
	}
	if !found {
		return exitErr(exitFailure, "provider %s not found", c.id)
	}
	if len(pr.Models) > 0 && !containsString(pr.Models, c.modelID) {
		return exitErr(exitFailure, "provider %s has no model %s", c.id, c.modelID)
	}
	disabled := pr.DisabledModels
	if c.enable {
		disabled = removeString(disabled, c.modelID)
	} else {
		if !containsString(disabled, c.modelID) {
			disabled = append(disabled, c.modelID)
		}
	}
	w := providerWriteFrom(pr)
	w.DisabledModels = disabled
	w.ExpectedGeneration = list.Generation
	resp, err := rt.client.providersReplace(ctx, c.id, w)
	if err != nil {
		return err
	}
	return p.providerOne(resp)
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func removeString(xs []string, s string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// --- combos ---

func (c *command) runCombosList(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.combosList(ctx)
	if err != nil {
		return err
	}
	return p.combos(list)
}

func (c *command) runCombosSet(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.combosList(ctx)
	if err != nil {
		return err
	}
	w := management.ComboWrite{
		Targets:     c.targets,
		Strategy:    c.strategy,
		StickyLimit: 0,
		Alias:       c.alias,
	}
	if c.affinity != "" {
		n, err := parseSticky(c.affinity)
		if err != nil {
			return err
		}
		w.StickyLimit = n
	}
	w.ExpectedGeneration = list.Generation
	resp, err := rt.client.combosPut(ctx, c.comboID, w)
	if err != nil {
		return err
	}
	if c.json {
		return p.printJSON(resp)
	}
	return p.text("combo saved: " + c.comboID + " (generation " + fmt.Sprint(resp.Generation) + ")")
}

func parseSticky(s string) (int, error) {
	n := 0
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 0 {
		return 0, exitErr(exitUsage, "invalid sticky-limit %q", s)
	}
	return n, nil
}

func (c *command) runCombosRemove(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.combosList(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, cb := range list.Combos {
		if cb.ID == c.comboID {
			found = true
		}
	}
	if !found {
		return exitErr(exitFailure, "combo %s not found", c.comboID)
	}
	resp, err := rt.client.combosDelete(ctx, c.comboID, list.Generation)
	if err != nil {
		return err
	}
	if c.json {
		return p.printJSON(resp)
	}
	return p.text("combo removed: " + c.comboID + " (generation " + fmt.Sprint(resp.Generation) + ")")
}

// --- routes ---

func (c *command) runRoutesList(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.routesList(ctx)
	if err != nil {
		return err
	}
	return p.routes(list)
}

func (c *command) runRoutesSet(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.routesList(ctx)
	if err != nil {
		return err
	}
	w := management.RouteWrite{Value: c.value, ExpectedGeneration: list.Generation}
	resp, err := rt.client.routesPut(ctx, c.routeKey, w)
	if err != nil {
		return err
	}
	return p.routes(resp)
}

func (c *command) runRoutesRemove(ctx context.Context, rt *cliRuntime, p printer) error {
	list, err := rt.client.routesList(ctx)
	if err != nil {
		return err
	}
	if _, ok := list.Routes[c.routeKey]; !ok {
		return exitErr(exitFailure, "route %s not found", c.routeKey)
	}
	resp, err := rt.client.routesDelete(ctx, c.routeKey, list.Generation)
	if err != nil {
		return err
	}
	return p.routes(resp)
}

// --- integrations ---

func (c *command) runIntegrationsStatus(ctx context.Context, rt *cliRuntime, p printer) error {
	if c.clientID != "" {
		st, err := rt.client.integrationGet(ctx, c.clientID)
		if err != nil {
			return err
		}
		return p.integrationOne(st)
	}
	list, err := rt.client.integrationsList(ctx)
	if err != nil {
		return err
	}
	return p.integrationsList(list.Integrations)
}

func (c *command) runIntegrationsAction(ctx context.Context, rt *cliRuntime, p printer, action string) error {
	var res integrationsApplyJSON
	var err error
	if action == "apply" {
		res, err = rt.client.integrationApply(ctx, c.clientID, c.force)
	} else {
		res, err = rt.client.integrationRollback(ctx, c.clientID)
	}
	if err != nil {
		return err
	}
	if !res.OK {
		// Refusal reason surfaces verbatim; typed exit code. A retryable
		// conflict names the confirmed re-run so scripts can branch on it.
		if perr := p.integrationApply(res, action); perr != nil {
			return perr
		}
		if action == "apply" && res.Retryable && !c.force {
			return exitErr(exitRefused, "%s refused: %s (re-run with --force to take over after confirmation)", action, res.Reason)
		}
		return exitErr(exitRefused, "%s refused: %s", action, res.Reason)
	}
	return p.integrationApply(res, action)
}

// --- usage ---

func (c *command) runUsage(ctx context.Context, rt *cliRuntime, p printer) error {
	u, err := rt.client.usage(ctx)
	if err != nil {
		return err
	}
	return p.usage(u)
}

// --- stats ---

func (c *command) runStats(ctx context.Context, rt *cliRuntime, p printer) error {
	resp, err := rt.client.stats(ctx, c.statsRange)
	if err != nil {
		return err
	}
	return p.stats(resp)
}
