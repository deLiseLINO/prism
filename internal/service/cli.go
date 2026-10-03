package service

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = `Usage: prism service <start|stop|restart|status> [flags]

  start    start the background daemon, or reuse the running one
  stop     stop the background daemon
  restart  stop, then start the background daemon
  status   print the daemon url if it is running this version; exit 1 otherwise

Flags for start and restart are forwarded to the daemon:
  --listen, --config, --credential-store, --webui, --management-token
`

var forwardedFlags = []string{"listen", "config", "credential-store", "webui", "management-token"}

func ParseDaemonFlags(args []string, stderr io.Writer) (Daemon, error) {
	fs := flag.NewFlagSet("prism service", flag.ContinueOnError)
	fs.SetOutput(stderr)
	values := map[string]*string{}
	for _, name := range forwardedFlags {
		values[name] = fs.String(name, "", "daemon flag")
	}
	if err := fs.Parse(args); err != nil {
		return Daemon{}, err
	}
	if fs.NArg() > 0 {
		return Daemon{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	d := Daemon{Listen: DefaultListen}
	fs.Visit(func(f *flag.Flag) {
		d.Args = append(d.Args, "--"+f.Name, f.Value.String())
		if f.Name == "listen" {
			d.Listen = f.Value.String()
		}
	})
	return d, nil
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	svc := New()
	switch args[0] {
	case "start", "restart":
		d, err := ParseDaemonFlags(args[1:], stderr)
		if err != nil {
			return 2
		}
		if args[0] == "restart" {
			if err := svc.Stop(ctx); err != nil {
				return fail(stderr, err)
			}
		}
		url, err := svc.Start(ctx, d)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, url)
		return 0
	case "stop":
		if len(args) > 1 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		if err := svc.Stop(ctx); err != nil {
			return fail(stderr, err)
		}
		return 0
	case "status":
		if len(args) > 1 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		url, ok := svc.Status(ctx)
		if !ok {
			return 1
		}
		fmt.Fprintln(stdout, url)
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "prism service: unknown command %q\n\n%s", args[0], usage)
	return 2
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "prism: %v\n", err)
	return 1
}
