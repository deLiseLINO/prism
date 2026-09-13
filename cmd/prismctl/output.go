package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"prism/internal/agentinstall"
	"prism/internal/integrations"
	"prism/internal/management"
)

// integrationsStatusJSON and integrationsApplyJSON mirror the exact JSON
// envelopes the daemon emits for integration routes. The daemon types are in
// internal/integrations (Status) and internal/management (IntegrationsResponse
// wraps them); these aliases reuse those structs so the wire shape stays
// single-sourced. ApplyResult already carries json tags.
type integrationsStatusJSON = integrations.Status
type integrationsApplyJSON = integrations.ApplyResult

// agentStatusJSON and agentJobJSON reuse the daemon's agent install types so
// the wire shape stays single-sourced; both already carry json tags.
type agentStatusJSON = agentinstall.AgentStatus
type agentJobJSON = management.AgentJobResponse

// printer renders one command outcome either as a stable human table for an
// 80-column terminal or as machine-stable JSON containing exactly the
// management-domain fields. Subcommands write through these helpers only.
type printer struct {
	w     io.Writer
	json  bool
	clock func() int64
}

func (p printer) printJSON(v any) error {
	enc := json.NewEncoder(p.w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeText prints pre-rendered lines for simple outputs.
func (p printer) text(lines ...string) error {
	for _, l := range lines {
		if _, err := fmt.Fprintln(p.w, l); err != nil {
			return err
		}
	}
	return nil
}

// table renders rows with aligned columns, truncating the last visible column
// so the total width stays within 80 columns.
func (p printer) table(headers []string, rows [][]string) error {
	if len(headers) == 0 {
		return nil
	}
	if p.json {
		objs := make([]map[string]string, 0, len(rows))
		for _, r := range rows {
			m := map[string]string{}
			for i, h := range headers {
				if i < len(r) {
					m[h] = r[i]
				} else {
					m[h] = ""
				}
			}
			objs = append(objs, m)
		}
		return p.printJSON(map[string]any{"rows": objs})
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i := range widths {
			if i < len(r) && len(r[i]) > widths[i] {
				widths[i] = len(r[i])
			}
		}
	}
	line := func(cells []string) string {
		var b strings.Builder
		for i := range widths {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			b.WriteString(c)
			if i < len(widths)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len(c)+2))
			}
		}
		return strings.TrimRight(b.String(), " ")
	}
	var out []string
	out = append(out, line(headers))
	for _, r := range rows {
		out = append(out, line(r))
	}
	return p.text(out...)
}

// kv renders "key: value" lines; the JSON form emits an object.
func (p printer) kv(pairs [][2]string) error {
	if p.json {
		m := map[string]string{}
		for _, kv := range pairs {
			m[kv[0]] = kv[1]
		}
		return p.printJSON(m)
	}
	var lines []string
	for _, kv := range pairs {
		lines = append(lines, kv[0]+": "+kv[1])
	}
	return p.text(lines...)
}

// --- per-domain renderers ---

func (p printer) health(h management.HealthResponse) error {
	return p.kv([][2]string{{"status", h.Status}})
}

func (p printer) models(m management.ModelsResponse) error {
	if p.json {
		return p.printJSON(m)
	}
	headers := []string{"ID", "ALIAS", "REASONING", "TOOLS", "PARALLEL"}
	rows := make([][]string, 0, len(m.Models))
	for _, mod := range m.Models {
		rows = append(rows, []string{
			mod.ID,
			mod.Alias,
			boolText(mod.Caps.Reasoning),
			boolText(mod.Caps.CustomTools),
			boolText(mod.Caps.ParallelTools),
		})
	}
	return p.table(headers, rows)
}

