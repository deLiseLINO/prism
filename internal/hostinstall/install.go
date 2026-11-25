// Package hostinstall installs, updates, or restarts prism on a remote macOS
// or Linux machine over SSH, so it runs this daemon's version.
package hostinstall

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
)

//go:embed remote-install.sh
var script string

const (
	scriptTimeout  = 6 * time.Minute
	uploadTimeout  = 5 * time.Minute
	fetchTimeout   = 5 * time.Minute
	logTailTimeout = 15 * time.Second
	verifyTimeout  = 30 * time.Second
	needArchive    = 20
	incomingDir    = ".prism/remote/incoming"
	maxArchive     = 256 << 20
)

const (
	// ReleaseBaseURL is where the prism release archives live.
	ReleaseBaseURL = "https://github.com/deLiseLINO/prism/releases/download"
	// DefaultPort is the remote loopback port prism listens on.
	DefaultPort = 10200
)

// Phase names the install step a failure happened in.
type Phase string

const (
	PhaseProbe    Phase = "probe"
	PhaseUpload   Phase = "upload"
	PhaseStart    Phase = "start"
	PhaseVerify   Phase = "verify"
	PhaseRegister Phase = "register"
)

// Reply is the outcome of one install, in the shape of the contracts'
// HostInstallReply.
type Reply struct {
	OK      bool    `json:"ok"`
	Host    string  `json:"host"`
	Version string  `json:"version,omitempty"`
	Adopted bool    `json:"adopted,omitempty"`
	Service *string `json:"service,omitempty"`
	Notice  *string `json:"notice,omitempty"`
	Phase   Phase   `json:"phase,omitempty"`
	Reason  string  `json:"reason,omitempty"`
}

// Installer holds what every install needs. The zero value is not usable.
type Installer struct {
	SSH string
	// Version is the prism version remote machines run: this daemon's.
	Version string
	// ReleaseBaseURL holds <base>/v<version>/prism_<version>_<target>.tar.gz.
	ReleaseBaseURL string
	// Port is the remote loopback port prism listens on.
	Port int
	// Binary returns a prism binary for target when no release exists for
	// Version, e.g. in development builds.
	Binary func(target string) (string, bool)
	HTTP   *http.Client
}

// LocalBinary finds a prism binary for target next to this daemon's
// executable. The desktop app ships one per platform as
// prism-<os>-<arch>; a daemon for target itself also works.
func LocalBinary(target string) (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	sibling := filepath.Join(filepath.Dir(exe), "prism-"+strings.Replace(target, "_", "-", 1))
	if info, err := os.Stat(sibling); err == nil && info.Mode().IsRegular() {
		return sibling, true
	}
	if target == runtime.GOOS+"_"+runtime.GOARCH {
		return exe, true
	}
	return "", false
}

