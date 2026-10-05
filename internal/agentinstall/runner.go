package agentinstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

// Runner executes one maintenance command with the daemon's environment.
// Unconfirmed process cleanup must return a CleanupError to retain ownership.
type Runner interface {
	Run(ctx context.Context, env integrations.Env, argv []string, stdout, stderr io.Writer) error
}

type CleanupError struct {
	ProcessGroup int
	Err          error
}

func (e *CleanupError) Error() string {
	return fmt.Sprintf("maintenance cleanup unconfirmed for process group %d: %v; mutations remain blocked until the group stops", e.ProcessGroup, e.Err)
}
func (e *CleanupError) Unwrap() error { return e.Err }

func unconfirmedCleanup(err error) *CleanupError {
	var cleanup *CleanupError
	errors.As(err, &cleanup)
	return cleanup
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, env integrations.Env, argv []string, stdout, stderr io.Writer) error {
	if len(argv) == 0 || argv[0] == "" {
		return fmt.Errorf("empty command")
	}
	path := LookPath(asEnv(env), argv[0], os.Stat)
	if path == "" {
		return fmt.Errorf("executable %q not found in supplied PATH", argv[0])
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.WaitDelay = time.Second
	cmd.Env = envPairs(env)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return runCommand(ctx, cmd)
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
// absolute HTTPS URLs are accepted and redirects must stay HTTPS.
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
