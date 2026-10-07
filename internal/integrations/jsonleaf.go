package integrations

import (
	"encoding/json"
	"fmt"
	"strings"
)

type JSONPatch struct {
	Kind      string
	Next      string
	Changed   bool
	Reason    string
	Retryable bool
}
type JSONScalarEntry struct {
	Key   string
	Value string
}
type JSONLeafRead struct {
	Kind     string
	Endpoint *string
	Reason   string
}

const (
	jsonLeafPresent = "present"
	jsonLeafAbsent  = "absent"
	jsonLeafRefused = "refused"
)

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }
func jsonUnquote(s string) string {
	var v string
	if json.Unmarshal([]byte(`"`+s+`"`), &v) != nil {
		return s
	}
	return v
}
func jsonPatchRefused(reason string) JSONPatch { return JSONPatch{Kind: "refused", Reason: reason} }
func jsonPatchForceable(reason string) JSONPatch {
	return JSONPatch{Kind: "refused", Reason: reason, Retryable: true}
}
func jsonResult(before, next string) JSONPatch {
	return JSONPatch{Kind: "written", Next: next, Changed: before != next}
}

type jsonMember struct {
	key               string
	start, end, comma int
	value             *jsonNode
}
type jsonNode struct {
	start, end int
	object     bool
	members    []jsonMember
}
type jsonParser struct {
	text string
	pos  int
}

func (p *jsonParser) space() error {
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		case '/':
			if strings.HasPrefix(p.text[p.pos:], "//") {
				for p.pos < len(p.text) && p.text[p.pos] != '\n' {
					p.pos++
				}
			} else if strings.HasPrefix(p.text[p.pos:], "/*") {
				end := strings.Index(p.text[p.pos+2:], "*/")
				if end < 0 {
					return fmt.Errorf("unclosed comment")
				}
				p.pos += end + 4
			} else {
				return fmt.Errorf("invalid comment")
			}
		default:
			return nil
		}
	}
	return nil
}
func (p *jsonParser) stringValue() (string, error) {
	start := p.pos
	p.pos++
	for p.pos < len(p.text) {
		c := p.text[p.pos]
		p.pos++
		if c == '\\' {
			p.pos++
		} else if c == '"' {
			var s string
			err := json.Unmarshal([]byte(p.text[start:p.pos]), &s)
			return s, err
		}
	}
	return "", fmt.Errorf("unclosed string")
}
func (p *jsonParser) value(depth int) (*jsonNode, error) {
	if depth > 256 {
		return nil, fmt.Errorf("nesting too deep")
	}
	if err := p.space(); err != nil {
		return nil, err
	}
	if p.pos >= len(p.text) {
		return nil, fmt.Errorf("missing value")
	}
	n := &jsonNode{start: p.pos}
	c := p.text[p.pos]
	if c == '{' || c == '[' {
		n.object = c == '{'
		p.pos++
		seen := map[string]bool{}
		for {
			if err := p.space(); err != nil {
				return nil, err
			}
			if p.pos >= len(p.text) {
				return nil, fmt.Errorf("unclosed container")
			}
			if (c == '{' && p.text[p.pos] == '}') || (c == '[' && p.text[p.pos] == ']') {
				p.pos++
				n.end = p.pos
				return n, nil
			}
			m := jsonMember{start: p.pos, comma: -1}
			if n.object {
				if p.text[p.pos] != '"' {
					return nil, fmt.Errorf("object key is not a string")
				}
				key, err := p.stringValue()
				if err != nil {
					return nil, err
				}
				m.key = key
				if seen[key] {
					return nil, fmt.Errorf("duplicate key %q", key)
				}
				seen[key] = true
				if err = p.space(); err != nil {
					return nil, err
				}
				if p.pos >= len(p.text) || p.text[p.pos] != ':' {
					return nil, fmt.Errorf("missing colon")
				}
				p.pos++
			}
			value, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			m.value = value
			m.end = p.pos
			if err = p.space(); err != nil {
				return nil, err
			}
			if p.pos < len(p.text) && p.text[p.pos] == ',' {
				m.comma = p.pos
				p.pos++
				n.members = append(n.members, m)
				continue
			}
			n.members = append(n.members, m)
			if p.pos >= len(p.text) || !((c == '{' && p.text[p.pos] == '}') || (c == '[' && p.text[p.pos] == ']')) {
				return nil, fmt.Errorf("missing comma")
			}
		}
	}
	if c == '"' {
		if _, err := p.stringValue(); err != nil {
			return nil, err
		}
	} else {
		for p.pos < len(p.text) && !strings.ContainsRune(" \t\r\n,}]/", rune(p.text[p.pos])) {
			p.pos++
		}
		if p.pos == n.start || !json.Valid([]byte(p.text[n.start:p.pos])) {
			return nil, fmt.Errorf("invalid scalar")
		}
	}
	n.end = p.pos
	return n, nil
}
func parseJSONObject(text string) (*jsonNode, error) {
	p := jsonParser{text: text}
	if strings.HasPrefix(text, "\xef\xbb\xbf") {
		p.pos = 3
	}
	if err := p.space(); err != nil {
		return nil, err
	}
	if p.pos == len(text) {
		return nil, nil
	}
	n, err := p.value(0)
	if err != nil {
		return nil, err
	}
	if !n.object {
		return nil, fmt.Errorf("root is not an object")
	}
	if err = p.space(); err != nil {
		return nil, err
	}
	if p.pos != len(text) {
		return nil, fmt.Errorf("trailing content")
	}
	return n, nil
}
func jsonContainer(text, key, label string) (*jsonNode, *jsonNode, string) {
	root, err := parseJSONObject(text)
	if err != nil {
		return nil, nil, "prism: " + label + " patch refused: " + err.Error()
	}
	if root == nil {
		return nil, nil, ""
	}
	for _, m := range root.members {
		if m.key == key {
			if !m.value.object {
				return root, nil, "prism: " + label + " patch refused: " + key + " is not an object"
			}
			return root, m.value, ""
		}
	}
	return root, nil, ""
}
func jsonFind(n *jsonNode, key string) (jsonMember, bool) {
	if n != nil {
		for _, m := range n.members {
			if m.key == key {
				return m, true
			}
		}
	}
	return jsonMember{}, false
}
func jsonIndent(text string, pos int) int {
	start := strings.LastIndex(text[:pos], "\n") + 1
	n := 0
	for start+n < pos && text[start+n] == ' ' {
		n++
	}
	return n
}
func jsonInsert(text string, n *jsonNode, key, value string) string {
	indent := jsonIndent(text, n.start) + 2
	if len(n.members) > 0 {
		indent = jsonIndent(text, n.members[0].start)
	}
	end := n.end - 1
	line := strings.LastIndex(text[:end], "\n") + 1
	block := line > n.start && strings.TrimSpace(text[line:end]) == ""
	insert := end
	suffix := ""
	prefix := "\n"
	if block {
		insert = line
		prefix = ""
	} else {
		suffix = "\n" + strings.Repeat(" ", max(0, indent-2))
	}
	body := prefix + strings.Repeat(" ", indent) + jsonString(key) + ": " + value + "\n" + suffix
	if len(n.members) > 0 {
		last := n.members[len(n.members)-1]
		if last.comma < 0 {
			text = text[:last.end] + "," + text[last.end:]
			insert++
		}
	}
	return text[:insert] + body + text[insert:]
}
func jsonRemoveMember(text string, n *jsonNode, key string) string {
	m, ok := jsonFind(n, key)
	if !ok {
		return text
	}
	start, end := m.start, m.end
	if m.comma >= 0 {
		end = m.comma + 1
	}
	line := strings.LastIndex(text[:start], "\n") + 1
	if strings.TrimSpace(text[line:start]) == "" {
		start = line
		next := strings.IndexByte(text[end:], '\n')
		if next >= 0 && strings.TrimSpace(text[end:end+next]) == "" {
			end += next + 1
		}
	}
	if m.comma < 0 {
		for i, prev := range n.members {
			if prev.key == key && i > 0 {
				comma := n.members[i-1].comma
				if comma >= 0 {
					text = text[:comma] + text[comma+1:]
					if comma < start {
						start--
						end--
					}
				}
				break
			}
		}
	}
	return text[:start] + text[end:]
}
func jsonUpsert(text string, n *jsonNode, key, value string) string {
	if m, ok := jsonFind(n, key); ok {
		if text[m.value.start:m.value.end] == value {
			return text
		}
		return text[:m.value.start] + value + text[m.value.end:]
	}
	return jsonInsert(text, n, key, value)
}