// Install runs the install for host at address. register saves the remote
// daemon port in the config once prism answers there. It is safe to repeat:
// a machine already on Version with its service in place is left running.
func (in *Installer) Install(ctx context.Context, host, address string, register func(port int) error) Reply {
	fail := func(phase Phase, reason string) Reply {
		return Reply{Host: host, Phase: phase, Reason: reason}
	}
	if strings.HasPrefix(address, "-") {
		return fail(PhaseProbe, "the SSH address must not start with -")
	}
	base := []string{"--version", in.Version, "--port", fmt.Sprint(in.Port), "--base-url", in.ReleaseBaseURL}
	run := in.runScript(ctx, address, base)
	if !run.ok && run.code == needArchive {
		target := run.lines["target"]
		if !validTarget(target) {
			return fail(PhaseProbe, "the machine did not report its platform")
		}
		args, err := in.uploadPrism(ctx, address, target)
		if err != nil {
			return fail(PhaseUpload, err.Error())
		}
		run = in.runScript(ctx, address, append(base, args...))
	}
	if !run.ok {
		return fail(PhaseStart, run.reason)
	}
	version := run.lines["version"]
	if run.lines["state"] == "adopted" {
		if err := register(in.Port); err != nil {
			return fail(PhaseRegister, "prism runs on the machine, but saving its port in the config failed: "+err.Error())
		}
		reply := Reply{OK: true, Host: host, Version: version, Adopted: true}
		if version != in.Version {
			notice := fmt.Sprintf("prism %s already runs on this machine and was not started by Prism, so it stays as is. Update it there to %s.", version, in.Version)
			reply.Notice = &notice
		}
		return reply
	}
	service := run.lines["service"]
	if service != "systemd" && service != "launchd" {
		service = "none"
	}
	healthy, err := in.awaitHealth(ctx, address)
	if err != nil {
		reason := fmt.Sprintf("prism started but did not answer within %s; click Install again to re-check", verifyTimeout)
		if tail := in.logTail(ctx, address); tail != "" {
			reason += "\n" + tail
		}
		return fail(PhaseVerify, reason)
	}
	if err := register(in.Port); err != nil {
		return fail(PhaseRegister, "prism runs on the machine, but saving its port in the config failed: "+err.Error())
	}
	return Reply{OK: true, Host: host, Version: healthy, Service: &service, Notice: serviceNotice(service, run.lines["linger"])}
}

func serviceNotice(service, linger string) *string {
	var notice string
	switch {
	case service == "none":
		notice = "This machine has no systemd user session or launchd, so prism does not start by itself after a reboot. Prism starts it again the next time the desktop app starts."
	case service == "systemd" && linger != "yes":
		notice = "prism starts after a reboot only once you sign in on that machine. To start it at boot, run sudo loginctl enable-linger $USER there."
	default:
		return nil
	}
	return &notice
}

func validTarget(target string) bool {
	switch target {
	case "linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64":
		return true
	}
	return false
}

