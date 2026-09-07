package responses

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"prism/internal/canon"
	"prism/internal/execution"
)

var _ interface {
	Parse(context.Context, *http.Request) (canon.Request, execution.Facts, error)
} = (*Ingress)(nil)

type Reason uint8

const (
	ReasonInvalidJSON Reason = iota + 1
	ReasonMissingField
	ReasonInvalidField
)

func (r Reason) String() string {
	switch r {
	case ReasonInvalidJSON:
		return "invalid_json"
	case ReasonMissingField:
		return "missing_field"
	case ReasonInvalidField:
		return "invalid_field"
	default:
		return "unknown"
	}
}

type ParseError struct {
	Status int
	Reason Reason
	Field  string
	Err    error
}

func (e *ParseError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("responses ingress: %s: %s: %v", e.Field, e.Reason, e.Err)
	}
	return fmt.Sprintf("responses ingress: %s: %s", e.Field, e.Reason)
}

func (e *ParseError) Unwrap() error { return e.Err }

const (
	WarnExoticItem    = "exotic_item"
	WarnUnknownItem   = "unknown_item"
	WarnOpaquePayload = "opaque_payload"
	WarnUnmappedField = "unmapped_field"
)

type Warning struct {
	Kind   string
	Detail string
}

type Ingress struct {
	warn func(Warning)
}

func New(onWarning func(Warning)) *Ingress {
	if onWarning == nil {
		panic("responses ingress: nil warning sink")
	}
	return &Ingress{warn: onWarning}
}

func (g *Ingress) warnOf(kind, detail string) {
	g.warn(Warning{Kind: kind, Detail: detail})
}

type EffortCaps struct {
	Subagent     string
	TurnMetadata string
}

func (g *Ingress) EffortCaps(hr *http.Request) (EffortCaps, bool) {
	caps := EffortCaps{
		Subagent:     strings.TrimSpace(hr.Header.Get("x-openai-subagent")),
		TurnMetadata: strings.TrimSpace(hr.Header.Get("x-codex-turn-metadata")),
	}
	return caps, caps.Subagent != "" || caps.TurnMetadata != ""
}

func (g *Ingress) Parse(ctx context.Context, hr *http.Request) (canon.Request, execution.Facts, error) {
	if hr.Method != http.MethodPost {
		return canon.Request{}, execution.Facts{}, &ParseError{
			Status: http.StatusMethodNotAllowed,
			Reason: ReasonInvalidField,
			Field:  "method",
		}
	}
	body, err := io.ReadAll(hr.Body)
	if err != nil {
		return canon.Request{}, execution.Facts{}, &ParseError{
			Status: http.StatusBadRequest,
			Reason: ReasonInvalidJSON,
			Field:  "body",
			Err:    err,
		}
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return canon.Request{}, execution.Facts{}, &ParseError{
			Status: http.StatusBadRequest,
			Reason: ReasonInvalidJSON,
			Field:  "body",
			Err:    err,
		}
	}
	req, err := g.requestFrom(root)
	if err != nil {
		return canon.Request{}, execution.Facts{}, err
	}
	facts, err := factsFrom(hr)
	if err != nil {
		return canon.Request{}, execution.Facts{}, err
	}
	return req, facts, nil
}

func (g *Ingress) requestFrom(root map[string]any) (canon.Request, error) {
	var req canon.Request
	model, ok := root["model"].(string)
	if !ok || model == "" {
		return req, &ParseError{Status: http.StatusBadRequest, Reason: ReasonMissingField, Field: "model"}
	}
	req.Model = canon.ModelID(model)
	if s, ok := root["stream"].(bool); ok {
		req.Stream = s
	}
	if ins, ok := root["instructions"].(string); ok && ins != "" {
		req.Instructions = []canon.Content{canon.TextContent{Text: ins}}
	} else if _, present := root["instructions"]; present && root["instructions"] != nil {
		if _, isStr := root["instructions"].(string); !isStr {
			return req, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "instructions"}
		}
	}
	if max, ok := root["max_output_tokens"].(float64); ok && max > 0 {
		req.MaxOutputTokens = int(max)
	}
	g.textInto(root, &req)
	req.Sampling = samplingFrom(root)
	reasoning, err := g.reasoningFrom(root)
	if err != nil {
		return req, err
	}
	req.Reasoning = reasoning
	if err := g.toolsFrom(root, &req); err != nil {
		return req, err
	}
	if err := g.toolChoiceFrom(root, &req); err != nil {
		return req, err
	}
	if err := g.inputFrom(root, &req); err != nil {
		return req, err
	}
	g.unmappedFrom(root)
	g.normalizeArguments(&req)
	return req, nil
}

