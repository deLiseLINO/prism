package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/config"
	"prism/internal/execution"
	"prism/internal/provider"
	"prism/internal/routing"
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
	// its claude alias so the native model picker accepts them.
	for providerName, p := range d.Providers {
		if !p.IsEnabled() {
			continue
		}
		for _, model := range p.Models {
			if slices.Contains(p.DisabledModels, model) {
				continue
			}
			alias := "claude-" + providerName + "--" + model
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
	counted, err := parseCountTokensBody(r)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorEnvelope{Error: errorObject{
				Code:    "request_too_large",
				Message: "request body exceeds " + strconv.FormatInt(mbe.Limit, 10) + " bytes",
			}})
			return
		}
		writeJSON(w, http.StatusBadRequest, messagesErrorEnvelope{Type: "error", Error: messagesErrorBody{
			Type:    "invalid_request_error",
			Message: err.Error(),
		}})
		return
	}
	if _, ok := s.planner.Plan(counted.model); !ok {
		writeJSON(w, http.StatusNotFound, messagesErrorEnvelope{Type: "error", Error: messagesErrorBody{
			Type:    "not_found_error",
			Message: fmt.Sprintf("no route for model %q", counted.model),
		}})
		return
	}
	writeJSON(w, http.StatusOK, countTokensResponse{InputTokens: counted.tokens})
}

type countedBody struct {
	model  canon.ModelID
	tokens int64
}

// parseCountTokensBody applies the Anthropic count_tokens contract, not the
// /v1/messages one: only a non-empty model is required, and messages, system,
// and tools ride along unvalidated (a count_tokens body never carries
// max_tokens, which the messages ingress would reject).
func parseCountTokensBody(r *http.Request) (countedBody, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return countedBody{}, fmt.Errorf("count_tokens: reading body: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return countedBody{}, errors.New("count_tokens: request body must be a JSON object")
	}
	var model string
	if raw, ok := root["model"]; ok {
		_ = json.Unmarshal(raw, &model)
	}
	if model == "" {
		return countedBody{}, errors.New("count_tokens: model is required")
	}
	return countedBody{model: canon.ModelID(model), tokens: estimateCountTokens(root)}, nil
}

// estimateCountTokens charges token counting the way count_tokens semantics
// expect: system, messages, and tools are joined as text and charged ~4
// chars/token, while base64 attachment payloads are blanked before that
// charge and counted through a bounded per-attachment estimate instead. A
// 2MB screenshot is ~2.7M base64 chars; as raw text that reports hundreds of
// thousands of tokens against a real cost around 1.6k. tool_use input and
// tool schemas can legitimately hold attachment-shaped JSON, so only
// protocol content positions (message blocks and tool_result blocks) are
// sanitized.
func estimateCountTokens(root map[string]json.RawMessage) int64 {
	var attachments int64
	var parts []string
	if raw, ok := root["system"]; ok && string(raw) != "null" {
		var sys any
		if err := json.Unmarshal(raw, &sys); err == nil {
			if s, isStr := sys.(string); isStr {
				parts = append(parts, s)
			} else if sys != nil {
				if b, err := json.Marshal(sys); err == nil {
					parts = append(parts, string(b))
				}
			}
		}
	}
	if raw, ok := root["messages"]; ok && string(raw) != "null" {
		var messages any
		if err := json.Unmarshal(raw, &messages); err == nil {
			messages = blankAttachments(messages, &attachments)
			if b, err := json.Marshal(messages); err == nil {
				parts = append(parts, string(b))
			}
		}
	}
	if raw, ok := root["tools"]; ok && string(raw) != "null" {
		parts = append(parts, string(raw))
	}
	total := textTokens(strings.Join(parts, "\n")) + attachments
	if total < 1 {
		total = 1
	}
	return total
}

func blankAttachments(v any, attachments *int64) any {
	msgs, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, len(msgs))
	for i, msg := range msgs {
		rec, ok := msg.(map[string]any)
		if !ok {
			out[i] = msg
			continue
		}
		blocks, ok := rec["content"].([]any)
		if !ok {
			out[i] = msg
			continue
		}
		next := cloneMap(rec)
		next["content"] = blankAttachmentBlocks(blocks, attachments)
		out[i] = next
	}
	return out
}

