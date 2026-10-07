package responses

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/reasonenv"
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
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return canon.Request{}, execution.Facts{}, &ParseError{
			Status: http.StatusBadRequest,
			Reason: ReasonInvalidJSON,
			Field:  "body",
			Err:    err,
		}
	}
	if decoder.Decode(new(any)) != io.EOF {
		return canon.Request{}, execution.Facts{}, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidJSON, Field: "body"}
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
	if previous := root["previous_response_id"]; previous != nil && previous != "" {
		return req, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "previous_response_id", Err: fmt.Errorf("resend the full conversation without previous_response_id")}
	}
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
	if max, ok := numberValue(root["max_output_tokens"]); ok && max > 0 {
		req.MaxOutputTokens = int(max)
	}
	if err := g.textInto(root, &req); err != nil {
		return req, err
	}
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
	return req, nil
}

func (g *Ingress) textInto(root map[string]any, req *canon.Request) error {
	if root["text"] == nil {
		return nil
	}
	text, ok := root["text"].(map[string]any)
	if !ok {
		return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "text"}
	}
	switch text["verbosity"] {
	case "low":
		req.Text.Verbosity = canon.VerbosityLow
	case "medium":
		req.Text.Verbosity = canon.VerbosityMedium
	case "high":
		req.Text.Verbosity = canon.VerbosityHigh
	case nil:
	default:
		return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "text.verbosity"}
	}
	if text["format"] == nil {
		return nil
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "text.format"}
	}
	typ, _ := format["type"].(string)
	if typ != "text" && typ != "json_object" && typ != "json_schema" {
		return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "text.format.type"}
	}
	for key := range format {
		if key != "type" && (typ != "json_schema" || key != "name" && key != "description" && key != "schema" && key != "strict") {
			return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "text.format." + key}
		}
	}
	tf := &canon.TextFormat{Type: typ}
	if typ == "json_schema" {
		name, ok := format["name"].(string)
		if !ok || name == "" {
			return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "text.format.name"}
		}
		tf.Name = name
		if _, ok := format["schema"].(map[string]any); !ok {
			return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "text.format.schema"}
		}
		tf.Schema, _ = json.Marshal(format["schema"])
		if value, present := format["description"]; present {
			description, ok := value.(string)
			if !ok {
				return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "text.format.description"}
			}
			tf.Description = description
		}
		if value := format["strict"]; value != nil {
			strict, ok := value.(bool)
			if !ok {
				return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "text.format.strict"}
			}
			tf.Strict = &strict
		}
	}
	req.Text.Format = tf
	return nil
}

func samplingFrom(root map[string]any) canon.Sampling {
	var s canon.Sampling
	if v, ok := numberValue(root["temperature"]); ok {
		s.Temperature = &v
	}
	if v, ok := numberValue(root["top_p"]); ok {
		s.TopP = &v
	}
	if v, ok := numberValue(root["presence_penalty"]); ok {
		s.PresencePenalty = &v
	}
	if v, ok := numberValue(root["frequency_penalty"]); ok {
		s.FrequencyPenalty = &v
	}
	if stop, ok := root["stop"].([]any); ok {
		for _, raw := range stop {
			if v, ok := raw.(string); ok && v != "" {
				s.Stop = append(s.Stop, v)
			}
		}
	}
	if v, ok := root["parallel_tool_calls"].(bool); ok {
		s.ParallelToolCalls = &v
	}
	switch root["service_tier"] {
	case "default":
		s.ServiceTier = canon.TierDefault
	case "flex":
		s.ServiceTier = canon.TierFlex
	case "priority":
		s.ServiceTier = canon.TierPriority
	}
	return s
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case json.Number:
		v, err := number.Float64()
		return v, err == nil
	case float64:
		return number, true
	default:
		return 0, false
	}
}