func (g *Ingress) textInto(root map[string]any, req *canon.Request) {
	text, ok := root["text"].(map[string]any)
	if !ok {
		return
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		return
	}
	tf := &canon.TextFormat{}
	if t, ok := format["type"].(string); ok {
		tf.Type = t
	}
	if n, ok := format["name"].(string); ok {
		tf.Name = n
	}
	if d, ok := format["description"].(string); ok {
		tf.Description = d
	}
	if s, ok := format["schema"]; ok {
		if raw, err := json.Marshal(s); err == nil {
			tf.Schema = raw
		}
	}
	if strict, ok := format["strict"].(bool); ok {
		tf.Strict = &strict
	}
	req.Text.Format = tf
}

func samplingFrom(root map[string]any) canon.Sampling {
	var s canon.Sampling
	if v, ok := root["temperature"].(float64); ok {
		s.Temperature = &v
	}
	if v, ok := root["top_p"].(float64); ok {
		s.TopP = &v
	}
	if stop, ok := root["stop"].([]any); ok {
		for _, raw := range stop {
			if v, ok := raw.(string); ok {
				s.Stop = append(s.Stop, v)
			}
		}
	}
	if v, ok := root["parallel_tool_calls"].(bool); ok {
		s.ParallelToolCalls = &v
	}
	switch root["service_tier"] {
	case "flex":
		s.ServiceTier = canon.TierFlex
	case "priority":
		s.ServiceTier = canon.TierPriority
	}
	return s
}

func (g *Ingress) reasoningFrom(root map[string]any) (canon.ReasoningConfig, error) {
	var rc canon.ReasoningConfig
	m, ok := root["reasoning"].(map[string]any)
	if !ok {
		return rc, nil
	}
	if e, ok := m["effort"].(string); ok {
		switch e {
		case "none":
			rc.Effort = 0
			g.warnOf(WarnUnmappedField, "reasoning.effort=none")
		case "minimal":
			rc.Effort = canon.EffortMinimal
		case "off":
			rc.Effort = canon.EffortOff
		case "low":
			rc.Effort = canon.EffortLow
		case "medium":
			rc.Effort = canon.EffortMedium
		case "high":
			rc.Effort = canon.EffortHigh
		case "xhigh":
			rc.Effort = canon.EffortXHigh
		case "max", "ultra":
			rc.Effort = canon.EffortXHigh
			g.warnOf(WarnUnmappedField, "reasoning.effort="+e)
		default:
			return rc, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "reasoning.effort"}
		}
	}
	if s, ok := m["summary"].(string); ok {
		switch s {
		case "auto":
			rc.Summary = canon.SummaryAuto
		case "concise":
			rc.Summary = canon.SummaryConcise
		case "detailed":
			rc.Summary = canon.SummaryDetailed
		default:
			return rc, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "reasoning.summary"}
		}
	}
	return rc, nil
}

