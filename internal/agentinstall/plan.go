package agentinstall

import (
	"os"
	"strings"
)

type Method string

const (
	MethodBrewCask Method = "brew-cask"
	MethodBrew     Method = "brew"
	MethodNpm      Method = "npm"
	MethodBun      Method = "bun"
	MethodScript   Method = "script"
)

type Script struct {
	URL         string
	Interpreter string
}

type Plan struct {
	Method   Method
	Tool     string
	Command  []string
	Script   *Script
	Package  string
	ForceArg []string
	DocsURL  string
}

// Display renders the plan's argv in the single-line form jobs and status
// surfaces report.
func (p Plan) Display() string {
	if p.Script != nil {
		return p.Script.Interpreter + " " + p.Script.URL
	}
	return strings.Join(p.Command, " ")
}

// argv assembles the exact process argv for package-manager plans; script
// plans are fetched to a temp file by the manager (interpreters cannot open
// URLs), so they have no static argv — Display keeps the readable form.
func (p Plan) argv(force bool) []string {
	argv := append([]string{}, p.Command...)
	if force && len(p.ForceArg) > 0 {
		argv = append(argv, p.ForceArg...)
	}
	return argv
}

// resolveInstallPlan picks the first plan for the OS whose Tool (if any)
// resolves as an executable on PATH; scripts need no tool. False means no
// plan is executable in this environment.
func resolveInstallPlan(key, goos string, env integrationsEnv, stat func(string) (os.FileInfo, error)) (Plan, Definition, bool) {
	def, ok := definition(key)
	if !ok {
		return Plan{}, Definition{}, false
	}
	for _, plan := range def.Plans[goos] {
		if plan.Tool == "" || LookPath(env, plan.Tool, stat) != "" {
			return plan, def, true
		}
	}
	return Plan{}, Definition{}, false
}