func jsonContainerHasOnlyMember(text string, n *jsonNode) bool {
	if len(n.members) != 1 {
		return false
	}
	m := n.members[0]
	end := m.end
	if m.comma >= 0 {
		end = m.comma + 1
	}
	return strings.TrimSpace(text[n.start+1:m.start]) == "" && strings.TrimSpace(text[end:n.end-1]) == ""
}
func UpsertJSONBlockLeaf(text, containerKey, leafKey, fileLabel string, renderBody func(int) string) JSONPatch {
	root, container, reason := jsonContainer(text, containerKey, fileLabel)
	if reason != "" {
		return jsonPatchRefused(reason)
	}
	indent := 4
	if container != nil {
		indent = jsonIndent(text, container.start) + 2
		if len(container.members) > 0 {
			indent = jsonIndent(text, container.members[0].start)
		}
	}
	body := renderBody(indent)
	pos := strings.IndexByte(body, ':')
	if pos < 0 {
		return jsonPatchRefused("prism: invalid rendered leaf")
	}
	value := strings.TrimSpace(body[pos+1:])
	if container != nil {
		if m, ok := jsonFind(container, leafKey); ok && !m.value.object {
			return jsonPatchRefused("prism: " + fileLabel + " patch refused: " + leafKey + " is not an object")
		}
		return jsonResult(text, jsonUpsert(text, container, leafKey, value))
	}
	member := "{\n" + strings.Repeat(" ", indent) + jsonString(leafKey) + ": " + value + "\n" + strings.Repeat(" ", indent-2) + "}"
	if root == nil {
		next := "{\n  " + jsonString(containerKey) + ": " + member + "\n}\n"
		return jsonResult(text, next)
	}
	return jsonResult(text, jsonInsert(text, root, containerKey, member))
}
func RemoveJSONBlockLeaf(text, containerKey, leafKey, fileLabel string) JSONPatch {
	root, container, reason := jsonContainer(text, containerKey, fileLabel)
	if reason != "" {
		return jsonPatchRefused(reason)
	}
	if container == nil {
		return jsonResult(text, text)
	}
	if _, ok := jsonFind(container, leafKey); !ok {
		return jsonResult(text, text)
	}
	if len(container.members) == 1 && jsonContainerHasOnlyMember(text, container) {
		return jsonResult(text, jsonRemoveMember(text, root, containerKey))
	}
	return jsonResult(text, jsonRemoveMember(text, container, leafKey))
}
func jsonEndpoint(text string, n *jsonNode, key string) *string {
	if n == nil {
		return nil
	}
	if m, ok := jsonFind(n, key); ok {
		var s string
		if json.Unmarshal([]byte(text[m.value.start:m.value.end]), &s) == nil {
			return &s
		}
	}
	for _, m := range n.members {
		if m.value.object {
			if s := jsonEndpoint(text, m.value, key); s != nil {
				return s
			}
		}
	}
	return nil
}
func ReadJSONBlockLeaf(text, containerKey, leafKey, fileLabel, endpointKey string) JSONLeafRead {
	_, container, reason := jsonContainer(text, containerKey, fileLabel)
	if reason != "" {
		return JSONLeafRead{Kind: jsonLeafRefused, Reason: reason}
	}
	m, ok := jsonFind(container, leafKey)
	if !ok {
		return JSONLeafRead{Kind: jsonLeafAbsent}
	}
	return JSONLeafRead{Kind: jsonLeafPresent, Endpoint: jsonEndpoint(text, m.value, endpointKey)}
}
func UpsertJSONScalarKeys(text, containerKey, fileLabel string, entries []JSONScalarEntry) JSONPatch {
	return UpsertJSONScalarKeysForced(text, containerKey, fileLabel, entries, false)
}
func UpsertJSONScalarKeysForced(text, containerKey, fileLabel string, entries []JSONScalarEntry, force bool) JSONPatch {
	root, container, reason := jsonContainer(text, containerKey, fileLabel)
	if reason != "" {
		return jsonPatchRefused(reason)
	}
	owned := false
	var foreign []string
	for _, e := range entries {
		if m, ok := jsonFind(container, e.Key); ok {
			var s string
			if json.Unmarshal([]byte(text[m.value.start:m.value.end]), &s) != nil {
				return jsonPatchRefused("prism: " + fileLabel + " patch refused: " + e.Key + " is not a string")
			}
			if s == e.Value {
				owned = true
			} else {
				foreign = append(foreign, e.Key)
			}
		}
	}
	if len(foreign) > 0 && !owned && !force {
		return jsonPatchForceable("prism: " + fileLabel + " patch refused: user-owned settings " + strings.Join(foreign, ", "))
	}
	next := text
	if container == nil {
		var body []string
		for _, e := range entries {
			body = append(body, "    "+jsonString(e.Key)+": "+jsonString(e.Value))
		}
		value := "{\n" + strings.Join(body, ",\n") + "\n  }"
		if root == nil {
			next = "{\n  " + jsonString(containerKey) + ": " + value + "\n}\n"
		} else {
			next = jsonInsert(text, root, containerKey, value)
		}
	} else {
		for _, e := range entries {
			_, container, _ = jsonContainer(next, containerKey, fileLabel)
			next = jsonUpsert(next, container, e.Key, jsonString(e.Value))
		}
	}
	return jsonResult(text, next)
}
func RemoveJSONScalarKeys(text, containerKey, fileLabel string, keys []string) JSONPatch {
	next := text
	for _, key := range keys {
		root, container, reason := jsonContainer(next, containerKey, fileLabel)
		if reason != "" {
			return jsonPatchRefused(reason)
		}
		if container == nil {
			continue
		}
		if _, ok := jsonFind(container, key); !ok {
			continue
		}
		if len(container.members) == 1 && jsonContainerHasOnlyMember(next, container) {
			next = jsonRemoveMember(next, root, containerKey)
		} else {
			next = jsonRemoveMember(next, container, key)
		}
	}
	return jsonResult(text, next)
}
func ReadJSONScalarKeys(text, containerKey, endpointKey, fileLabel string, keys []string) JSONLeafRead {
	_, container, reason := jsonContainer(text, containerKey, fileLabel)
	if reason != "" {
		return JSONLeafRead{Kind: jsonLeafRefused, Reason: reason}
	}
	for _, key := range keys {
		if _, ok := jsonFind(container, key); ok {
			return JSONLeafRead{Kind: jsonLeafPresent, Endpoint: jsonEndpoint(text, container, endpointKey)}
		}
	}
	return JSONLeafRead{Kind: jsonLeafAbsent}
}