func (g *Ingress) toolsFrom(root map[string]any, req *canon.Request) error {
	tools, ok := root["tools"].([]any)
	if !ok {
		return nil
	}
	for i, raw := range tools {
		tm, ok := raw.(map[string]any)
		if !ok {
			return &ParseError{
				Status: http.StatusBadRequest,
				Reason: ReasonInvalidField,
				Field:  fmt.Sprintf("tools[%d]", i),
			}
		}
		name, _ := tm["name"].(string)
		desc, _ := tm["description"].(string)
		path := fmt.Sprintf("tools[%d]", i)
		switch tm["type"] {
	case "custom":
			format := canon.FormatText
			var grammar *canon.ToolGrammar
			if f, ok := tm["format"].(map[string]any); ok {
				switch ft, _ := f["type"].(string); ft {
				case "json":
					format = canon.FormatJSON
				case "grammar":
					syntax, _ := f["syntax"].(string)
					definition, _ := f["definition"].(string)
					grammar = &canon.ToolGrammar{Syntax: syntax, Definition: definition}
				}
			}
			if gr, ok := tm["grammar"].(map[string]any); ok {
				syntax, _ := gr["syntax"].(string)
				definition, _ := gr["definition"].(string)
				grammar = &canon.ToolGrammar{Syntax: syntax, Definition: definition}
			}
			req.Tools = append(req.Tools, canon.CustomToolDef{
				Name:        canon.ToolName(name),
				Description: desc,
				Format:      format,
				Grammar:     grammar,
			})
		case "local_shell":
			req.Tools = append(req.Tools, canon.LocalShellToolDef{})
		case "tool_search":
			limit := 0
			if f, ok := tm["max_results"].(float64); ok {
				limit = int(f)
			}
			req.Tools = append(req.Tools, canon.ToolSearchToolDef{Limit: limit})
		case "web_search", "image_generation":
			g.warnOf(WarnUnmappedField, path+".type="+fmt.Sprintf("%v", tm["type"]))
		default:
			fn := canon.FunctionTool{Name: canon.ToolName(name), Description: desc}
			if params, present := tm["parameters"]; present && params != nil {
				if err := validateSchema(params, path+".parameters"); err != nil {
					return &ParseError{
						Status: http.StatusBadRequest,
						Reason: ReasonInvalidField,
						Field:  path + ".parameters",
						Err:    err,
					}
				}
				raw, err := json.Marshal(params)
				if err != nil {
					return &ParseError{
						Status: http.StatusBadRequest,
						Reason: ReasonInvalidField,
						Field:  path + ".parameters",
						Err:    err,
					}
				}
				fn.Parameters = raw
			}
			if strict, ok := tm["strict"].(bool); ok {
				fn.Strict = strict
			}
			req.Tools = append(req.Tools, fn)
		}
	}
	return nil
}

var schemaPrimitiveTypes = map[string]bool{
	"object":  true,
	"array":   true,
	"string":  true,
	"number":  true,
	"integer": true,
	"boolean": true,
	"null":    true,
}

func validateSchema(v any, path string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a schema object", path)
	}
	if t, ok := m["type"]; ok {
		switch tv := t.(type) {
		case string:
			if !schemaPrimitiveTypes[tv] {
				return fmt.Errorf("%s.type %q is not a JSON Schema type", path, tv)
			}
		case []any:
			for _, e := range tv {
				s, ok := e.(string)
				if !ok || !schemaPrimitiveTypes[s] {
					return fmt.Errorf("%s.type entries must be JSON Schema type strings", path)
				}
			}
		default:
			return fmt.Errorf("%s.type must be a string or array of strings", path)
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for k, pv := range props {
			if err := validateSchema(pv, path+".properties."+k); err != nil {
				return err
			}
		}
	}
	if required, ok := m["required"].([]any); ok {
		props, _ := m["properties"].(map[string]any)
		for _, r := range required {
			name, ok := r.(string)
			if !ok {
				return fmt.Errorf("%s.required entries must be strings", path)
			}
			if props != nil {
				if _, ok := props[name]; !ok {
					return fmt.Errorf("%s.required name %q is missing from properties", path, name)
				}
			}
		}
	}
	if items, ok := m["items"]; ok {
		switch items.(type) {
		case map[string]any, bool:
		default:
			return fmt.Errorf("%s.items must be a schema object", path)
		}
		if im, ok := items.(map[string]any); ok {
			if err := validateSchema(im, path+".items"); err != nil {
				return err
			}
		}
	}
	for _, kw := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := m[kw].([]any); ok {
			for i, e := range arr {
				if err := validateSchema(e, fmt.Sprintf("%s.%s[%d]", path, kw, i)); err != nil {
					return err
				}
			}
		}
	}
	if ap, ok := m["additionalProperties"].(map[string]any); ok {
		if err := validateSchema(ap, path+".additionalProperties"); err != nil {
			return err
		}
	}
	return nil
}

