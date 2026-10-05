package agentinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type releaseCheck struct {
	before, expected, verification, note string
	entryReal                            string
	current                              bool
}

func (m *Manager) releaseJSON(ctx context.Context, raw string, dst any) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return fmt.Errorf("release URL must be absolute HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	client := *m.http
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" || req.URL.Host != u.Host || req.URL.User != nil {
			return fmt.Errorf("untrusted release metadata redirect")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("release check returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("release metadata exceeds limit")
	}
	return json.Unmarshal(data, dst)
}
func (m *Manager) npmRelease(ctx context.Context, pkg, channel string) (string, error) {
	var body struct {
		Version string `json:"version"`
	}
	err := m.releaseJSON(ctx, "https://registry.npmjs.org/"+url.PathEscape(pkg)+"/"+channel, &body)
	if err != nil {
		return "", err
	}
	if normalizedVersion(body.Version) == "" {
		return "", fmt.Errorf("release response has no valid version")
	}
	return strings.TrimPrefix(body.Version, "v"), nil
}
func (m *Manager) checkRelease(ctx context.Context, def Definition, action preparedAction) (releaseCheck, error) {
	before, _, err := m.versionAt(ctx, def, action.target.entry, m.verifyTimeout)
	if err != nil {
		return releaseCheck{}, err
	}
	real, err := m.eval(action.target.entry)
	if err != nil {
		return releaseCheck{}, err
	}
	check := releaseCheck{before: before, verification: "release", entryReal: absolute(real)}
	if obs := m.observeEntry(def, action.target.entry); obs.reason != "" || obs.target != action.target {
		return check, fmt.Errorf("selected owner changed before release check")
	}
	if action.target.source == SourceBrew {
		check.expected, err = m.brewRelease(ctx, action)
		if err == nil {
			upstream, upstreamErr := m.upstreamRelease(ctx, def, before)
			if upstreamErr == nil && compareVersions(upstream, check.expected) > 0 {
				check.note = "Upstream " + upstream + " is not yet available from Homebrew; verifying the Homebrew release " + check.expected
			}
		}
		return check, err
	}
	switch def.Key {
	case "claude":
		out, err := m.readCommand(ctx, action.env, action.target.entry, "doctor")
		if err != nil {
			return check, err
		}
		channel := ""
		for _, line := range strings.Split(out, "\n") {
			if _, after, ok := strings.Cut(strings.ToLower(line), "auto-update channel:"); ok {
				value := strings.TrimSpace(after)
				if value == "stable" || value == "latest" {
					channel = value
				}
			}
		}
		if channel == "" {
			check.verification, check.note = "client", "Client doctor did not identify a release channel"
			return check, nil
		}
		check.expected, err = m.npmRelease(ctx, "@anthropic-ai/claude-code", channel)
		return check, err
	case "grok":
		out, err := m.readCommand(ctx, action.env, action.target.entry, "update", "--check")
		if err != nil {
			return check, err
		}
		check.expected = parsedVersion(out, true)
		if check.expected == "" {
			return check, fmt.Errorf("update check returned no valid release version")
		}
	case "hermes":
		out, err := m.readCommand(ctx, action.env, action.target.entry, "update", "--check")
		if err != nil {
			return check, err
		}
		check.verification, check.note = "client", "Client tracks commits; verified the runnable client version"
		recognized := false
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimLeftFunc(line, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') })
			if line == "Already up to date." {
				check.current, recognized = true, true
				break
			}
			if strings.HasPrefix(line, "Update available: ") && strings.Contains(line, " behind ") || strings.HasPrefix(line, "Update available (behind ") && strings.HasSuffix(line, ").") {
				recognized = true
				break
			}
		}
		if !recognized {
			return check, fmt.Errorf("update check returned no recognized commit verdict")
		}
	case "omp":
		check.verification, check.note = "client", "Verified the selected installation; no release feed is configured"
	default:
		check.expected, err = m.upstreamRelease(ctx, def, before)
		return check, err
	}
	return check, nil
}
func (m *Manager) upstreamRelease(ctx context.Context, def Definition, before string) (string, error) {
	switch def.Key {
	case "pi":
		return m.npmRelease(ctx, "@earendil-works/pi-coding-agent", "latest")
	case "opencode":
		pkg := "opencode-ai"
		if strings.HasPrefix(normalizedVersion(before), "v2.") || compareVersions(before, "3.0.0") >= 0 {
			pkg = "@opencode/cli"
		}
		return m.npmRelease(ctx, pkg, "latest")
	case "codex":
		var release struct {
			Tag string `json:"tag_name"`
		}
		if err := m.releaseJSON(ctx, "https://api.github.com/repos/openai/codex/releases/latest", &release); err != nil {
			return "", err
		}
		if !strings.HasPrefix(release.Tag, "rust-v") || normalizedVersion(strings.TrimPrefix(release.Tag, "rust-v")) == "" {
			return "", fmt.Errorf("release has no valid version tag")
		}
		return strings.TrimPrefix(release.Tag, "rust-v"), nil
	}
	return "", fmt.Errorf("no upstream release feed")
}
func brewVersion(data []byte, token string, cask bool) (string, error) {
	var body struct {
		Name     string `json:"name"`
		Token    string `json:"token"`
		Version  string `json:"version"`
		Versions struct {
			Stable string `json:"stable"`
		} `json:"versions"`
		Casks    []json.RawMessage `json:"casks"`
		Formulae []json.RawMessage `json:"formulae"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", err
	}
	items := body.Formulae
	if cask {
		items = body.Casks
	}
	if len(items) > 0 {
		for _, item := range items {
			if version, err := brewVersion(item, token, cask); err == nil {
				return version, nil
			}
		}
		return "", fmt.Errorf("Homebrew response has no valid release for %s", token)
	}
	name := body.Name
	if cask {
		name = body.Token
	}
	if name != token {
		return "", fmt.Errorf("Homebrew response does not describe %s", token)
	}
	value := body.Versions.Stable
	if cask {
		value, _, _ = strings.Cut(body.Version, ",")
	}
	if normalizedVersion(value) == "" {
		return "", fmt.Errorf("Homebrew response has no valid release")
	}
	return value, nil
}
func (m *Manager) brewRelease(ctx context.Context, action preparedAction) (string, error) {
	t := action.target
	kind := "formula"
	if t.cask {
		kind = "cask"
	}
	var body json.RawMessage
	apiErr := m.releaseJSON(ctx, "https://formulae.brew.sh/api/"+kind+"/"+url.PathEscape(t.name)+".json", &body)
	api, err := brewVersion(body, t.name, t.cask)
	apiErr = errors.Join(apiErr, err)
	env := copyEnv(action.env)
	env["HOMEBREW_NO_AUTO_UPDATE"] = "1"
	out, localErr := m.readCommand(ctx, env, action.argv[0], "info", "--"+kind, "--json=v2", t.name)
	local, err := brewVersion([]byte(out), t.name, t.cask)
	localErr = errors.Join(localErr, err)
	if apiErr == nil && localErr == nil {
		if compareVersions(local, api) > 0 {
			return local, nil
		}
		return api, nil
	}
	if apiErr == nil {
		return api, nil
	}
	if localErr == nil {
		return local, nil
	}
	return "", errors.Join(apiErr, localErr)
}