func (in *Installer) ssh(ctx context.Context, address string, args ...string) *exec.Cmd {
	flags := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=10", address}
	cmd := exec.CommandContext(ctx, in.SSH, append(flags, args...)...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	return cmd
}

type scriptRun struct {
	ok     bool
	code   int
	lines  map[string]string
	reason string
}

// runScript pipes the install script to the machine's sh. Paths in args are
// relative to the remote home, where an ssh command starts.
func (in *Installer) runScript(ctx context.Context, address string, args []string) scriptRun {
	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()
	quoted := []string{"sh", "-s", "--"}
	quoted = append(quoted, args...)
	for i, arg := range quoted {
		quoted[i] = shellQuote(arg)
	}
	cmd := in.ssh(ctx, address, strings.Join(quoted, " "))
	cmd.Stdin = strings.NewReader(script)
	var stdout bytes.Buffer
	stderr := &tail{max: 4000}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	err := cmd.Run()
	lines := map[string]string{}
	sc := bufio.NewScanner(&stdout)
	for sc.Scan() {
		if key, value, ok := strings.Cut(sc.Text(), "="); ok && strings.TrimSpace(key) != "" {
			lines[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if err == nil {
		return scriptRun{ok: true, lines: lines}
	}
	code := -1
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	return scriptRun{code: code, lines: lines, reason: scriptFailure(err, stderr.String())}
}

var windowsShell = regexp.MustCompile(`(?i)is not recognized as an internal or external command|The system cannot find the path`)

func scriptFailure(err error, stderr string) string {
	var message string
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "prism-remote: ") {
			message = strings.TrimPrefix(line, "prism-remote: ")
		}
	}
	if message != "" {
		return message
	}
	if windowsShell.MatchString(stderr) {
		return "this machine runs Windows; prism can be installed on macOS and Linux machines for now"
	}
	return withStderr("ssh "+err.Error(), stderr)
}

func withStderr(reason, stderr string) string {
	if s := strings.TrimSpace(stderr); s != "" {
		return reason + ": " + s
	}
	return reason
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// uploadPrism puts prism for target on the machine when it cannot download
// the release: the release archive fetched here first, else a local binary.
// It returns the script arguments that point at the upload.
func (in *Installer) uploadPrism(ctx context.Context, address, target string) ([]string, error) {
	name, archive, sum, fetchErr := in.fetchRelease(ctx, target)
	if fetchErr == nil {
		remote := incomingDir + "/" + name
		if err := in.upload(ctx, address, bytes.NewReader(archive), remote); err != nil {
			return nil, err
		}
		return []string{"--archive", remote, "--sha256", sum}, nil
	}
	path, ok := in.Binary(target)
	if !ok {
		return nil, fmt.Errorf("the machine cannot download prism %s for %s, and this machine could not either: %v", in.Version, target, fetchErr)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading the local prism binary failed: %w", err)
	}
	defer f.Close()
	remote := incomingDir + "/prism"
	if err := in.upload(ctx, address, f, remote); err != nil {
		return nil, err
	}
	return []string{"--binary", remote}, nil
}

func (in *Installer) fetchRelease(ctx context.Context, target string) (string, []byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	name := fmt.Sprintf("prism_%s_%s.tar.gz", in.Version, target)
	dir := fmt.Sprintf("%s/v%s/", in.ReleaseBaseURL, in.Version)
	sums, err := in.get(ctx, dir+"SHA256SUMS", 1<<20)
	if err != nil {
		return "", nil, "", fmt.Errorf("the prism %s release is not available: %w", in.Version, err)
	}
	var want string
	for _, line := range strings.Split(string(sums), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && (fields[1] == name || fields[1] == "*"+name) {
			want = fields[0]
		}
	}
	if want == "" {
		return "", nil, "", fmt.Errorf("the prism %s release has no build for %s", in.Version, target)
	}
	archive, err := in.get(ctx, dir+name, maxArchive)
	if err != nil {
		return "", nil, "", fmt.Errorf("downloading %s failed: %w", name, err)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return "", nil, "", fmt.Errorf("checksum mismatch for %s: expected %s, got %x", name, want, got)
	}
	return name, archive, want, nil
}

func (in *Installer) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := in.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, nil
}

func (in *Installer) upload(ctx context.Context, address string, body io.Reader, remote string) error {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	dir := remote[:strings.LastIndex(remote, "/")]
	cmd := in.ssh(ctx, address, "mkdir -p "+shellQuote(dir)+" && cat > "+shellQuote(remote))
	cmd.Stdin = body
	stderr := &tail{max: 2000}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("uploading prism failed: %s", withStderr("ssh "+err.Error(), stderr.String()))
	}
	return nil
}

// awaitHealth polls the remote daemon's health through ssh until it reports
// its version or verifyTimeout passes.
func (in *Installer) awaitHealth(ctx context.Context, address string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	transport := hostsync.SSHTransport(in.SSH)
	host := config.Host{Address: address, DaemonPort: in.Port}
	for {
		version, err := in.health(ctx, transport, host)
		if err == nil {
			return version, nil
		}
		select {
		case <-ctx.Done():
			return "", err
		case <-time.After(time.Second):
		}
	}
}

func (in *Installer) health(ctx context.Context, transport hostsync.Transport, host config.Host) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", host.DaemonPort), nil)
	if err != nil {
		return "", err
	}
	resp, err := transport(ctx, host, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil || body.Status != "ok" || body.Version == "" {
		return "", fmt.Errorf("health check returned HTTP %d", resp.StatusCode)
	}
	return body.Version, nil
}

func (in *Installer) logTail(ctx context.Context, address string) string {
	ctx, cancel := context.WithTimeout(ctx, logTailTimeout)
	defer cancel()
	out, _ := in.ssh(ctx, address, "tail -c 2000 .prism/remote/prism.log 2>/dev/null").Output()
	return strings.TrimSpace(string(out))
}

// tail keeps the last max bytes written to it.
type tail struct {
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.buf) }