func (g *Ingress) toolChoiceFrom(root map[string]any, req *canon.Request) error {
	switch tc := root["tool_choice"].(type) {
	case string:
		switch tc {
		case "none":
			req.ToolChoice = canon.ToolNone{}
		case "auto":
			req.ToolChoice = canon.ToolAuto{}
		case "required":
			req.ToolChoice = canon.ToolRequired{}
		default:
			return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "tool_choice"}
		}
	case map[string]any:
		switch tc["type"] {
		case "none":
			req.ToolChoice = canon.ToolNone{}
		case "auto":
			req.ToolChoice = canon.ToolAuto{}
		case "required":
			req.ToolChoice = canon.ToolRequired{}
		case "allowed_tools":
			mode, _ := tc["mode"].(string)
			if mode != "required" && mode != "auto" {
				return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "tool_choice.mode"}
			}
			names := allowedToolNames(tc)
			if mode == "required" && len(names) == 1 {
				req.ToolChoice = canon.ToolNamed{Name: canon.ToolName(names[0])}
				req.Tools = filterTools(req.Tools, names)
			} else if mode == "required" {
				req.ToolChoice = canon.ToolRequired{}
				req.Tools = filterTools(req.Tools, names)
			} else {
				req.ToolChoice = canon.ToolAuto{}
			}
		default:
			if name, ok := tc["name"].(string); ok {
				req.ToolChoice = canon.ToolNamed{Name: canon.ToolName(name)}
			} else {
				return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "tool_choice.type"}
			}
		}
	}
	return nil
}

func allowedToolNames(tc map[string]any) []string {
	var names []string
	tools, ok := tc["tools"].([]any)
	if !ok {
		return names
	}
	for _, tv := range tools {
		tm, ok := tv.(map[string]any)
		if !ok {
			continue
		}
		if n, ok := tm["name"].(string); ok {
			names = append(names, n)
		}
	}
	return names
}

func filterTools(tools []canon.Tool, names []string) []canon.Tool {
	allowed := make(map[string]struct{}, len(names))
	for _, n := range names {
		allowed[n] = struct{}{}
	}
	out := make([]canon.Tool, 0, len(tools))
	for _, t := range tools {
		switch tt := t.(type) {
		case canon.FunctionTool:
			if _, ok := allowed[string(tt.Name)]; ok {
				out = append(out, t)
			}
		case canon.CustomToolDef:
			if _, ok := allowed[string(tt.Name)]; ok {
				out = append(out, t)
			}
		}
	}
	return out
}

func (g *Ingress) inputFrom(root map[string]any, req *canon.Request) error {
	input, ok := root["input"]
	if !ok {
		return &ParseError{Status: http.StatusBadRequest, Reason: ReasonMissingField, Field: "input"}
	}
	switch in := input.(type) {
	case string:
		req.Input = []canon.Item{canon.Message{
			Role:    canon.RoleUser,
			Content: []canon.Content{canon.TextContent{Text: in}},
		}}
		return nil
	case []any:
		for i, raw := range in {
			m, ok := raw.(map[string]any)
			if !ok {
				return &ParseError{
					Status: http.StatusBadRequest,
					Reason: ReasonInvalidField,
					Field:  fmt.Sprintf("input[%d]", i),
				}
			}
			item, err := g.itemFrom(m, i)
			if err != nil {
				return err
			}
			if item != nil {
				req.Input = append(req.Input, item)
			}
		}
		return nil
	default:
		return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "input"}
	}
}

