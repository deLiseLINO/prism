package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// Exit codes are stable contract: scripts and the verification skill branch on
// them. 0 success, 1 generic operational failure, 2 usage/parse failure,
// 3 stale generation/version CAS conflict, 4 refused (integration refusals),
// 5 daemon unreachable, 6 malformed server response.
const (
	exitOK        = 0
	exitFailure   = 1
	exitUsage     = 2
	exitConflict  = 3
	exitRefused   = 4
	exitUnreach   = 5
	exitMalformed = 6
)

// cliRuntime carries every ambient dependency a command handler needs;
// handlers never reach for os.Stdout, os.Getenv, time.Now, or exec directly.
type cliRuntime struct {
	baseURL string
	env     map[string]string
	stdout  io.Writer
	stderr  io.Writer
	client  *client
	// urlOpener opens the system browser for auth login. Injected so tests
	// never exec anything and --no-open never depends on the platform.
	urlOpener func(ctx context.Context, url string) error
}

func newRuntime() *cliRuntime {
	baseURL := "http://127.0.0.1:10200"
	if v, ok := os.LookupEnv("PRISM_URL"); ok && v != "" {
		baseURL = v
	}
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return &cliRuntime{
		baseURL: baseURL,
		env:     env,
		stdout:  os.Stdout,
		stderr:  os.Stderr,
		client:  newClient(baseURL),
	}
}

// exitError couples a typed process exit code with the message users see.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func exitErr(code int, format string, a ...any) error {
	return &exitError{code: code, msg: fmt.Sprintf(format, a...)}
}

// signalContext returns a context cancelled on the first SIGINT/SIGTERM so
// polling loops and HTTP calls stop promptly on Ctrl-C.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}

// run executes one command line and returns the process exit code.
func run(rt *cliRuntime, args []string) int {
	cmd, err := parseCommand(args)
	if err != nil {
		var usage *usageError
		if errors.As(err, &usage) {
			fmt.Fprintf(rt.stderr, "prismctl: %v\n\n%s", usage.msg, usage.help)
		} else {
			fmt.Fprintf(rt.stderr, "prismctl: %v\n", err)
		}
		return exitUsage
	}
	ctx, cancel := signalContext()
	defer cancel()
	err = cmd.run(ctx, rt)
	if err == nil {
		return exitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		fmt.Fprintf(rt.stderr, "prismctl: %s\n", ee.msg)
		return ee.code
	}
	fmt.Fprintf(rt.stderr, "prismctl: %v\n", err)
	return exitFailure
}

func main() {
	rt := newRuntime()
	os.Exit(run(rt, os.Args[1:]))
}
