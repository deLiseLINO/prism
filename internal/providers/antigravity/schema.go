package antigravity

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

const (
	schemaMaxDepth      = 24
	schemaMaxDerefDepth = 16
	schemaMaxNodes      = 1024
	rootSchemaFallback  = `{"type":"object","properties":{}}`
)

var allowedSchemaTypes = map[string]bool{
	"string":  true,
	"integer": true,
	"number":  true,
	"boolean": true,
	"array":   true,
	"object":  true,
}

type schemaState struct {
	remaining  int
	defs       map[string]map[string]any
	activeRefs map[string]bool
}

func sanitizeToolParameters(raw []byte) []byte {
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return json.RawMessage(rootSchemaFallback)
	}
	defs := map[string]map[string]any{}
	if m, ok := node.(map[string]any); ok {
		collectDefs(m, defs)
	}
	state := &schemaState{
		remaining:  schemaMaxNodes,
		defs:       defs,
		activeRefs: map[string]bool{},
	}
	out, ok := sanitizeSchemaNode(node, 0, 0, false, state)
	root, isMap := out.(map[string]any)
	if !ok || !isMap || len(root) == 0 {
		return json.RawMessage(rootSchemaFallback)
	}
	root["type"] = "object"
	if _, has := root["properties"]; !has {
		root["properties"] = map[string]any{}
	}
	encoded, err := marshalSchemaValue(root)
	if err != nil {
		return json.RawMessage(rootSchemaFallback)
	}
	return encoded
}

var schemaKeyOrder = []string{
	"type", "nullable", "description", "format", "enum", "properties", "required", "items",
}

