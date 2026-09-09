package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"prism/internal/tui"
)

const defaultBaseURL = "http://127.0.0.1:10200"

func main() {
	baseURL := flag.String("base-url", envOr("PRISM_URL", defaultBaseURL), "prism daemon base URL")
	mgmtToken := flag.String("mgmt-token", os.Getenv("PRISM_MGMT_TOKEN"), "management bearer token")
	compact := flag.Bool("compact", false, "start in compact view")
	flag.Parse()

	client := tui.NewHTTPClient(strings.TrimRight(*baseURL, "/"), *mgmtToken, nil)
	program := tea.NewProgram(
		tui.InitialModel(client, *compact),
		tea.WithAltScreen(),
	)
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "prismtui: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
