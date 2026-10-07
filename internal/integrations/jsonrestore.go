package integrations

import (
	"encoding/json"
	"fmt"
	"strings"
)

func jsonComparable(text string, n *jsonNode) string {
	if n == nil {
		return "absent"
	}
	if n.object {
		values := make(map[string]string, len(n.members))
		for _, m := range n.members {
			values[m.key] = jsonComparable(text, m.value)
		}
		b, _ := json.Marshal(values)
		return string(b)
	}
	if n.start < len(text) && text[n.start] == '[' {
		values := make([]string, 0, len(n.members))
		for _, m := range n.members {
			values = append(values, jsonComparable(text, m.value))
		}
		b, _ := json.Marshal(values)
		return string(b)
	}
	return strings.TrimSpace(text[n.start:n.end])
}

func mergeJSONValues(written, current, original string) (string, error) {
	w, err := parseJSONObject(written)
	if err != nil {
		return "", err
	}
	c, err := parseJSONObject(current)
	if err != nil {
		return "", err
	}
	o, err := parseJSONObject(original)
	if err != nil {
		return "", err
	}
	if w == nil || c == nil {
		return "", fmt.Errorf("missing JSON object")
	}
	if o == nil {
		original = "{}"
		o, _ = parseJSONObject(original)
	}
	next := current
	keys := make(map[string]bool)
	for _, m := range w.members {
		keys[m.key] = true
	}
	for _, m := range o.members {
		keys[m.key] = true
	}
	for key := range keys {
		wm, _ := jsonFind(w, key)
		om, _ := jsonFind(o, key)
		if jsonComparable(written, wm.value) == jsonComparable(original, om.value) {
			continue
		}
		cn, err := parseJSONObject(next)
		if err != nil {
			return "", err
		}
		cm, cPresent := jsonFind(cn, key)
		if wm.value != nil && om.value != nil && cm.value != nil && wm.value.object && om.value.object && cm.value.object {
			merged, err := mergeJSONValues(written[wm.value.start:wm.value.end], next[cm.value.start:cm.value.end], original[om.value.start:om.value.end])
			if err != nil {
				return "", err
			}
			next = jsonUpsert(next, cn, key, merged)
			continue
		}
		if om.value == nil && wm.value != nil && cm.value != nil && wm.value.object && cm.value.object {
			merged, err := mergeJSONValues(written[wm.value.start:wm.value.end], next[cm.value.start:cm.value.end], "{}")
			if err != nil {
				return "", err
			}
			node, _ := parseJSONObject(merged)
			if len(node.members) > 0 || strings.Contains(merged, "//") || strings.Contains(merged, "/*") {
				next = jsonUpsert(next, cn, key, merged)
			} else {
				next = jsonRemoveMember(next, cn, key)
			}
			continue
		}
		observed := jsonComparable(next, cm.value)
		if observed != jsonComparable(written, wm.value) && observed != jsonComparable(original, om.value) {
			return "", fmt.Errorf("written value changed at %s", key)
		}
		if om.value != nil {
			next = jsonUpsert(next, cn, key, original[om.value.start:om.value.end])
		} else if cPresent {
			next = jsonRemoveMember(next, cn, key)
		}
	}
	return next, nil
}
