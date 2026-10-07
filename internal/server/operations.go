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
	"time"
	"unicode/utf8"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/routing"
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
	if plan, ok := s.planner.Plan(counted.model); !ok || len(plan.Targets) == 0 {
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
	facts.RequestID = execution.RequestID(newRequestID())
	start := s.clock.Now()
	sink := &summarySink{}
	turnReq := req
	turnReq.Stream = false
	turnReq.Tools = nil
	turnReq.ToolChoice = nil
	turnReq.Text = canon.TextOutput{}
	turnReq.Input = compactionTurnInput(req.Input)
	router := routing.NewRouter(s.pool, operationRunners{registry: s.registry, input: req.Input, output: &sink.output}, s.planner, s.group, s.rlog)
	res := router.Turn(r.Context(), turnReq, facts, notStartedLifecycle{}, sink)
	s.recordUsage(facts, req, protocolResponses, res, start)
	if failed, ok := res.Terminal.(routing.Failed); ok {
		if res.RetryAfter > 0 {
			seconds := int64(res.RetryAfter / time.Second)
			if res.RetryAfter%time.Second != 0 {
				seconds++
			}
			w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		}
		writeJSON(w, failureStatus(failed.Event.Failure.Reason), errorEnvelope{Error: errorObject{Code: "compact_failed", Message: failed.Event.Failure.Message}})
		return
	}
	finished, ok := res.Terminal.(routing.Finished)
	if !ok || finished.Event.Status.Kind() != canon.StatusCompleted {
		writeJSON(w, http.StatusBadGateway, errorEnvelope{Error: errorObject{Code: "compact_failed", Message: "compaction turn did not complete"}})
		return
	}
	writeCompact(w, http.StatusOK, req.Input, provider.CompactResult{Summary: canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: sink.assistantText()}}}, Output: sink.output, Usage: finished.Event.Usage})
}

type operationRunners struct {
	registry *provider.Registry
	input    []canon.Item
	output   *[]json.RawMessage
}

func (r operationRunners) Lookup(id account.ProviderID) (provider.Runner, bool) {
	runner, ok := r.registry.Lookup(id)
	if !ok {
		return nil, false
	}
	return compactionRunner{inner: runner, input: r.input, output: r.output}, true
}

type compactionRunner struct {
	inner  provider.Runner
	input  []canon.Item
	output *[]json.RawMessage
}

func (r compactionRunner) Run(ctx context.Context, req provider.RunRequest, sink provider.Sink) error {
	attempt := &summarySink{progress: req.Progress}
	var err error
	var output []json.RawMessage
	if comp, ok := r.inner.(provider.Compactor); ok {
		input := r.input
		if !req.Target.ImageInput {
			input = withoutImages(input)
		}
		result, compactErr := comp.Compact(ctx, provider.CompactRequest{Target: req.Target, Lease: req.Lease, Facts: req.Facts, Instructions: req.Request.Instructions, Input: input, AttemptObserver: req.AttemptObserver, CredentialObserver: req.CredentialObserver})
		err = compactErr
		if err == nil {
			output = result.Output
			if len(output) == 0 {
				if emitErr := attempt.Emit(canon.ItemFinished{Item: canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: summaryText(result.Summary)}}}}); emitErr != nil {
					return emitErr
				}
			}
			if emitErr := attempt.Emit(canon.TurnFinished{Status: canon.Completed(), Usage: result.Usage}); emitErr != nil {
				return emitErr
			}
		}
	} else {
		err = r.inner.Run(ctx, req, attempt)
	}
	if failed, ok := attempt.terminal.(canon.TurnFailed); ok {
		if emitErr := sink.Emit(failed); emitErr != nil {
			return emitErr
		}
		return err
	}
	if err != nil {
		return err
	}
	finished, ok := attempt.terminal.(canon.TurnFinished)
	if !ok {
		return nil
	}
	if finished.Status.Kind() == canon.StatusCompleted {
		text := attempt.assistantText()
		if len(output) == 0 && strings.TrimSpace(text) == "" {
			failure := canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailServerOverloaded, Message: "compaction turn produced no summary text"}, Usage: finished.Usage}
			if err := sink.Emit(failure); err != nil {
				return err
			}
			return provider.RunError{Kind: provider.TerminalEmitted, Class: provider.ClassServer, Accepted: true, ReplaySafe: true, Cause: errors.New(failure.Failure.Message)}
		}
		if len(output) > 0 {
			*r.output = output
		} else if err := sink.Emit(canon.ItemFinished{Item: canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: text}}}}); err != nil {
			return err
		}
	}
	return sink.Emit(finished)
}

// compactionTurnInput prepares the summarizer turn's input: images become a
// text marker (a summary needs no pixels and text-only gateways reject
// them) and the compaction prompt lands as the final user message.
func compactionTurnInput(input []canon.Item) []canon.Item {
	return append(withoutImages(input), canon.Message{
		Role:    canon.RoleUser,
		Content: []canon.Content{canon.TextContent{Text: compactPrompt}},
	})
}

func withoutImages(input []canon.Item) []canon.Item {
	strip := func(parts []canon.Content) []canon.Content {
		content := make([]canon.Content, len(parts))
		for i, c := range parts {
			if _, isImage := c.(canon.ImageContent); isImage {
				content[i] = canon.TextContent{Text: "[image omitted for compaction]"}
			} else {
				content[i] = c
			}
		}
		return content
	}
	out := make([]canon.Item, len(input))
	for i, item := range input {
		switch v := item.(type) {
		case canon.Message:
			v.Content = strip(v.Content)
			out[i] = v
		case canon.FunctionOutput:
			v.Output = strip(v.Output)
			out[i] = v
		case canon.CustomToolOutput:
			if v.Content != nil {
				v.Content = strip(v.Content)
			}
			out[i] = v
		default:
			out[i] = item
		}
	}
	return out
}

type summarySink struct {
	mu       sync.Mutex
	parts    []string
	terminal canon.Event
	progress provider.Progress
	output   []json.RawMessage
}

func (s *summarySink) Emit(ev canon.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal != nil {
		return errors.New("compaction: event after terminal")
	}
	provider.Mark(s.progress)
	switch ev.(type) {
	case canon.TurnFinished, canon.TurnFailed:
		s.terminal = ev
	}
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
	if len(result.Output) > 0 {
		writeJSON(w, status, struct {
			Output []json.RawMessage `json:"output"`
		}{Output: result.Output})
		return
	}
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