func (p printer) providers(resp management.ProvidersResponse) error {
	if p.json {
		return p.printJSON(resp)
	}
	headers := []string{"ID", "WIRE", "ENABLED", "MODELS", "CRED", "BASE"}
	rows := make([][]string, 0, len(resp.Providers))
	for _, pr := range resp.Providers {
		enabled := "yes"
		if pr.Enabled != nil && !*pr.Enabled {
			enabled = "no"
		}
		rows = append(rows, []string{
			pr.ID,
			pr.Wire,
			enabled,
			fmt.Sprintf("%d", len(pr.Models)),
			pr.Credential.State,
			pr.BaseURL,
		})
	}
	if err := p.table(headers, rows); err != nil {
		return err
	}
	return p.text("generation: " + fmt.Sprint(resp.Generation))
}

func (p printer) providerOne(resp management.ProviderMutationResponse) error {
	if p.json {
		return p.printJSON(resp)
	}
	return p.text("provider saved: " + resp.Provider.ID + " (generation " + fmt.Sprint(resp.Generation) + ")")
}

func (p printer) generation(resp management.GenerationResponse, what string) error {
	if p.json {
		return p.printJSON(resp)
	}
	return p.text(what + " (generation " + fmt.Sprint(resp.Generation) + ")")
}

func (p printer) accounts(resp management.AccountsResponse) error {
	if p.json {
		return p.printJSON(resp)
	}
	headers := []string{"ID", "PROVIDER", "STATE", "PRIO", "USED", "LIMIT", "WINDOW"}
	rows := make([][]string, 0, len(resp.Accounts))
	for _, a := range resp.Accounts {
		limit := "-"
		if a.Quota.Limit != nil {
			limit = fmt.Sprint(*a.Quota.Limit)
		}
		rows = append(rows, []string{
			a.ID,
			a.Provider,
			a.State,
			fmt.Sprint(a.Priority),
			fmt.Sprint(a.Quota.Used),
			limit,
			a.Quota.WindowEnd.Format(time.RFC3339),
		})
	}
	return p.table(headers, rows)
}

func (p printer) accountOne(a management.Account) error {
	if p.json {
		return p.printJSON(a)
	}
	limit := "-"
	if a.Quota.Limit != nil {
		limit = fmt.Sprint(*a.Quota.Limit)
	}
	return p.kv([][2]string{
		{"id", a.ID},
		{"provider", a.Provider},
		{"state", a.State},
		{"priority", fmt.Sprint(a.Priority)},
		{"version", fmt.Sprint(a.Version)},
		{"used", fmt.Sprint(a.Quota.Used)},
		{"limit", limit},
	})
}

func (p printer) quota(q management.QuotaResponse) error {
	if p.json {
		return p.printJSON(q)
	}
	limit := "-"
	if q.Quota.Limit != nil {
		limit = fmt.Sprint(*q.Quota.Limit)
	}
	return p.kv([][2]string{
		{"account", q.Account},
		{"used", fmt.Sprint(q.Quota.Used)},
		{"limit", limit},
		{"windowEnd", q.Quota.WindowEnd.Format(time.RFC3339)},
		{"source", q.Quota.Source},
	})
}

func (p printer) combos(resp management.CombosResponse) error {
	if p.json {
		return p.printJSON(resp)
	}
	headers := []string{"ID", "TARGETS", "STRATEGY", "STICKY", "ALIAS"}
	rows := make([][]string, 0, len(resp.Combos))
	for _, c := range resp.Combos {
		targets := make([]string, 0, len(c.Targets))
		for _, t := range c.Targets {
			if t.Weight > 0 {
				targets = append(targets, fmt.Sprintf("%s/%s:%d", t.Provider, t.Model, t.Weight))
			} else {
				targets = append(targets, t.Provider+"/"+t.Model)
			}
		}
		rows = append(rows, []string{
			c.ID,
			strings.Join(targets, ","),
			c.Strategy,
			fmt.Sprint(c.StickyLimit),
			c.Alias,
		})
	}
	if err := p.table(headers, rows); err != nil {
		return err
	}
	return p.text("generation: " + fmt.Sprint(resp.Generation))
}

