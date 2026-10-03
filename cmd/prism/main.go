package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/deLiseLINO/prism/internal/buildinfo"
	"github.com/deLiseLINO/prism/internal/cli"
	"github.com/deLiseLINO/prism/internal/daemon"
	"github.com/deLiseLINO/prism/internal/service"
	"github.com/deLiseLINO/prism/internal/tui"
	"github.com/deLiseLINO/prism/internal/update"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		return runTUI(nil)
	}
	switch args[0] {
	case "tui":
		return runTUI(args[1:])
	case "daemon":
		return daemon.Run(args[1:])
	case "service":
		return service.Run(args[1:], os.Stdout, os.Stderr)
	case "upgrade":
		return runUpgradeCommand(args[1:], os.Stdout, os.Stderr)
	case "version", "--version", "-v":
		fmt.Println(buildinfo.Version)
		return 0
	case "--compact":
		return runTUI(args)
	}
	return cli.Run(args)
}

var (
	detectUpdateMethod = update.DetectMethod
	fetchLatestVersion = func(ctx context.Context, method update.Method) (string, error) {
		return update.DefaultSource().FetchLatest(ctx, method)
	}
	runUpgradeFn = update.RunUpgrade
)

func runUpgradeCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "prism: upgrade does not accept additional arguments")
		return 2
	}
	method := detectUpdateMethod()
	currentVersion := buildinfo.Version

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	latestVersion, latestErr := fetchLatestVersion(ctx, method)

	if method == update.MethodUnknown {
		if latestErr != nil {
			fmt.Fprintf(stderr, "warning: failed to resolve latest version: %v\n", latestErr)
		}
		fmt.Fprintln(stdout, update.ManualUpgradeInstructions(currentVersion, latestVersion))
		return 1
	}
	if latestErr == nil && !update.IsNewer(latestVersion, currentVersion) {
		fmt.Fprintf(stdout, "prism is already up to date (%s)\n", currentVersion)
		return 0
	}
	if latestErr != nil {
		fmt.Fprintf(stderr, "warning: failed to resolve latest version: %v\n", latestErr)
	}
	if err := runUpgradeFn(method, latestVersion, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "prism: %v\n", err)
		return 1
	}
	return 0
}

func runTUI(args []string) int {
	target, err := managedDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "prism: %v\n", err)
		return 1
	}
	if target != nil {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		daemonURL, err := service.New().Start(ctx, *target)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "prism: %v\n", err)
			return 1
		}
		args = append([]string{"--base-url", daemonURL}, args...)
	}
	return tui.Run(args, os.Stdout, os.Stderr)
}

func managedDaemon() (*service.Daemon, error) {
	raw := strings.TrimSpace(os.Getenv("PRISM_URL"))
	if raw == "" {
		return &service.Daemon{Listen: service.DefaultListen}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid PRISM_URL %q", raw)
	}
	if !isLocalHost(u.Hostname()) {
		return nil, nil
	}
	if u.Port() == "" {
		return nil, fmt.Errorf("PRISM_URL %q has no port; cannot start a daemon", raw)
	}
	listen := u.Host
	return &service.Daemon{Listen: listen, Args: []string{"--listen", listen}}, nil
}

func isLocalHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
