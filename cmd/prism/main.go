package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/deLiseLINO/prism/internal/autostart"
	"github.com/deLiseLINO/prism/internal/buildinfo"
	"github.com/deLiseLINO/prism/internal/cli"
	"github.com/deLiseLINO/prism/internal/daemon"
	"github.com/deLiseLINO/prism/internal/tui"
)

const defaultURL = "http://127.0.0.1:10200"

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
	case "stop":
		return runStop()
	case "version", "--version", "-v":
		fmt.Println(buildinfo.Version)
		return 0
	case "--compact":
		return runTUI(args)
	}
	return cli.Run(args)
}

func baseURL() string {
	if v := strings.TrimSpace(os.Getenv("PRISM_URL")); v != "" {
		return v
	}
	return defaultURL
}

func stateDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".prism")
	}
	return ".prism"
}

func runTUI(args []string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := autostart.New(baseURL(), stateDir()).Ensure(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "prism: %v\n", err)
		return 1
	}
	cancel()
	return tui.Run(args, os.Stderr)
}

func runStop() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := autostart.Stop(ctx, baseURL(), stateDir(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "prism: %v\n", err)
		return 1
	}
	return 0
}
