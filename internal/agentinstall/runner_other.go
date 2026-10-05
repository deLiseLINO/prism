//go:build !unix

package agentinstall

import "os/exec"

func runCommand(cmd *exec.Cmd) error { return cmd.Run() }