func (g *Ingress) itemFrom(m map[string]any, i int) (canon.Item, error) {
	path := fmt.Sprintf("input[%d]", i)
	typ, _ := m["type"].(string)
	switch typ {
	case "message":
		return g.messageFrom(m, path)
	case "reasoning":
		return g.reasoningItemFrom(m, path)
	case "function_call":
		return g.functionCallFrom(m, path)
	case "function_call_output":
		return g.functionOutputFrom(m, path)
	case "custom_tool_call":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		name, _ := m["name"].(string)
		input, _ := m["input"].(string)
		return canon.CustomToolCall{
			ID:     canon.ItemID(id),
			CallID: canon.CallID(callID),
			Name:   canon.ToolName(name),
			Input:  input,
		}, nil
	case "custom_tool_call_output":
		callID, _ := m["call_id"].(string)
		output, _ := m["output"].(string)
		return canon.CustomToolOutput{CallID: canon.CallID(callID), Output: output}, nil
	case "local_shell_call":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		command := shellCommand(m["action"])
		return canon.LocalShellCall{
			ID:      canon.ItemID(id),
			CallID:  canon.CallID(callID),
			Command: command,
		}, nil
	case "local_shell_output":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		exit := 0
		if e, ok := m["exit_code"].(float64); ok {
			exit = int(e)
		}
		output, _ := m["output"].(string)
		return canon.LocalShellOutput{
			ID:       canon.ItemID(id),
			CallID:   canon.CallID(callID),
			ExitCode: exit,
			Output:   output,
		}, nil
	case "tool_search_call":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		query, _ := m["query"].(string)
		return canon.ToolSearchCall{
			ID:     canon.ItemID(id),
			CallID: canon.CallID(callID),
			Query:  query,
		}, nil
	case "tool_search_output":
		callID, _ := m["call_id"].(string)
		out := canon.ToolSearchOutput{CallID: canon.CallID(callID)}
		if specs, ok := m["tools"].([]any); ok {
			for _, raw := range specs {
				spec, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				name, ok := spec["name"].(string)
				if !ok {
					g.warnOf(WarnUnknownItem, path+".tools entry without name")
					continue
				}
				summary, _ := spec["description"].(string)
				out.Results = append(out.Results, canon.ToolSearchResult{
					ToolName: canon.ToolName(name),
					Summary:  summary,
				})
			}
		}
		return out, nil
	case "context_compaction":
		if enc, ok := m["encrypted_content"].(string); ok && enc != "" {
			g.warnOf(WarnOpaquePayload, path+".encrypted_content")
		}
		return canon.CompactionMarker{Kind: canon.CompactionExplicit}, nil
	case "compaction_trigger":
		return canon.CompactionMarker{Kind: canon.CompactionAuto}, nil
	case "additional_tools":
		g.warnOf(WarnExoticItem, path+" additional_tools")
		return nil, nil
	default:
		g.warnOf(WarnUnknownItem, path+" type="+typ)
		return nil, nil
	}
}

func (g *Ingress) messageFrom(m map[string]any, path string) (canon.Item, error) {
	role, _ := m["role"].(string)
	r, err := roleFrom(role)
	if err != nil {
		return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path + ".role", Err: err}
	}
	content, err := g.contentFrom(m["content"], path+".content")
	if err != nil {
		return nil, err
	}
	return canon.Message{ID: itemID(m["id"]), Role: r, Content: content}, nil
}

func itemID(v any) canon.ItemID {
	s, _ := v.(string)
	return canon.ItemID(s)
}

func roleFrom(role string) (canon.Role, error) {
	switch role {
	case "user":
		return canon.RoleUser, nil
	case "assistant":
		return canon.RoleAssistant, nil
	case "system":
		return canon.RoleSystem, nil
	case "developer":
		return canon.RoleDeveloper, nil
	default:
		return 0, fmt.Errorf("unsupported role %q", role)
	}
}