func marshalSchemaValue(m map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, key := range schemaKeyOrder {
		v, ok := m[key]
		if !ok {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		keyJSON, _ := json.Marshal(key)
		buf.Write(keyJSON)
		buf.WriteByte(':')
		encoded, err := marshalSchemaField(key, v)
		if err != nil {
			return nil, err
		}
		buf.Write(encoded)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func marshalSchemaField(key string, v any) ([]byte, error) {
	if key == "properties" {
		return marshalPropertyBag(v)
	}
	return marshalSchemaNode(v)
}

func marshalPropertyBag(v any) ([]byte, error) {
	props, ok := v.(map[string]any)
	if !ok {
		return json.Marshal(v)
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			buf.WriteByte(',')
		}
		nameJSON, _ := json.Marshal(name)
		buf.Write(nameJSON)
		buf.WriteByte(':')
		encoded, err := marshalSchemaNode(props[name])
		if err != nil {
			return nil, err
		}
		buf.Write(encoded)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func marshalSchemaNode(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		return marshalSchemaValue(t)
	default:
		return json.Marshal(v)
	}
}

func collectDefs(root map[string]any, defs map[string]map[string]any) {
	for _, key := range []string{"$defs", "definitions"} {
		raw, ok := root[key].(map[string]any)
		if !ok {
			continue
		}
		prefix := defPrefix(key)
		for name, def := range raw {
			if m, ok := def.(map[string]any); ok {
				defs[prefix+name] = m
			}
		}
	}
}

func defPrefix(key string) string {
	if key == "$defs" {
		return "#/$defs/"
	}
	return "#/definitions/"
}

func resolveSchemaRef(ref string, defs map[string]map[string]any) (map[string]any, bool) {
	for prefix, def := range defs {
		if strings.HasPrefix(ref, prefix) {
			return def, true
		}
	}
	return nil, false
}

func mergeRefTarget(target, overlay map[string]any) map[string]any {
	merged := map[string]any{}
	for k, v := range target {
		merged[k] = v
	}
	for k, v := range overlay {
		if k == "$ref" {
			continue
		}
		merged[k] = v
	}
	return merged
}

func normalizeSchemaType(value any, out map[string]any, preserveNull bool) {
	candidates := []any{value}
	if arr, ok := value.([]any); ok {
		candidates = arr
	}
	sawNull := false
	for _, c := range candidates {
		s, ok := c.(string)
		if !ok {
			continue
		}
		switch lower := strings.ToLower(s); {
		case lower == "null":
			sawNull = true
		case allowedSchemaTypes[lower]:
			if _, has := out["type"]; !has {
				out["type"] = lower
			}
		}
	}
	if !sawNull {
		return
	}
	if _, has := out["type"]; has {
		out["nullable"] = true
	} else if preserveNull {
		out["type"] = "null"
	} else {
		out["nullable"] = true
	}
}

func sanitizeEnumValue(value any) []any {
	arr, ok := value.([]any)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []any
	for _, item := range arr {
		s, ok := item.(string)
		if !ok || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func isNullOnlySchema(s map[string]any) bool {
	if len(s) != 1 {
		return false
	}
	_, has := s["type"]
	return has && s["type"] == "null"
}

func isEnumOnlySchema(s map[string]any, hasType bool) bool {
	enum, hasEnum := s["enum"].([]any)
	if !hasEnum || len(enum) == 0 {
		return false
	}
	for k := range s {
		if k == "enum" {
			continue
		}
		if k == "type" && hasType {
			continue
		}
		return false
	}
	return true
}

func sanitizeSchemaNode(node any, depth, refDepth int, preserveNull bool, state *schemaState) (any, bool) {
	if state.remaining <= 0 {
		return nil, false
	}
	state.remaining--
	m, ok := node.(map[string]any)
	if !ok || depth >= schemaMaxDepth {
		return map[string]any{}, true
	}
	if ref, ok := m["$ref"].(string); ok && refDepth < schemaMaxDerefDepth {
		if target, found := resolveSchemaRef(ref, state.defs); found {
			if state.activeRefs[ref] {
				return map[string]any{}, true
			}
			state.activeRefs[ref] = true
			merged := mergeRefTarget(target, m)
			out, ok := sanitizeSchemaNode(merged, depth, refDepth+1, preserveNull, state)
			delete(state.activeRefs, ref)
			return out, ok
		}
	}
	out := map[string]any{}
	normalizeSchemaType(m["type"], out, preserveNull)
	if nullable, ok := m["nullable"].(bool); ok {
		out["nullable"] = nullable
	}
	if desc, ok := m["description"].(string); ok {
		out["description"] = desc
	}
	if format, ok := m["format"].(string); ok {
		out["format"] = format
	}
	enum := sanitizeEnumValue(m["enum"])
	if enum == nil {
		if c, ok := m["const"].(string); ok {
			enum = sanitizeEnumValue([]any{c})
		}
	}
	if enum != nil {
		out["enum"] = enum
	}
	var props map[string]any
	if raw, ok := m["properties"].(map[string]any); ok && state.remaining > 0 {
		props = map[string]any{}
		for name, pv := range raw {
			if state.remaining <= 0 {
				break
			}
			child, ok := sanitizeSchemaNode(pv, depth+1, refDepth, false, state)
			if !ok {
				break
			}
			props[name] = child
		}
		out["properties"] = props
	}
	if required, ok := m["required"].([]any); ok && props != nil {
		seen := map[string]bool{}
		var kept []any
		for _, r := range required {
			s, ok := r.(string)
			if !ok || seen[s] {
				continue
			}
			if _, exists := props[s]; !exists {
				continue
			}
			seen[s] = true
			kept = append(kept, s)
		}
		if len(kept) > 0 {
			out["required"] = kept
		}
	}
	if state.remaining <= 0 {
		return out, true
	}
	if items, ok := m["items"].(map[string]any); ok {
		child, ok := sanitizeSchemaNode(items, depth+1, refDepth, false, state)
		if ok {
			out["items"] = child
		}
	}
	if state.remaining <= 0 {
		return out, true
	}
	if anyOf, ok := m["anyOf"].([]any); ok {
		for k, v := range collapseAnyOf(anyOf, depth, refDepth, state) {
			out[k] = v
		}
	}
	return out, true
}

func collapseAnyOf(branches []any, depth, refDepth int, state *schemaState) map[string]any {
	if len(branches) == 0 {
		return map[string]any{}
	}
	var schemas []map[string]any
	for _, branch := range branches {
		if state.remaining <= 0 {
			return map[string]any{}
		}
		child, ok := sanitizeSchemaNode(branch, depth+1, refDepth, true, state)
		if !ok {
			return map[string]any{}
		}
		if m, ok := child.(map[string]any); ok {
			schemas = append(schemas, m)
		}
	}
	nonNull := make([]map[string]any, 0, len(schemas))
	nullOnly := 0
	for _, s := range schemas {
		if isNullOnlySchema(s) {
			nullOnly++
			continue
		}
		nonNull = append(nonNull, s)
	}
	if len(nonNull) == 1 && nullOnly > 0 {
		merged := map[string]any{}
		for k, v := range nonNull[0] {
			merged[k] = v
		}
		merged["nullable"] = true
		return merged
	}
	if len(schemas) > 0 {
		firstType, hasType := schemas[0]["type"].(string)
		sameType := hasType && firstType != "null"
		enumOnly := true
		for _, s := range schemas {
			t, _ := s["type"].(string)
			if t != firstType || t == "null" || !hasType {
				sameType = false
			}
			if !isEnumOnlySchema(s, hasType) {
				enumOnly = false
			}
		}
		if sameType && enumOnly {
			var values []any
			for _, s := range schemas {
				values = append(values, s["enum"].([]any)...)
			}
			merged := map[string]any{"type": firstType}
			if enum := sanitizeEnumValue(values); enum != nil {
				merged["enum"] = enum
			}
			return merged
		}
	}
	return map[string]any{}
}
