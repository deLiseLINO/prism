package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/buildinfo"
	"github.com/deLiseLINO/prism/internal/update"
)

func stubUpgrade(t *testing.T, method update.Method, latest string, fetchErr error) *[]string {
	t.Helper()
	prevDetect, prevFetch, prevRun, prevVersion := detectUpdateMethod, fetchLatestVersion, runUpgradeFn, buildinfo.Version
	t.Cleanup(func() {
		detectUpdateMethod, fetchLatestVersion, runUpgradeFn, buildinfo.Version = prevDetect, prevFetch, prevRun, prevVersion
	})
	var ran []string
	buildinfo.Version = "0.1.0"
	detectUpdateMethod = func() update.Method { return method }
	fetchLatestVersion = func(context.Context, update.Method) (string, error) { return latest, fetchErr }
	runUpgradeFn = func(m update.Method, v string, _, _ io.Writer) error {
		ran = append(ran, string(m)+" "+v)
		return nil
	}
	return &ran
}

func TestUpgradeUnknownMethodPrintsInstructions(t *testing.T) {
	ran := stubUpgrade(t, update.MethodUnknown, "0.2.0", nil)
	var out, errOut bytes.Buffer
	if code := runUpgradeCommand(nil, &out, &errOut); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out.String(), "Manual update options") || len(*ran) != 0 {
		t.Fatalf("out = %q ran = %v", out.String(), *ran)
	}
}

func TestUpgradeUpToDate(t *testing.T) {
	ran := stubUpgrade(t, update.MethodGo, "0.1.0", nil)
	var out, errOut bytes.Buffer
	if code := runUpgradeCommand(nil, &out, &errOut); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out.String(), "already up to date") || len(*ran) != 0 {
		t.Fatalf("out = %q ran = %v", out.String(), *ran)
	}
}

func TestUpgradeRunsForNewerVersion(t *testing.T) {
	ran := stubUpgrade(t, update.MethodBrew, "0.2.0", nil)
	var out, errOut bytes.Buffer
	if code := runUpgradeCommand(nil, &out, &errOut); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(*ran) != 1 || (*ran)[0] != "brew 0.2.0" {
		t.Fatalf("ran = %v", *ran)
	}
}

func TestUpgradeRunsWhenLookupFails(t *testing.T) {
	ran := stubUpgrade(t, update.MethodGo, "", errors.New("offline"))
	var out, errOut bytes.Buffer
	if code := runUpgradeCommand(nil, &out, &errOut); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(*ran) != 1 || !strings.Contains(errOut.String(), "offline") {
		t.Fatalf("ran = %v err = %q", *ran, errOut.String())
	}
}

func TestUpgradeFailureExitsOne(t *testing.T) {
	stubUpgrade(t, update.MethodGo, "0.2.0", nil)
	runUpgradeFn = func(update.Method, string, io.Writer, io.Writer) error { return errors.New("boom") }
	var out, errOut bytes.Buffer
	if code := runUpgradeCommand(nil, &out, &errOut); code != 1 {
		t.Fatalf("code = %d", code)
	}
}

func TestUpgradeRejectsArguments(t *testing.T) {
	stubUpgrade(t, update.MethodGo, "0.2.0", nil)
	var out, errOut bytes.Buffer
	if code := runUpgradeCommand([]string{"now"}, &out, &errOut); code != 2 {
		t.Fatalf("code = %d", code)
	}
}
