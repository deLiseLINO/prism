package server

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/config"
	"prism/internal/execution"
	"prism/internal/provider"
)

type modelWire struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Object      string `json:"object"`
}

type modelsWire struct {
	Object string      `json:"object"`
	Data   []modelWire `json:"data"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	d := s.cfg.Get().Config
	ids := map[string]struct{}{}
	for k := range d.Routes {
		ids[k] = struct{}{}
	}
	for k := range d.Aliases {
		ids[k] = struct{}{}
	}
	for k := range d.Combos {
		ids[k] = struct{}{}
	}
	keys := make([]string, 0, len(ids))
	for k := range ids {
		if keyBlocked(d, k) {
			continue
		}
		keys = append(keys, k)
	}
	// Claude Code discovers routed models through this endpoint when gateway
	// model discovery is on: every enabled provider/model pair is listed under
	// its claude-prism alias so the native model picker accepts them.
	for providerName, p := range d.Providers {
		if !p.IsEnabled() {
			continue
		}
		for _, model := range p.Models {
			if slices.Contains(p.DisabledModels, model) {
				continue
			}
			alias := "claude-prism-" + providerName + "--" + model
			if keyBlocked(d, alias) {
				continue
			}
			if _, exists := ids[alias]; !exists {
				keys = append(keys, alias)
				ids[alias] = struct{}{}
			}
		}
	}
	sort.Strings(keys)
	data := make([]modelWire, 0, len(keys))
	for _, k := range keys {
		data = append(data, modelWire{Type: "model", ID: k, DisplayName: k, Object: "model"})
	}
	writeJSON(w, http.StatusOK, modelsWire{Object: "list", Data: data})
}

func keyBlocked(d config.Document, key string) bool {
	if v, ok := d.Routes[key]; ok {
		return routeValueBlocked(d, v)
	}
	if v, ok := d.Aliases[key]; ok {
		return routeValueBlocked(d, v)
	}
	if c, ok := d.Combos[key]; ok {
		return comboBlocked(d, c)
	}
	return false
}

func routeValueBlocked(d config.Document, v string) bool {
	if c, ok := d.Combos[v]; ok {
		return comboBlocked(d, c)
	}
	providerID, model, ok := strings.Cut(v, "/")
	if !ok {
		return false
	}
	return targetDisabled(d, providerID, model)
}

func comboBlocked(d config.Document, c config.Combo) bool {
	if len(c.Targets) == 0 {
		return false
	}
	for _, t := range c.Targets {
		if !targetDisabled(d, t.Provider, t.Model) {
			return false
		}
	}
	return true
}

type countTokensResponse struct {
	InputTokens int64 `json:"input_tokens"`
}

func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	req, facts, err := s.ingressMessages.Parse(r.Context(), r)
	if err != nil {
		s.writeParseError(w, protocolMessages, err)
		return
	}
	plan, ok := s.planner.Plan(req.Model)
	if !ok || len(plan.Targets) == 0 {
		writeJSON(w, http.StatusNotFound, messagesErrorEnvelope{Type: "error", Error: messagesErrorBody{
			Type:    "not_found_error",
			Message: fmt.Sprintf("no route for model %q", req.Model),
		}})
		return
	}
	target := plan.Targets[0]
	var count int64
	runner, found := s.registry.Lookup(target.Provider)
	if found {
		if tc, is := runner.(provider.TokenCounter); is {
			res, err := tc.CountTokens(r.Context(), provider.CountTokensRequest{
				Target:       target,
				Facts:        facts,
				Instructions: req.Instructions,
				Input:        req.Input,
				Tools:        req.Tools,
			})
			if err != nil {
				writeJSON(w, http.StatusBadGateway, messagesErrorEnvelope{Type: "error", Error: messagesErrorBody{
					Type:    "api_error",
					Message: err.Error(),
				}})
				return
			}
			count = res.InputTokens
		} else {
			count = estimateTokens(req)
		}
	} else {
		count = estimateTokens(req)
	}
	writeJSON(w, http.StatusOK, countTokensResponse{InputTokens: count})
}

func estimateTokens(req canon.Request) int64 {
	var total int64
	for _, c := range req.Instructions {
		if t, ok := c.(canon.TextContent); ok {
			total += textTokens(t.Text)
		}
	}
	for _, item := range req.Input {
		total += 4
		switch it := item.(type) {
		case canon.Message:
			total += contentTokens(it.Content)
		case canon.ReasoningItem:
			total += textTokens(it.Content)
			for _, sm := range it.Summary {
				total += textTokens(sm.Text)
			}
		case canon.FunctionCall:
			total += textTokens(string(it.Arguments)) + textTokens(string(it.Name))
		case canon.FunctionOutput:
			total += contentTokens(it.Output)
		case canon.CustomToolCall:
			total += textTokens(it.Input)
		case canon.CustomToolOutput:
			total += textTokens(it.Output)
		case canon.LocalShellCall:
			total += textTokens(it.Command)
		case canon.LocalShellOutput:
			total += textTokens(it.Output)
		case canon.ToolSearchCall:
			total += textTokens(it.Query)
		case canon.ToolSearchOutput:
			for _, res := range it.Results {
				total += textTokens(res.Summary)
			}
		}
	}
	for _, t := range req.Tools {
		switch tt := t.(type) {
		case canon.FunctionTool:
			total += textTokens(string(tt.Name)) + textTokens(tt.Description) + textTokens(string(tt.Parameters))
		case canon.CustomToolDef:
			total += textTokens(string(tt.Name)) + textTokens(tt.Description)
		case canon.ToolSearchToolDef:
			total += 8
		}
	}
	return total
}

func contentTokens(cs []canon.Content) int64 {
	var total int64
	for _, c := range cs {
		if t, ok := c.(canon.TextContent); ok {
			total += textTokens(t.Text)
		}
	}
	return total
}

func textTokens(s string) int64 {
	if s == "" {
		return 0
	}
	return int64(len(s)+3) / 4
}

type compactSummaryWire struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type compactUsageWire struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type compactResponse struct {
	ID      string             `json:"id"`
	Summary compactSummaryWire `json:"summary"`
	Usage   compactUsageWire   `json:"usage"`
}

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	req, facts, err := s.ingressResponses.Parse(r.Context(), r)
	if err != nil {
		s.writeParseError(w, protocolResponses, err)
		return
	}
	plan, ok := s.planner.Plan(req.Model)
	if !ok || len(plan.Targets) == 0 {
		writeJSON(w, http.StatusNotFound, errorEnvelope{Error: errorObject{
			Code:    "not_found",
			Message: fmt.Sprintf("no route for model %q", req.Model),
		}})
		return
	}
	target := plan.Targets[0]
	runner, found := s.registry.Lookup(target.Provider)
	if found {
		if comp, is := runner.(provider.Compactor); is {
			s.compactViaProvider(w, r, comp, target, facts, req)
			return
		}
	}
	result := localCompact(req.Input)
	writeCompact(w, http.StatusOK, result)
}

func (s *Server) compactViaProvider(w http.ResponseWriter, r *http.Request, comp provider.Compactor, target provider.Target, facts execution.Facts, req canon.Request) {
	lease, err := s.pool.Acquire(r.Context(), account.AcquireRequest{
		Provider:   target.Provider,
		Model:      target.Model,
		QuotaGroup: s.group,
		Session:    facts.Session,
		Thread:     facts.Thread,
		Policy:     target.Policy,
	})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope{Error: errorObject{
			Code:    "pool_exhausted",
			Message: err.Error(),
		}})
		return
	}
	result, err := comp.Compact(r.Context(), provider.CompactRequest{
		Target: target,
		Lease:  lease,
		Facts:  facts,
		Input:  req.Input,
	})
	if err != nil {
		_ = s.pool.Record(r.Context(), lease, account.ServerError{})
		writeJSON(w, http.StatusBadGateway, errorEnvelope{Error: errorObject{
			Code:    "compact_failed",
			Message: err.Error(),
		}})
		return
	}
	_ = s.pool.Record(r.Context(), lease, account.TurnSucceeded{Usage: result.Usage})
	writeCompact(w, http.StatusOK, result)
}

func writeCompact(w http.ResponseWriter, status int, result provider.CompactResult) {
	writeJSON(w, status, compactResponse{
		ID: newRequestID(),
		Summary: compactSummaryWire{
			Role:    "assistant",
			Content: summaryText(result.Summary),
		},
		Usage: usageWireOf(result.Usage),
	})
}

func localCompact(input []canon.Item) provider.CompactResult {
	summary := canon.Message{
		ID:      "msg_local_compact",
		Role:    canon.RoleAssistant,
		Content: []canon.Content{canon.TextContent{Text: fmt.Sprintf("compacted %d items", len(input))}},
	}
	return provider.CompactResult{Summary: summary}
}

func summaryText(m canon.Message) string {
	var out string
	for _, c := range m.Content {
		if t, ok := c.(canon.TextContent); ok {
			if out != "" {
				out += "\n"
			}
			out += t.Text
		}
	}
	return out
}

func usageWireOf(u canon.Usage) compactUsageWire {
	return compactUsageWire{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.TotalTokens,
	}
}
