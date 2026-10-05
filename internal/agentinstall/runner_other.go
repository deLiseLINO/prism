//go:build !unix

package agentinstall

import (
	"context"
	"os/exec"
)

func runCommand(ctx context.Context, cmd *exec.Cmd) error { return cmd.Run() }