func (g *Ingress) reasoningFrom(root map[string]any) (canon.ReasoningConfig, error) {
	var rc canon.ReasoningConfig
	m, ok := root["reasoning"].(map[string]any)
	if !ok {
		return rc, nil
	}
	if e, ok := m["effort"].(string); ok {
		switch e {
		case "none", "off":
			rc.Effort = canon.EffortOff
		case "minimal":
			rc.Effort = canon.EffortMinimal
		case "low":
			rc.Effort = canon.EffortLow
		case "medium":
			rc.Effort = canon.EffortMedium
		case "high":
			rc.Effort = canon.EffortHigh
		case "xhigh":
			rc.Effort = canon.EffortXHigh
		case "max":
			rc.Effort = canon.EffortMax
		case "ultra":
			rc.Effort = canon.EffortMax
			g.warnOf(WarnUnmappedField, "reasoning.effort=ultra")
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
	flattened := make(map[canon.ToolName]canon.ToolRoute)
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
			req.Tools = append(req.Tools, customToolFrom(tm, name, desc))
		case "namespace":
			if err := namespaceToolsFrom(tm, path, req, flattened); err != nil {
				return err
			}
		case "local_shell":
			req.Tools = append(req.Tools, canon.LocalShellToolDef{})
		case "tool_search":
			limit := 0
			if f, ok := numberValue(tm["max_results"]); ok {
				limit = int(f)
			}
			req.Tools = append(req.Tools, canon.ToolSearchToolDef{Limit: limit})
		case "web_search", "image_generation":
			g.warnOf(WarnUnmappedField, path+".type="+fmt.Sprintf("%v", tm["type"]))
		default:
			fn, err := functionToolFrom(tm, name, desc, path)
			if err != nil {
				return err
			}
			req.Tools = append(req.Tools, fn)
		}
	}
	return nil
}

func customToolFrom(tm map[string]any, name, desc string) canon.CustomToolDef {
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
	return canon.CustomToolDef{
		Name:        canon.ToolName(name),
		Description: desc,
		Format:      format,
		Grammar:     grammar,
	}
}

