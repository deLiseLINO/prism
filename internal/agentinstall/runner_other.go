//go:build !darwin && !linux

package agentinstall

import (
	"context"
	"io"
	"os/exec"
)

func runOwned(ctx context.Context, path string, env, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, stdout, stderr
	return cmd.Run()
}
