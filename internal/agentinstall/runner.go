package agentinstall

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"

	"prism/internal/integrations"
)

// Runner executes one install/update argv with the daemon's environment,
// merging combined output into dst. The seam keeps manager tests process-free.
type Runner interface {
	Run(ctx context.Context, env integrations.Env, argv []string, dst io.Writer) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, env integrations.Env, argv []string, dst io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = envPairs(env)
	cmd.Stdout = dst
	cmd.Stderr = dst
	return cmd.Run()
}

func envPairs(env integrations.Env) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+env[k])
	}
	return pairs
}

// scriptSizeCap bounds fetched install scripts; vendor installers are
// kilobytes, so anything larger is a wrong URL, not a script.
const scriptSizeCap = 4 << 20

// FetchScript downloads an install script to a temp file and returns its
// path; the caller owns removal. Interpreters cannot open URLs, so the
// script install method fetches first and execs the local copy. Only
// absolute HTTPS URLs are accepted and redirects must stay HTTPS, matching
// how Agent Orchestrator runs its official installers.
func FetchScript(ctx context.Context, raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("installer URL must be absolute HTTPS: %q", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return fmt.Errorf("installer download exceeded redirect limit")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("installer redirect must remain HTTPS")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("GET %s: %s", raw, resp.Status)
	}
	f, err := os.CreateTemp("", "prism-script-*")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, scriptSizeCap+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if n > scriptSizeCap {
		os.Remove(f.Name())
		return "", fmt.Errorf("GET %s: response exceeds %d bytes", raw, scriptSizeCap)
	}
	return f.Name(), nil
}