func (g *Ingress) contentFrom(v any, path string) ([]canon.Content, error) {
	switch c := v.(type) {
	case string:
		return []canon.Content{canon.TextContent{Text: c}}, nil
	case []any:
		var out []canon.Content
		for i, raw := range c {
			part, ok := raw.(map[string]any)
			if !ok {
				return nil, &ParseError{
					Status: http.StatusBadRequest,
					Reason: ReasonInvalidField,
					Field:  fmt.Sprintf("%s[%d]", path, i),
				}
			}
			content, err := g.partToContent(part, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out = append(out, content)
		}
		return out, nil
	default:
		return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path}
	}
}

func (g *Ingress) partToContent(part map[string]any, path string) (canon.Content, error) {
	typ, _ := part["type"].(string)
	switch typ {
	case "input_text", "text", "output_text":
		text, ok := part["text"].(string)
		if !ok {
			return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonMissingField, Field: path + ".text"}
		}
		return canon.TextContent{Text: text}, nil
	case "input_image":
		imageURL, _ := part["image_url"].(string)
		detail, _ := part["detail"].(string)
		img, err := dataURLImage(imageURL, detail)
		if err != nil {
			return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path, Err: err}
		}
		return img, nil
	default:
		return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path + ".type"}
	}
}

func dataURLImage(imageURL, detail string) (canon.ImageContent, error) {
	const prefix = "data:"
	if !strings.HasPrefix(imageURL, prefix) {
		return canon.ImageContent{}, fmt.Errorf("image_url is not a data URL")
	}
	rest := imageURL[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return canon.ImageContent{}, fmt.Errorf("data URL has no payload")
	}
	meta := rest[:comma]
	payload := rest[comma+1:]
	if !strings.HasSuffix(meta, ";base64") {
		return canon.ImageContent{}, fmt.Errorf("data URL is not base64")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return canon.ImageContent{}, fmt.Errorf("data URL payload: %w", err)
	}
	return canon.ImageContent{
		MIMEType: strings.TrimSuffix(meta, ";base64"),
		Data:     data,
		Detail:   normalizeImageDetail(detail),
	}, nil
}

func normalizeImageDetail(detail string) string {
	if detail == "original" {
		return "high"
	}
	return detail
}

func (g *Ingress) reasoningItemFrom(m map[string]any, path string) (canon.Item, error) {
	item := canon.ReasoningItem{ID: itemID(m["id"])}
	if sig, ok := m["signature"].(string); ok {
		item.Signature = sig
	}
	summary, err := textParts(m["summary"], path+".summary", "summary_text")
	if err != nil {
		return nil, err
	}
	item.Summary = summary
	var contentTexts []string
	if parts, ok := m["content"].([]any); ok {
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path + ".content"}
			}
			text, ok := part["text"].(string)
			if !ok {
				return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonMissingField, Field: path + ".content[].text"}
			}
			contentTexts = append(contentTexts, text)
		}
	}
	item.Content = strings.Join(contentTexts, "")
	if item.Content == "" {
		for _, s := range summary {
			item.Content += s.Text
		}
	}
	if enc, ok := m["encrypted_content"].(string); ok && enc != "" {
		item.State = canon.OpaqueRef{Store: "wire", Key: enc}
	}
	return item, nil
}

func textParts(v any, path, wantType string) ([]canon.TextContent, error) {
	arr, ok := v.([]any)
	if !ok {
		if v == nil {
			return nil, nil
		}
		return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path}
	}
	var out []canon.TextContent
	for i, raw := range arr {
		part, ok := raw.(map[string]any)
		if !ok {
			return nil, &ParseError{
				Status: http.StatusBadRequest,
				Reason: ReasonInvalidField,
				Field:  fmt.Sprintf("%s[%d]", path, i),
			}
		}
		text, ok := part["text"].(string)
		if !ok {
			return nil, &ParseError{
				Status: http.StatusBadRequest,
				Reason: ReasonMissingField,
				Field:  fmt.Sprintf("%s[%d].text", path, i),
			}
		}
		if t, ok := part["type"].(string); ok && wantType != "" && t != wantType {
			return nil, &ParseError{
				Status: http.StatusBadRequest,
				Reason: ReasonInvalidField,
				Field:  fmt.Sprintf("%s[%d].type", path, i),
			}
		}
		out = append(out, canon.TextContent{Text: text})
	}
	return out, nil
}

