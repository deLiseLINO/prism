package execution

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

type RequestID string
type SessionKey string
type ThreadKey string

type Facts struct {
	RequestID RequestID
	Client    Client
	Session   SessionKey
	Thread    ThreadKey
	Forward   ForwardSet
}

type Client uint8

const (
	ClientCodex Client = iota + 1
	ClientGrok
	ClientOMP
	ClientAnthropic
)

type ForwardName uint8

const (
	ForwardSessionID ForwardName = iota + 1
	ForwardOriginator
	ForwardUserAgent
)

type ForwardSet struct{ pairs []forwardPair }

type forwardPair struct {
	Name  ForwardName
	Value string
}

var forwardWireNames = map[string]ForwardName{
	"x-session-id": ForwardSessionID,
	"originator":   ForwardOriginator,
	"user-agent":   ForwardUserAgent,
}

var forwardDisplayNames = map[ForwardName]string{
	ForwardSessionID:  "x-session-id",
	ForwardOriginator: "originator",
	ForwardUserAgent:  "user-agent",
}

func NewForwardSet(h http.Header) (ForwardSet, error) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return strings.ToLower(keys[i]) < strings.ToLower(keys[j])
	})
	var pairs []forwardPair
	seen := make(map[ForwardName]bool, len(keys))
	for _, k := range keys {
		lower := strings.ToLower(k)
		n, ok := forwardWireNames[lower]
		if !ok {
			return ForwardSet{}, fmt.Errorf("execution: header %q is not forwardable", k)
		}
		if seen[n] {
			continue
		}
		vals := h[k]
		if len(vals) == 0 {
			continue
		}
		seen[n] = true
		pairs = append(pairs, forwardPair{Name: n, Value: vals[0]})
	}
	return ForwardSet{pairs: pairs}, nil
}

func (f ForwardSet) Get(n ForwardName) (string, bool) {
	for _, p := range f.pairs {
		if p.Name == n {
			return p.Value, true
		}
	}
	return "", false
}

func (f ForwardSet) Redacted() string {
	parts := make([]string, 0, len(f.pairs))
	for _, p := range f.pairs {
		parts = append(parts, forwardDisplayNames[p.Name]+"=<redacted>")
	}
	return strings.Join(parts, ";")
}