func (p printer) routes(resp management.RoutesResponse) error {
	if p.json {
		return p.printJSON(resp)
	}
	headers := []string{"KEY", "TARGET"}
	keys := make([]string, 0, len(resp.Routes))
	for k := range resp.Routes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([][]string, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, []string{k, resp.Routes[k]})
	}
	if err := p.table(headers, rows); err != nil {
		return err
	}
	return p.text("generation: " + fmt.Sprint(resp.Generation))
}

func (p printer) usage(u management.UsageResponse) error {
	if p.json {
		return p.printJSON(u)
	}
	headers := []string{"ACCOUNT", "PROVIDER", "STATE", "USED", "LIMIT", "WINDOW", "SOURCE"}
	rows := make([][]string, 0, len(u.Accounts))
	for _, a := range u.Accounts {
		limit := "-"
		if a.Quota.Limit != nil {
			limit = fmt.Sprint(*a.Quota.Limit)
		}
		rows = append(rows, []string{
			a.Account,
			a.Provider,
			a.State,
			fmt.Sprint(a.Quota.Used),
			limit,
			a.Quota.WindowEnd.Format(time.RFC3339),
			a.Quota.Source,
		})
	}
	return p.table(headers, rows)
}

func (p printer) stats(resp management.StatsResponse) error {
	if p.json {
		return p.printJSON(resp)
	}
	o := resp.Overview
	if err := p.text("range: " + resp.Range); err != nil {
		return err
	}
	if err := p.kv([][2]string{
		{"requests", fmt.Sprint(o.Requests)},
		{"completed", fmt.Sprint(o.Completed)},
		{"failed", fmt.Sprint(o.Failed)},
		{"tokens in", fmt.Sprint(o.InputTokens)},
		{"tokens out", fmt.Sprint(o.OutputTokens)},
		{"tokens cached", fmt.Sprint(o.CachedTokens)},
		{"tokens total", fmt.Sprint(o.TotalTokens)},
		{"measured", fmt.Sprintf("%d/%d", o.Measured, o.Requests)},
	}); err != nil {
		return err
	}
	if err := p.text("", "providers:"); err != nil {
		return err
	}
	provHeaders := []string{"PROVIDER", "REQUESTS", "COMPLETED", "FAILED", "IN", "OUT", "CACHED", "TOTAL"}
	provRows := make([][]string, 0, len(resp.Providers))
	for _, pr := range resp.Providers {
		provRows = append(provRows, statsRow(pr.Provider, pr.StatsOverview))
	}
	if err := p.table(provHeaders, provRows); err != nil {
		return err
	}
	if err := p.text("", "models (top 10):"); err != nil {
		return err
	}
	modelHeaders := []string{"MODEL", "PROVIDER", "REQUESTS", "COMPLETED", "FAILED", "IN", "OUT", "CACHED", "TOTAL"}
	limit := len(resp.Models)
	if limit > 10 {
		limit = 10
	}
	modelRows := make([][]string, 0, limit)
	for _, m := range resp.Models[:limit] {
		modelRows = append(modelRows, statsRow(m.Model, m.StatsOverview))
	}
	if err := p.table(modelHeaders, modelRows); err != nil {
		return err
	}
	if len(resp.Models) > 10 {
		return p.text(fmt.Sprintf("(%d more models omitted)", len(resp.Models)-10))
	}
	return nil
}

func statsRow(name string, o management.StatsOverview) []string {
	return []string{
		name,
		fmt.Sprint(o.Requests),
		fmt.Sprint(o.Completed),
		fmt.Sprint(o.Failed),
		fmt.Sprint(o.InputTokens),
		fmt.Sprint(o.OutputTokens),
		fmt.Sprint(o.CachedTokens),
		fmt.Sprint(o.TotalTokens),
	}
}

func (p printer) authStatus(st management.AuthStatusResponse) error {
	if p.json {
		return p.printJSON(st)
	}
	return p.kv([][2]string{
		{"provider", st.Provider},
		{"state", st.State},
	})
}