func functionToolFrom(tm map[string]any, name, desc, path string) (canon.FunctionTool, error) {
	fn := canon.FunctionTool{Name: canon.ToolName(name), Description: desc}
	if params, present := tm["parameters"]; present && params != nil {
		if err := validateSchema(params, path+".parameters"); err != nil {
			return fn, &ParseError{
				Status: http.StatusBadRequest,
				Reason: ReasonInvalidField,
				Field:  path + ".parameters",
				Err:    err,
			}
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return fn, &ParseError{
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
	return fn, nil
}

const reservedNamespace = "functions"

func isBareNamespace(namespace string) bool {
	return namespace == "" || namespace == reservedNamespace
}

func replayedToolName(m map[string]any, name string) string {
	ns, _ := m["namespace"].(string)
	return wireToolName(ns, name)
}

func wireToolName(namespace, name string) string {
	if isBareNamespace(namespace) {
		return name
	}
	return namespace + "__" + name
}

func namespaceToolsFrom(tm map[string]any, path string, req *canon.Request, flattened map[canon.ToolName]canon.ToolRoute) error {
	ns, _ := tm["name"].(string)
	children, _ := tm["tools"].([]any)
	for j, raw := range children {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := child["name"].(string)
		typ, _ := child["type"].(string)
		if name == "" || (typ != "function" && typ != "custom") {
			continue
		}
		childPath := fmt.Sprintf("%s.tools[%d]", path, j)
		wire := wireToolName(ns, name)
		identity := canon.ToolRoute{Namespace: ns, Name: name}
		if isBareNamespace(ns) {
			identity.Namespace = ""
		}
		known, seen := flattened[canon.ToolName(wire)]
		if seen {
			if known != identity {
				return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: childPath + ".name"}
			}
			continue
		}
		desc, _ := child["description"].(string)
		if typ == "custom" {
			req.Tools = append(req.Tools, customToolFrom(child, wire, desc))
		} else {
			fn, err := functionToolFrom(child, wire, desc, childPath)
			if err != nil {
				return err
			}
			req.Tools = append(req.Tools, fn)
		}
		flattened[canon.ToolName(wire)] = identity
		if !isBareNamespace(ns) {
			registerToolRoute(req, canon.ToolName(wire), identity)
		}
	}
	return nil
}

func registerToolRoute(req *canon.Request, wire canon.ToolName, identity canon.ToolRoute) {
	if req.ToolRoutes == nil {
		req.ToolRoutes = make(map[canon.ToolName]canon.ToolRoute)
	}
	req.ToolRoutes[wire] = identity
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
			selectors, ok := tc["tools"].([]any)
			if !ok || len(selectors) == 0 {
				return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "tool_choice.tools"}
			}
			var names []string
			for _, raw := range selectors {
				selector, ok := raw.(map[string]any)
				if !ok {
					return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "tool_choice.tools"}
				}
				name, err := resolveToolSelector(selector, req)
				if err != nil {
					return err
				}
				names = append(names, name)
			}
			req.Tools = filterTools(req.Tools, names)
			if mode == "auto" {
				req.ToolChoice = canon.ToolAuto{}
			} else if len(req.Tools) == 1 {
				if _, function := req.Tools[0].(canon.FunctionTool); function {
					req.ToolChoice = canon.ToolNamed{Name: canon.ToolName(names[0])}
				} else {
					req.ToolChoice = canon.ToolRequired{}
				}
			} else {
				req.ToolChoice = canon.ToolRequired{}
			}
		default:
			name, err := resolveToolSelector(tc, req)
			if err != nil {
				return err
			}
			selected := filterTools(req.Tools, []string{name})
			if _, custom := selected[0].(canon.CustomToolDef); custom {
				req.Tools = selected
				req.ToolChoice = canon.ToolRequired{}
			} else {
				req.ToolChoice = canon.ToolNamed{Name: canon.ToolName(name)}
			}
		}
	case nil:
	default:
		return &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "tool_choice"}
	}
	return nil
}