func (g *Ingress) functionCallFrom(m map[string]any, path string) (canon.Item, error) {
	callID, _ := m["call_id"].(string)
	name, _ := m["name"].(string)
	call := canon.FunctionCall{
		ID:     itemID(m["id"]),
		CallID: canon.CallID(callID),
		Name:   canon.ToolName(name),
	}
	if args, ok := m["arguments"].(string); ok && args != "" {
		var v any
		if err := json.Unmarshal([]byte(args), &v); err != nil {
			return nil, &ParseError{
				Status: http.StatusBadRequest,
				Reason: ReasonInvalidJSON,
				Field:  path + ".arguments",
				Err:    err,
			}
		}
		if _, ok := v.(map[string]any); !ok {
			return nil, &ParseError{
				Status: http.StatusBadRequest,
				Reason: ReasonInvalidField,
				Field:  path + ".arguments",
			}
		}
		call.Arguments = []byte(args)
	}
	return call, nil
}

func (g *Ingress) functionOutputFrom(m map[string]any, path string) (canon.Item, error) {
	callID, ok := m["call_id"].(string)
	if !ok || callID == "" {
		return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonMissingField, Field: path + ".call_id"}
	}
	switch out := m["output"].(type) {
	case string:
		return canon.FunctionOutput{
			CallID: canon.CallID(callID),
			Output: []canon.Content{canon.TextContent{Text: out}},
		}, nil
	case []any:
		var contents []canon.Content
		for i, raw := range out {
			part, ok := raw.(map[string]any)
			if !ok {
				return nil, &ParseError{
					Status: http.StatusBadRequest,
					Reason: ReasonInvalidField,
					Field:  fmt.Sprintf("%s.output[%d]", path, i),
				}
			}
			text, ok := part["text"].(string)
			if !ok {
				return nil, &ParseError{
					Status: http.StatusBadRequest,
					Reason: ReasonMissingField,
					Field:  fmt.Sprintf("%s.output[%d].text", path, i),
				}
			}
			contents = append(contents, canon.TextContent{Text: text})
		}
		return canon.FunctionOutput{CallID: canon.CallID(callID), Output: contents}, nil
	case nil:
		return canon.FunctionOutput{CallID: canon.CallID(callID)}, nil
	default:
		return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path + ".output"}
	}
}

func shellCommand(action any) string {
	m, ok := action.(map[string]any)
	if !ok {
		return ""
	}
	parts, ok := m["command"].([]any)
	if !ok {
		if s, ok := m["command"].(string); ok {
			return s
		}
		return ""
	}
	command := ""
	for _, p := range parts {
		if s, ok := p.(string); ok {
			if command != "" {
				command += " "
			}
			command += s
		}
	}
	return command
}

func (g *Ingress) unmappedFrom(root map[string]any) {
	if v, ok := root["previous_response_id"].(string); ok && v != "" {
		g.warnOf(WarnUnmappedField, "previous_response_id")
	}
	if v, ok := root["prompt_cache_key"].(string); ok && v != "" {
		g.warnOf(WarnUnmappedField, "prompt_cache_key")
	}
	if _, ok := root["include"]; ok {
		g.warnOf(WarnUnmappedField, "include")
	}
}

func (g *Ingress) normalizeArguments(req *canon.Request) {
	schemas := make(map[canon.ToolName][]byte)
	for _, t := range req.Tools {
		if fn, ok := t.(canon.FunctionTool); ok && len(fn.Parameters) > 0 {
			schemas[fn.Name] = fn.Parameters
		}
	}
	if len(schemas) == 0 {
		return
	}
	for i, item := range req.Input {
		call, ok := item.(canon.FunctionCall)
		if !ok || len(call.Arguments) == 0 {
			continue
		}
		schema, ok := schemas[call.Name]
		if !ok {
			continue
		}
		if normalized, changed := normalizeAgainstSchema(schema, call.Arguments); changed {
			call.Arguments = normalized
			req.Input[i] = call
		}
	}
}