func (p printer) integrationsList(list []integrations.Status) error {
	if p.json {
		return p.printJSON(management.IntegrationsResponse{Integrations: list})
	}
	headers := []string{"CLIENT", "INSTALLED", "MANAGED", "DRIFT", "TARGET"}
	rows := make([][]string, 0, len(list))
	for _, s := range list {
		target := "-"
		if s.TargetPath != nil {
			target = *s.TargetPath
		}
		rows = append(rows, []string{
			string(s.ID),
			boolText(s.Installed),
			boolText(s.Managed),
			boolText(s.Drift),
			target,
		})
	}
	return p.table(headers, rows)
}

func (p printer) integrationOne(s integrations.Status) error {
	if p.json {
		return p.printJSON(s)
	}
	target := "-"
	if s.TargetPath != nil {
		target = *s.TargetPath
	}
	endpoint := "-"
	if s.Endpoint != nil {
		endpoint = *s.Endpoint
	}
	return p.kv([][2]string{
		{"client", string(s.ID)},
		{"installed", boolText(s.Installed)},
		{"managed", boolText(s.Managed)},
		{"targetPath", target},
		{"endpoint", endpoint},
		{"drift", boolText(s.Drift)},
		{"detail", s.Detail},
	})
}

func (p printer) integrationApply(res integrations.ApplyResult, action string) error {
	if p.json {
		return p.printJSON(res)
	}
	if !res.OK {
		// The refusal is printed verbatim; the typed exit happens at the
		// command layer so --json still gets the structured body. Flow-style,
		// tab, and duplicate refusals carry a hint because the fix is a
		// one-line edit the user can make themselves.
		if res.Retryable {
			return p.text(action + " refused (retryable): " + res.Reason)
		}
		return p.text(action + " refused: " + res.Reason + refusalHint(res.Reason))
	}
	return p.text(action + " ok: " + string(res.ID))
}

// refusalHint appends a plain-language fix for the refusal reasons a user can
// repair by hand; unknown reasons get no hint rather than a guess.
func refusalHint(reason string) string {
	switch {
	case strings.Contains(reason, "flow-style value, not a block map"):
		return "\nhint: rewrite the providers line in block style — put `providers:` alone on its line and each provider under it as `  name:` — then run the command again"
	case strings.Contains(reason, "tab indentation"):
		return "\nhint: models.yml indents with tabs; convert the indentation to spaces, then run the command again"
	case strings.Contains(reason, "duplicate"):
		return "\nhint: the file declares the same key twice; remove the duplicate entry, then run the command again"
	}
	return ""
}

func (p printer) agentsList(list []agentinstall.AgentStatus) error {
	if p.json {
		return p.printJSON(management.AgentsResponse{Agents: list})
	}
	headers := []string{"ID", "KEY", "INSTALLED", "SOURCE", "CAN-UPDATE"}
	rows := make([][]string, 0, len(list))
	for _, a := range list {
		source := string(a.Source)
		if source == "" {
			source = "-"
		}
		rows = append(rows, []string{
			string(a.ID),
			a.Key,
			boolText(a.Installed),
			source,
			boolText(a.CanUpdate),
		})
	}
	return p.table(headers, rows)
}

func (p printer) agentOne(a agentinstall.AgentStatus) error {
	if p.json {
		return p.printJSON(a)
	}
	lines := [][2]string{
		{"id", string(a.ID)},
		{"key", a.Key},
		{"installed", boolText(a.Installed)},
		{"source", string(a.Source)},
		{"path", a.Path},
		{"canUpdate", boolText(a.CanUpdate)},
		{"jobState", string(a.Job.State)},
	}
	if a.Reason != "" {
		lines = append(lines, [2]string{"reason", a.Reason})
	}
	return p.kv(lines)
}

func (p printer) agentJob(res agentJobJSON, kind string) error {
	if p.json {
		return p.printJSON(res)
	}
	job := res.Job
	line := kind + " " + job.Key + ": " + string(job.State)
	if job.Command != "" {
		line += " (" + job.Command + ")"
	}
	if job.Error != "" {
		line += " — " + job.Error
	}
	return p.text(line)
}

func boolText(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