func resolveToolSelector(selector map[string]any, req *canon.Request) (string, error) {
	fail := func(field string) (string, error) {
		return "", &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: "tool_choice." + field}
	}
	name, ok := selector["name"].(string)
	if !ok || name == "" {
		return fail("name")
	}
	var namespace string
	if raw, present := selector["namespace"]; present {
		var ok bool
		namespace, ok = raw.(string)
		if !ok {
			return fail("namespace")
		}
	}
	wire := wireToolName(namespace, name)
	if _, explicit := selector["namespace"]; explicit {
		route := req.ToolRoutes[canon.ToolName(wire)]
		if isBareNamespace(namespace) {
			if route.Namespace != "" {
				return fail("namespace")
			}
		} else if route != (canon.ToolRoute{Namespace: namespace, Name: name}) {
			return fail("namespace")
		}
	}
	var kind string
	for _, tool := range req.Tools {
		var toolName canon.ToolName
		var toolKind string
		switch tool := tool.(type) {
		case canon.FunctionTool:
			toolName, toolKind = tool.Name, "function"
		case canon.CustomToolDef:
			toolName, toolKind = tool.Name, "custom"
		}
		if string(toolName) == wire {
			if kind != "" {
				return fail("name")
			}
			kind = toolKind
		}
	}
	if kind == "" {
		return fail("name")
	}
	if typ, present := selector["type"]; present && typ != kind {
		return fail("type")
	}
	return wire, nil
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
	if _, hasRole := m["role"]; typ == "" && hasRole {
		typ = "message"
	}
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
		callID, ok := m["call_id"].(string)
		if !ok || callID == "" {
			return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonMissingField, Field: path + ".call_id"}
		}
		name, ok := m["name"].(string)
		if !ok || name == "" {
			return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonMissingField, Field: path + ".name"}
		}
		name = replayedToolName(m, name)
		input, ok := m["input"].(string)
		if !ok {
			return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path + ".input"}
		}
		return canon.CustomToolCall{
			ID:     canon.ItemID(id),
			CallID: canon.CallID(callID),
			Name:   canon.ToolName(name),
			Input:  input,
		}, nil
	case "custom_tool_call_output":
		callID, ok := m["call_id"].(string)
		if !ok || callID == "" {
			return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonMissingField, Field: path + ".call_id"}
		}
		if _, array := m["output"].([]any); array {
			item, err := g.functionOutputFrom(m, path)
			if err != nil {
				return nil, err
			}
			content := item.(canon.FunctionOutput).Output
			if content == nil {
				content = []canon.Content{}
			}
			return canon.CustomToolOutput{CallID: canon.CallID(callID), Content: content}, nil
		}
		output, ok := m["output"].(string)
		if !ok {
			return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path + ".output"}
		}
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
		if e, ok := numberValue(m["exit_code"]); ok {
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
	case "compaction", "compaction_summary", "context_compaction":
		if raw, supplied := m["encrypted_content"]; supplied && raw != nil {
			if _, ok := raw.(string); !ok {
				return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path + ".encrypted_content"}
			}
		}
		enc, _ := m["encrypted_content"].(string)
		if enc == "" {
			return nil, nil
		}
		return canon.CompactionMarker{ID: itemID(m["id"]), Kind: canon.CompactionExplicit, Type: typ, State: canon.OpaqueRef{Store: canon.StoreWire, Key: enc}}, nil
	case "compaction_trigger":
		return nil, nil
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
	phase, _ := m["phase"].(string)
	return canon.Message{ID: itemID(m["id"]), Role: r, Phase: canon.ParseMessagePhase(phase), Content: content}, nil
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
	case "refusal":
		text, ok := part["refusal"].(string)
		if !ok {
			return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonMissingField, Field: path + ".refusal"}
		}
		return canon.TextContent{Text: text}, nil
	case "input_image", "output_image":
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
		if strings.HasPrefix(enc, reasonenv.Prefix) {
			if _, ok := reasonenv.Decode(enc); !ok {
				return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: path + ".encrypted_content"}
			}
		}
		if env, ok := reasonenv.Decode(enc); ok {
			if env.Sig != "" {
				item.Signature = env.Sig
			} else {
				item.Signature = enc
			}
			if item.Content == "" || env.Sig != "" {
				item.Content = env.Txt
			}
		} else {
			item.State = canon.OpaqueRef{Store: canon.StoreWire, Key: enc}
		}
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
	name = replayedToolName(m, name)
	call := canon.FunctionCall{
		ID:     itemID(m["id"]),
		CallID: canon.CallID(callID),
		Name:   canon.ToolName(name),
	}
	if args, ok := m["arguments"].(string); ok && args != "" {
		var v map[string]json.RawMessage
		if err := json.Unmarshal([]byte(args), &v); err != nil {
			return nil, &ParseError{
				Status: http.StatusBadRequest,
				Reason: ReasonInvalidJSON,
				Field:  path + ".arguments",
				Err:    err,
			}
		}
		if v == nil {
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
			if typ, _ := part["type"].(string); typ == "input_image" || typ == "output_image" {
				content, err := g.partToContent(part, fmt.Sprintf("%s.output[%d]", path, i))
				if err != nil {
					return nil, err
				}
				contents = append(contents, content)
				continue
			}
			typ, _ := part["type"].(string)
			if typ != "input_text" && typ != "output_text" && typ != "text" && typ != "refusal" {
				return nil, &ParseError{Status: http.StatusBadRequest, Reason: ReasonInvalidField, Field: fmt.Sprintf("%s.output[%d].type", path, i)}
			}
			if typ == "refusal" {
				part = map[string]any{"text": part["refusal"]}
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
