package tui

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const defaultBaseURL = "http://127.0.0.1:10200"

func Run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("prism tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	baseURL := fs.String("base-url", envOr("PRISM_URL", defaultBaseURL), "prism daemon base URL")
	mgmtToken := fs.String("mgmt-token", os.Getenv("PRISM_MGMT_TOKEN"), "management bearer token")
	compact := fs.Bool("compact", false, "start in compact view")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	client := NewHTTPClient(strings.TrimRight(*baseURL, "/"), *mgmtToken, nil)
	program := tea.NewProgram(
		InitialModel(client, *compact),
		tea.WithAltScreen(),
	)
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(stderr, "prism tui: %v\n", err)
		return 1
	}
	return 0
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