func normalizeAgainstSchema(schema []byte, args []byte) ([]byte, bool) {
	var schemaAny any
	if err := json.Unmarshal(schema, &schemaAny); err != nil {
		return args, false
	}
	sm, ok := schemaAny.(map[string]any)
	if !ok {
		return args, false
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return args, false
	}
	fixed, changed := normalizeValue(sm, v)
	if !changed {
		return args, false
	}
	out, err := json.Marshal(fixed)
	if err != nil {
		return args, false
	}
	return out, true
}

func normalizeValue(schema map[string]any, v any) (any, bool) {
	switch t := schema["type"].(type) {
	case string:
		return normalizeByType(t, schema, v)
	case []any:
		for _, tv := range t {
			if s, ok := tv.(string); ok {
				if fixed, changed := normalizeByType(s, schema, v); changed {
					return fixed, true
				}
			}
		}
		return v, false
	default:
		return v, false
	}
}

func normalizeByType(t string, schema map[string]any, v any) (any, bool) {
	switch t {
	case "integer":
		if f, ok := v.(float64); ok && f == math.Trunc(f) && !math.IsInf(f, 0) {
			return int64(f), true
		}
		return v, false
	case "object":
		return normalizeObject(schema, v)
	case "array":
		return normalizeArray(schema, v)
	default:
		return v, false
	}
}

func normalizeObject(schema map[string]any, v any) (any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return v, false
	}
	props, _ := schema["properties"].(map[string]any)
	changed := false
	for k, pv := range m {
		ps, ok := props[k].(map[string]any)
		if !ok {
			continue
		}
		nv, c := normalizeValue(ps, pv)
		if c {
			m[k] = nv
			changed = true
		}
	}
	return m, changed
}

func normalizeArray(schema map[string]any, v any) (any, bool) {
	arr, ok := v.([]any)
	if !ok {
		return v, false
	}
	items, ok := schema["items"].(map[string]any)
	if !ok {
		return v, false
	}
	changed := false
	for i, ev := range arr {
		nv, c := normalizeValue(items, ev)
		if c {
			arr[i] = nv
			changed = true
		}
	}
	return arr, changed
}

func factsFrom(hr *http.Request) (execution.Facts, error) {
	fwd := http.Header{}
	if v := strings.TrimSpace(hr.Header.Get("session_id")); v != "" {
		fwd.Set("x-session-id", v)
	}
	if v := strings.TrimSpace(hr.Header.Get("originator")); v != "" {
		fwd.Set("originator", v)
	}
	if v := strings.TrimSpace(hr.UserAgent()); v != "" {
		fwd.Set("User-Agent", v)
	}
	forward, err := execution.NewForwardSet(fwd)
	if err != nil {
		return execution.Facts{}, err
	}
	facts := execution.Facts{
		Client:  clientFrom(hr),
		Forward: forward,
	}
	if v := strings.TrimSpace(hr.Header.Get("session_id")); v != "" {
		facts.Session = execution.SessionKey(v)
	}
	if v := strings.TrimSpace(hr.Header.Get("thread-id")); v != "" {
		facts.Thread = execution.ThreadKey(v)
	} else if v := strings.TrimSpace(hr.Header.Get("x-codex-parent-thread-id")); v != "" {
		facts.Thread = execution.ThreadKey(v)
	}
	return facts, nil
}

func clientFrom(hr *http.Request) execution.Client {
	if hr.Header.Get("x-prism-grok") != "" {
		return execution.ClientGrok
	}
	if strings.HasPrefix(strings.TrimSpace(hr.Header.Get("originator")), "codex") {
		return execution.ClientCodex
	}
	return execution.ClientOMP
}
