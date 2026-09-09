package agentinstall

import (
	"context"
	"io"
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
