package conformance

import (
	"regexp"
	"strconv"
	"strings"
)

var arrayIndexRE = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

func Resolve(doc any, selector string) (value any, ok bool) {
	if !strings.HasPrefix(selector, "/") {
		return nil, false
	}
	if selector == "/" {
		return doc, true
	}
	tokens := strings.Split(selector[1:], "/")
	cur := doc
	for _, t := range tokens {
		t = decodeToken(t)
		if t == "-" {
			return nil, false
		}
		if arr, isArr := cur.([]any); isArr {
			if !arrayIndexRE.MatchString(t) {
				return nil, false
			}
			idx, _ := strconv.Atoi(t)
			if idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
			continue
		}
		if cur == nil {
			return nil, false
		}
		obj, isObj := cur.(map[string]any)
		if !isObj {
			return nil, false
		}
		v, has := obj[t]
		if !has {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

func Exists(doc any, selector string) bool {
	_, ok := Resolve(doc, selector)
	return ok
}

func decodeToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
}
