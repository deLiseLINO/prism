package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/deLiseLINO/prism/internal/buildinfo"
	"github.com/deLiseLINO/prism/internal/cli"
	"github.com/deLiseLINO/prism/internal/daemon"
	"github.com/deLiseLINO/prism/internal/service"
	"github.com/deLiseLINO/prism/internal/tui"
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
	case "version", "--version", "-v":
		fmt.Println(buildinfo.Version)
		return 0
	case "--compact":
		return runTUI(args)
	}
	return cli.Run(args)
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
	return tui.Run(args, os.Stderr)
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
