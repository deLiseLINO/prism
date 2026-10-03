package tui

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deLiseLINO/prism/internal/buildinfo"
	"github.com/deLiseLINO/prism/internal/service"
	"github.com/deLiseLINO/prism/internal/update"
)

const defaultBaseURL = "http://127.0.0.1:10200"

func Run(args []string, stdout, stderr io.Writer) int {
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
	model := InitialModel(client, *compact)
	stateDir := service.StateDir()
	method := update.DetectMethod()
	currentVersion := buildinfo.Version
	state := update.LoadState(stateDir)
	enabled := model.Settings.CheckForUpdateOnStartup
	latest, _ := update.ShouldPrompt(enabled, state, currentVersion, method)
	model = model.WithStartupUpdate(latest, method, stateDir)

	program := tea.NewProgram(model, tea.WithAltScreen())
	if enabled && update.ShouldRefresh(state, time.Now()) {
		go refreshUpdateState(program, update.DefaultSource(), stateDir, state, method, currentVersion)
	}
	final, err := program.Run()
	if err != nil {
		fmt.Fprintf(stderr, "prism tui: %v\n", err)
		return 1
	}
	if finalModel, ok := final.(Model); ok {
		if pendingMethod, version, ok := finalModel.PendingUpdate(); ok {
			if err := runUpgrade(pendingMethod, version, stdout, stderr); err != nil {
				fmt.Fprintf(stderr, "prism tui: %v\n", err)
				return 1
			}
		}
	}
	return 0
}

var runUpgrade = update.RunUpgrade

func refreshUpdateState(program *tea.Program, source update.Source, stateDir string, state update.State, method update.Method, currentVersion string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	refreshed, err := source.Refresh(ctx, state, method)
	if err != nil {
		return
	}
	refreshed.DismissedVersion = update.LoadState(stateDir).DismissedVersion
	if err := update.SaveState(stateDir, refreshed); err != nil {
		return
	}
	if latest, ok := update.ShouldPrompt(true, refreshed, currentVersion, method); ok {
		program.Send(UpdateAvailableMsg{Version: latest, Method: method})
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
