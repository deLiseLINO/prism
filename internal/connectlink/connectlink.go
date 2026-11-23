// Package connectlink parses and builds connect links, each of which carries
// one custom provider definition. A link comes in two forms that carry the
// same parameters: prism://connect?<parameters> opens the app, and
// https://<host>/connect#<parameters> opens a web page that hands the link to
// the app. The web form keeps the parameters in the fragment, which browsers
// never send, so the page's server and link previews never see the key.
package connectlink

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/deLiseLINO/prism/internal/config"
)

// MaxLength is the longest link Parse accepts, in bytes.
const MaxLength = 8192

const version = "1"

// WebPath is the path of the web form of a link.
const WebPath = "/connect"

const errStart = "link must start with prism://connect or https://<host>/connect#"

// Link is one provider definition. Endpoints[0] is the base URL; the rest
// are fallback base URLs in order.
type Link struct {
	ID           string
	Wire         string
	Endpoints    []string
	Spread       bool
	Models       []string
	DefaultModel string
	Key          string
}

var knownParams = []string{"v", "id", "wire", "endpoint", "mode", "model", "defaultModel", "key"}

var repeatableParams = []string{"endpoint", "model"}

// Parse validates raw and returns the provider it describes. Error texts are
// shown to users as they are, so none of them may quote the key or the raw
// link, which carries it.
func Parse(raw string) (Link, error) {
	if len(raw) > MaxLength {
		return Link{}, fmt.Errorf("link is longer than %d bytes", MaxLength)
	}
	raw, err := appLink(raw)
	if err != nil {
		return Link{}, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Link{}, errors.New("link is not a valid URL")
	}
	if !strings.EqualFold(u.Scheme, "prism") {
		return Link{}, errors.New(errStart)
	}
	if u.Host != "connect" || u.User != nil {
		return Link{}, errors.New(errStart)
	}
	if u.Path != "" && u.Path != "/" {
		return Link{}, errors.New("link must have no path after prism://connect")
	}
	// A bare trailing '#' leaves Fragment empty but still cuts the key off.
	if strings.Contains(raw, "#") {
		return Link{}, errors.New("link must not have a fragment")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Link{}, errors.New("link parameters are not valid percent-encoded query values")
	}
	names := make([]string, 0, len(q))
	for name := range q {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !slices.Contains(knownParams, name) {
			// Never name the parameter: an unencoded '&' in the key turns the
			// rest of the key into a parameter name.
			return Link{}, errors.New("link has an unsupported parameter")
		}
		if len(q[name]) > 1 && !slices.Contains(repeatableParams, name) {
			return Link{}, fmt.Errorf("link parameter %q appears more than once", name)
		}
	}

	if !q.Has("v") {
		return Link{}, errors.New("link has no version (v)")
	}
	if v := q.Get("v"); v != version {
		return Link{}, fmt.Errorf("link version %q is not supported, this prism reads version %s", v, version)
	}

	l := Link{ID: q.Get("id"), Wire: q.Get("wire")}
	if l.ID == "" {
		return Link{}, errors.New("link has no provider id")
	}
	if !config.ValidProviderID(l.ID) {
		return Link{}, fmt.Errorf("provider id %q may use only letters, digits, '_' and '-'", l.ID)
	}
	if l.Wire == "" {
		return Link{}, errors.New("link has no wire")
	}
	if !config.Wire(l.Wire).Custom() {
		return Link{}, fmt.Errorf("wire %q is not supported, use responses, chat or messages", l.Wire)
	}

	l.Endpoints = q["endpoint"]
	if len(l.Endpoints) == 0 {
		return Link{}, errors.New("link has no endpoint")
	}
	seen := make([]string, 0, len(l.Endpoints))
	for _, endpoint := range l.Endpoints {
		e, err := url.Parse(endpoint)
		if err != nil || (e.Scheme != "http" && e.Scheme != "https") || e.Host == "" || endpoint != strings.TrimSpace(endpoint) {
			return Link{}, fmt.Errorf("endpoint %q is not an absolute http or https URL", endpoint)
		}
		key := strings.TrimRight(endpoint, "/")
		if slices.Contains(seen, key) {
			return Link{}, fmt.Errorf("endpoint %q repeats another endpoint", endpoint)
		}
		seen = append(seen, key)
	}

	switch mode := q.Get("mode"); mode {
	case "", string(config.EndpointModeFailover):
	case string(config.EndpointModeSpread):
		l.Spread = true
	default:
		return Link{}, fmt.Errorf("endpoint mode %q is not supported, use failover or spread", mode)
	}

	l.Models = q["model"]
	if slices.Contains(l.Models, "") {
		return Link{}, errors.New("link has an empty model")
	}
	if q.Has("defaultModel") {
		l.DefaultModel = q.Get("defaultModel")
		if l.DefaultModel == "" {
			return Link{}, errors.New("link has an empty default model")
		}
	}
	if q.Has("key") {
		l.Key = q.Get("key")
		if l.Key == "" {
			return Link{}, errors.New("link has an empty key")
		}
	}
	return l, nil
}

// appLink rewrites the web form of a link to the prism:// form and returns
// any other link unchanged.
func appLink(raw string) (string, error) {
	const scheme = "https://"
	if len(raw) < len(scheme) || !strings.EqualFold(raw[:len(scheme)], scheme) {
		return raw, nil
	}
	page, params, found := strings.Cut(raw, "#")
	u, err := url.Parse(page)
	if err != nil || u.Host == "" || u.User != nil || strings.TrimSuffix(u.Path, "/") != WebPath || u.RawQuery != "" || u.ForceQuery {
		return "", errors.New(errStart)
	}
	if !found || params == "" {
		return "", errors.New("link has no parameters after #")
	}
	return "prism://connect?" + params, nil
}

// String returns the canonical form of l: parameters in a fixed order and
// failover mode left out, so Parse(l.String()) reproduces l.
func (l Link) String() string {
	var b strings.Builder
	b.WriteString("prism://connect?v=" + version)
	add := func(name, value string) {
		b.WriteString("&" + name + "=" + url.QueryEscape(value))
	}
	add("id", l.ID)
	add("wire", l.Wire)
	for _, endpoint := range l.Endpoints {
		add("endpoint", endpoint)
	}
	if l.Spread {
		add("mode", string(config.EndpointModeSpread))
	}
	for _, model := range l.Models {
		add("model", model)
	}
	if l.DefaultModel != "" {
		add("defaultModel", l.DefaultModel)
	}
	if l.Key != "" {
		add("key", l.Key)
	}
	return b.String()
}

// MaskKey returns a preview of key that is safe to show: its first four
// characters and an ellipsis, or only asterisks for a key that short.
func MaskKey(key string) string {
	r := []rune(key)
	switch {
	case len(r) == 0:
		return ""
	case len(r) <= 4:
		return "****"
	}
	return string(r[:4]) + "…"
}