func blankAttachmentBlocks(blocks []any, attachments *int64) []any {
	out := make([]any, len(blocks))
	for i, b := range blocks {
		out[i] = blankAttachmentBlock(b, attachments)
	}
	return out
}

func blankAttachmentBlock(b any, attachments *int64) any {
	rec, ok := b.(map[string]any)
	if !ok {
		return b
	}
	switch rec["type"] {
	case "image", "document":
		src, ok := rec["source"].(map[string]any)
		if !ok || src["type"] != "base64" {
			return b
		}
		data, ok := src["data"].(string)
		if !ok {
			return b
		}
		*attachments += base64AttachmentTokens(data)
		next := cloneMap(rec)
		source := cloneMap(src)
		source["data"] = ""
		next["source"] = source
		return next
	case "tool_result":
		blocks, ok := rec["content"].([]any)
		if !ok {
			return b
		}
		next := cloneMap(rec)
		next["content"] = blankAttachmentBlocks(blocks, attachments)
		return next
	}
	return b
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// base64AttachmentTokens charges one attachment by its decoded byte size at
// ~512 bytes per token, floored at 256.
func base64AttachmentTokens(data string) int64 {
	unpadded := len(data)
	if strings.HasSuffix(data, "==") {
		unpadded -= 2
	} else if strings.HasSuffix(data, "=") {
		unpadded -= 1
	}
	tokens := (int64(unpadded)*3/4 + 511) / 512
	if tokens < 256 {
		tokens = 256
	}
	return tokens
}

func textTokens(s string) int64 {
	if s == "" {
		return 0
	}
	return int64(len(s)+3) / 4
}

const compactPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, and user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.`

const summaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"

// compactRetainedCharBudget mirrors codex-rs COMPACT_USER_MESSAGE_MAX_TOKENS
// of 20k tokens at ~4 chars/token.
const compactRetainedCharBudget = 20_000 * 4

type compactOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type compactOutputMessage struct {
	Type    string                 `json:"type"`
	Role    string                 `json:"role"`
	Content []compactOutputContent `json:"content"`
}

// compactResponse matches the codex CompactHistoryResponse contract: the
// client deserializes only {output: [ResponseItem]} and installs it as the
// replacement history. Items carry no id: the client replays them verbatim
// on later turns and treats minted ids as modified history.
type compactResponse struct {
	Output []compactOutputMessage `json:"output"`
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
	if runner, found := s.registry.Lookup(target.Provider); found {
		if comp, is := runner.(provider.Compactor); is {
			s.compactViaProvider(w, r, comp, target, facts, req)
			return
		}
	}
	s.compactViaTurn(w, r, target, facts, req)
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
		Target:       target,
		Lease:        lease,
		Facts:        facts,
		Instructions: req.Instructions,
		Input:        req.Input,
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
	writeCompact(w, http.StatusOK, req.Input, result)
}

// compactViaTurn produces a real summary for providers without a Compactor:
// a tool-free model turn dispatched through the standard run machinery with
// the compaction prompt as the final user message, never fabricated history.
func (s *Server) compactViaTurn(w http.ResponseWriter, r *http.Request, target provider.Target, facts execution.Facts, req canon.Request) {
	summary, err := s.compactSummaryTurn(r.Context(), target, facts, req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorEnvelope{Error: errorObject{
			Code:    "compact_failed",
			Message: err.Error(),
		}})
		return
	}
	writeCompact(w, http.StatusOK, req.Input, provider.CompactResult{Summary: summary})
}

func (s *Server) compactSummaryTurn(ctx context.Context, target provider.Target, facts execution.Facts, req canon.Request) (canon.Message, error) {
	turnReq := req
	turnReq.Stream = false
	turnReq.Tools = nil
	turnReq.ToolChoice = nil
	turnReq.Text = canon.TextOutput{}
	turnReq.Input = compactionTurnInput(req.Input)
	sink := &summarySink{}
	res := s.router.Turn(ctx, turnReq, facts, notStartedLifecycle{}, sink)
	if failed, ok := res.Terminal.(routing.Failed); ok {
		return canon.Message{}, fmt.Errorf("provider %q compaction turn failed: %s", target.Provider, failed.Event.Failure.Message)
	}
	finished, ok := res.Terminal.(routing.Finished)
	if !ok || finished.Event.Status.Kind() != canon.StatusCompleted {
		return canon.Message{}, fmt.Errorf("provider %q compaction turn did not complete", target.Provider)
	}
	text := sink.assistantText()
	if strings.TrimSpace(text) == "" {
		return canon.Message{}, fmt.Errorf("provider %q compaction turn produced no summary text", target.Provider)
	}
	return canon.Message{
		Role:    canon.RoleUser,
		Content: []canon.Content{canon.TextContent{Text: text}},
	}, nil
}

// compactionTurnInput prepares the summarizer turn's input: images become a
// text marker (a summary needs no pixels and text-only gateways reject
// them) and the compaction prompt lands as the final user message.
func compactionTurnInput(input []canon.Item) []canon.Item {
	out := make([]canon.Item, 0, len(input)+1)
	for _, item := range input {
		m, ok := item.(canon.Message)
		if !ok {
			out = append(out, item)
			continue
		}
		content := make([]canon.Content, len(m.Content))
		for i, c := range m.Content {
			if _, isImage := c.(canon.ImageContent); isImage {
				content[i] = canon.TextContent{Text: "[image omitted for compaction]"}
				continue
			}
			content[i] = c
		}
		out = append(out, canon.Message{ID: m.ID, Role: m.Role, Content: content})
	}
	return append(out, canon.Message{
		Role:    canon.RoleUser,
		Content: []canon.Content{canon.TextContent{Text: compactPrompt}},
	})
}

type summarySink struct {
	mu    sync.Mutex
	parts []string
}

func (s *summarySink) Emit(ev canon.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if finished, ok := ev.(canon.ItemFinished); ok {
		m, ok := finished.Item.(canon.Message)
		if !ok || m.Role != canon.RoleAssistant {
			return nil
		}
		for _, c := range m.Content {
			if t, ok := c.(canon.TextContent); ok {
				s.parts = append(s.parts, t.Text)
			}
		}
	}
	return nil
}

func (s *summarySink) assistantText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.parts, "")
}

type notStartedLifecycle struct{}

func (notStartedLifecycle) CommitState() provider.CommitState { return provider.NotStarted }

func writeCompact(w http.ResponseWriter, status int, input []canon.Item, result provider.CompactResult) {
	writeJSON(w, status, compactResponse{Output: compactOutputItems(input, summaryText(result.Summary))})
}

// compactOutputItems mirrors codex-rs build_compacted_history: recent
// plain-text user messages within the retention budget, then one user
// message carrying the summary behind the prefix codex detects on replay.
func compactOutputItems(input []canon.Item, summary string) []compactOutputMessage {
	texts := retainedUserTexts(input)
	items := make([]compactOutputMessage, 0, len(texts)+1)
	for _, text := range texts {
		items = append(items, compactUserMessageItem(text))
	}
	if strings.TrimSpace(summary) == "" {
		summary = "(no summary available)"
	} else {
		summary = summaryPrefix + "\n" + summary
	}
	return append(items, compactUserMessageItem(summary))
}

func compactUserMessageItem(text string) compactOutputMessage {
	return compactOutputMessage{
		Type:    "message",
		Role:    "user",
		Content: []compactOutputContent{{Type: "input_text", Text: text}},
	}
}

func retainedUserTexts(input []canon.Item) []string {
	var texts []string
	for _, item := range input {
		m, ok := item.(canon.Message)
		if !ok || m.Role != canon.RoleUser {
			continue
		}
		var text strings.Builder
		for _, c := range m.Content {
			if t, ok := c.(canon.TextContent); ok {
				text.WriteString(t.Text)
			}
		}
		if s := text.String(); strings.TrimSpace(s) != "" {
			texts = append(texts, s)
		}
	}
	selected := make([]string, 0, len(texts))
	remaining := compactRetainedCharBudget
	for i := len(texts) - 1; i >= 0 && remaining > 0; i-- {
		msg := texts[i]
		if len(msg) <= remaining {
			selected = append(selected, msg)
			remaining -= len(msg)
			continue
		}
		// The budget partially covers this older message: keep its tail and
		// stop. The tail must not start mid-rune or it would be invalid UTF-8.
		start := len(msg) - remaining
		for start > 0 && !utf8.RuneStart(msg[start]) {
			start--
		}
		selected = append(selected, msg[start:])
		break
	}
	slices.Reverse(selected)
	return selected
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
