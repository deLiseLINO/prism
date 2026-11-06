package hostsync

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"

	"github.com/deLiseLINO/prism/internal/config"
)

// SSHTransport reaches the host's daemon through `ssh -W`, which pipes one
// connection to the host's loopback port for the lifetime of the command.
// Nothing listens on either side once the request is done. It honors the
// user's ~/.ssh/config for keys and aliases.
func SSHTransport(sshPath string) Transport {
	return func(ctx context.Context, host config.Host, req *http.Request) (*http.Response, error) {
		cmd := exec.CommandContext(ctx, sshPath,
			"-o", "BatchMode=yes",
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "ConnectTimeout=10",
			"-W", fmt.Sprintf("127.0.0.1:%d", host.DaemonPort),
			host.Address)
		return roundTrip(cmd, req)
	}
}

// roundTrip writes req to cmd's stdin and reads the response from its stdout.
// Stdin stays open until the response is read: closing it early would look
// like a dropped client to the daemon and cancel the request.
func roundTrip(cmd *exec.Cmd, req *http.Request) (*http.Response, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr tailBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	req.Close = true
	resp, body, err := exchange(stdin, stdout, req)
	stdin.Close()
	waitErr := cmd.Wait()
	if err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, fmt.Errorf("ssh: %s", detail)
		}
		if waitErr != nil {
			return nil, fmt.Errorf("ssh: %w", waitErr)
		}
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

func exchange(stdin io.Writer, stdout io.Reader, req *http.Request) (*http.Response, []byte, error) {
	if err := req.Write(stdin); err != nil {
		return nil, nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(stdout), req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	return resp, body, nil
}

// tailBuffer keeps the last 2 KiB of ssh's stderr for error messages.
type tailBuffer struct{ buf []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2048 {
		t.buf = t.buf[len(t.buf)-2048:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
