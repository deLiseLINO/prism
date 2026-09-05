package main

import (
	"fmt"
	"strconv"
	"strings"

	"prism/internal/management"
)

// command is the discriminated union parsed from argv. Parsing happens once,
// before any network or disk work; handlers receive only parsed fields.
type command struct {
	verb string
	json bool

	// auth
	provider    string
	authNoOpen  bool
	authSession string

	// accounts
	account      string
	selectValue  string
	version      uint64
	priority     int
	autoSwitch   bool
	threshold    float64
	thresholdSet bool
	strategy     string
	affinity     string

	// providers
	id           string
	wire         string
	baseURL      string
	defaultModel string
	models       []string
	credFile     string
	credStdin    bool
	enable       bool

	// combos / routes
	comboID  string
	routeKey string
	targets  []management.Target
	alias    string
	value    string

	// integrations
	clientID string

	// models
	modelID string
}

// usageError carries the specific subcommand help so parse failures are
// deterministic and always name the command the user invoked.
type usageError struct {
	msg  string
	help string
}

type flagSet struct {
	names  map[string]bool
	values map[string]string
	// multi collects every value of repeatable flags (--target).
	multi map[string][]string
	// unknown records flags that were present but unassigned, so callers
	// can reject them instead of silently dropping.
	unknown []string
}

func scanFlags(args []string, spec map[string]bool) ([]string, *flagSet) {
	fs := &flagSet{names: map[string]bool{}, values: map[string]string{}, multi: map[string][]string{}}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			return pos, fs
		}
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		name := a[2:]
		if name == "" {
			fs.unknown = append(fs.unknown, "--")
			continue
		}
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			if wantsValue, ok := spec[name[:eq]]; ok && wantsValue {
				fs.names[name[:eq]] = true
				fs.values[name[:eq]] = name[eq+1:]
				fs.multi[name[:eq]] = append(fs.multi[name[:eq]], name[eq+1:])
				continue
			}
		}
		if wantsValue, ok := spec[name]; ok {
			fs.names[name] = true
			if wantsValue {
				if i+1 >= len(args) {
					fs.unknown = append(fs.unknown, name)
					continue
				}
				i++
				fs.values[name] = args[i]
				fs.multi[name] = append(fs.multi[name], args[i])
			}
			continue
		}
		fs.unknown = append(fs.unknown, name)
	}
	return pos, fs
}
func (fs *flagSet) has(name string) bool { return fs.names[name] }

func (fs *flagSet) val(name string) (string, bool) {
	v, ok := fs.values[name]
	return v, ok
}

// all returns every recorded value of a repeatable flag.
func (fs *flagSet) all(name string) []string {
	return fs.multi[name]
}
func (e *usageError) Error() string { return e.msg }

func usageFail(help string, format string, a ...any) error {
	return &usageError{help: help, msg: fmt.Sprintf(format, a...)}
}

// globalFlags is the flag vocabulary shared by every subcommand; per-command
// specs extend it so --json is legal everywhere and unknown flags are errors.
func globalFlags() map[string]bool {
	return map[string]bool{
		"json":            false,
		"no-open":         false,
		"session":         true,
		"threshold":       true,
		"version":         true,
		"priority":        true,
		"wire":            true,
		"endpoint":        true,
		"base-url":        true,
		"default-model":   true,
		"model":           true,
		"models":          true,
		"credential-file": true,
		"stdin":           false,
		"weight":          true,
		"alias":           true,
		"sticky-limit":    true,
		"strategy":        true,
		"affinity":        true,
		"value":           true,
		"target":          true,
	}
}

var helpTop = `prismctl - control the Prism daemon

Usage: prismctl <command> [subcommand] [flags]

Commands:
  status                    daemon health and summary
  doctor                    connectivity, version, and config sanity report
  auth login|status         browser OAuth login or session/provider status
  accounts <sub>            list/pause/resume/priority/quota/remove/select/
                            auto-switch/distribute/affinity
  providers <sub>           list/add/edit/enable/disable/remove
  models <sub>              list/enable/disable
  combos <sub>              list/set/remove
  routes <sub>              list/set/remove
  integrations <sub>        status/apply/rollback
  usage                     quota usage across accounts

Every command accepts --json for machine-stable output.
`

var helpStatus = `Usage: prismctl status [--json]
`
var helpDoctor = `Usage: prismctl doctor [--json]
`
var helpUsage = `Usage: prismctl usage [--json]
`

var helpAuth = `Usage: prismctl auth login <codex|antigravity> [--no-open] [--json]
       prismctl auth status [session] [--json]
`

var helpAccounts = `Usage: prismctl accounts list [--json]
       prismctl accounts pause <account> [--version N] [--json]
       prismctl accounts resume <account> [--version N] [--json]
       prismctl accounts priority <account> <N> [--version N] [--json]
       prismctl accounts quota <account> [--json]
       prismctl accounts remove <account> [--json]
       prismctl accounts select <provider> <account|auto> [--json]
       prismctl accounts auto-switch <provider> <on|off> [--threshold N] [--json]
       prismctl accounts distribute <provider> <quota|round-robin|fill-first> [--json]
       prismctl accounts affinity <provider> <sticky|off> [--json]
`

var helpProviders = `Usage: prismctl providers list [--json]
       prismctl providers add <id> --wire <wire> [--endpoint URL] [--default-model M]
                              [--model M ...] [--credential-file PATH | --stdin] [--json]
       prismctl providers edit <id> [--wire W] [--endpoint URL] [--default-model M]
                               [--model M ...] [--credential-file PATH | --stdin] [--json]
       prismctl providers enable <id> [--json]
       prismctl providers disable <id> [--json]
       prismctl providers remove <id> [--json]
Wires: codex, antigravity, responses, chat, messages
`

var helpModels = `Usage: prismctl models list [--json]
       prismctl models enable <provider> <model> [--json]
       prismctl models disable <provider> <model> [--json]
`

var helpCombos = `Usage: prismctl combos list [--json]
       prismctl combos set <id> --strategy <failover|round_robin>
                           [--target provider/model[:weight]] ...
                           [--sticky-limit N] [--alias A] [--json]
       prismctl combos remove <id> [--json]
`

var helpRoutes = `Usage: prismctl routes list [--json]
       prismctl routes set <key> <provider/model|combo-id> [--json]
       prismctl routes remove <key> [--json]
`

var helpIntegrations = `Usage: prismctl integrations status [codex|grok|omp] [--json]
       prismctl integrations apply <codex|grok|omp> [--json]
       prismctl integrations rollback <codex|grok|omp> [--json]
`

// parseCommand turns argv (after the binary name) into one command value or a
// usage error. No network, no disk, no clock.
func parseCommand(args []string) (*command, error) {
	if len(args) == 0 {
		return nil, &usageError{help: helpTop, msg: "no command given"}
	}
	spec := globalFlags()
	switch args[0] {
	case "status":
		pos, fs := scanFlags(args[1:], spec)
		if err := rejectUnknown(fs, helpStatus); err != nil {
			return nil, err
		}
		if len(pos) > 0 {
			return nil, usageFail(helpStatus, "unexpected argument %q", pos[0])
		}
		return &command{verb: "status", json: fs.has("json")}, nil
	case "doctor":
		pos, fs := scanFlags(args[1:], spec)
		if err := rejectUnknown(fs, helpDoctor); err != nil {
			return nil, err
		}
		if len(pos) > 0 {
			return nil, usageFail(helpDoctor, "unexpected argument %q", pos[0])
		}
		return &command{verb: "doctor", json: fs.has("json")}, nil
	case "usage":
		pos, fs := scanFlags(args[1:], spec)
		if err := rejectUnknown(fs, helpUsage); err != nil {
			return nil, err
		}
		if len(pos) > 0 {
			return nil, usageFail(helpUsage, "unexpected argument %q", pos[0])
		}
		return &command{verb: "usage", json: fs.has("json")}, nil
	case "auth":
		return parseAuth(args[1:], spec)
	case "accounts":
		return parseAccounts(args[1:], spec)
	case "providers":
		return parseProviders(args[1:], spec)
	case "models":
		return parseModels(args[1:], spec)
	case "combos":
		return parseCombos(args[1:], spec)
	case "routes":
		return parseRoutes(args[1:], spec)
	case "integrations":
		return parseIntegrations(args[1:], spec)
	case "help", "--help", "-h":
		return &command{verb: "help"}, nil
	default:
		return nil, usageFail(helpTop, "unknown command %q", args[0])
	}
}

func rejectUnknown(fs *flagSet, help string) error {
	if len(fs.unknown) > 0 {
		return usageFail(help, "unknown flag --%s", fs.unknown[0])
	}
	return nil
}

func parseAuth(args []string, spec map[string]bool) (*command, error) {
	if len(args) == 0 {
		return nil, usageFail(helpAuth, "auth requires a subcommand")
	}
	sub := args[0]
	pos, fs := scanFlags(args[1:], spec)
	if err := rejectUnknown(fs, helpAuth); err != nil {
		return nil, err
	}
	cmd := &command{verb: "auth-" + sub, json: fs.has("json")}
	switch sub {
	case "login":
		if len(pos) != 1 {
			return nil, usageFail(helpAuth, "auth login requires exactly one provider: codex or antigravity")
		}
		if pos[0] != "codex" && pos[0] != "antigravity" {
			return nil, usageFail(helpAuth, "unknown auth provider %q (want codex or antigravity)", pos[0])
		}
		cmd.provider = pos[0]
		cmd.authNoOpen = fs.has("no-open")
		return cmd, nil
	case "status":
		if len(pos) > 1 {
			return nil, usageFail(helpAuth, "auth status takes at most one session argument")
		}
		cmd.provider = "codex"
		if v, ok := fs.val("session"); ok {
			if len(pos) > 0 {
				return nil, usageFail(helpAuth, "auth status takes one session, not both a positional and --session")
			}
			cmd.authSession = v
		} else if len(pos) == 1 {
			cmd.authSession = pos[0]
		}
		// status without provider: query both providers.
		return cmd, nil
	default:
		return nil, usageFail(helpAuth, "unknown auth subcommand %q", sub)
	}
}

func parseAccounts(args []string, spec map[string]bool) (*command, error) {
	if len(args) == 0 {
		return nil, usageFail(helpAccounts, "accounts requires a subcommand")
	}
	sub := args[0]
	pos, fs := scanFlags(args[1:], spec)
	if err := rejectUnknown(fs, helpAccounts); err != nil {
		return nil, err
	}
	cmd := &command{verb: "accounts-" + sub, json: fs.has("json")}
	switch sub {
	case "list":
		if len(pos) != 0 {
			return nil, usageFail(helpAccounts, "accounts list takes no arguments")
		}
		return cmd, nil
	case "pause", "resume":
		if len(pos) != 1 {
			return nil, usageFail(helpAccounts, "accounts %s requires exactly one account id", sub)
		}
		cmd.account = pos[0]
		if v, ok := fs.val("version"); ok {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return nil, usageFail(helpAccounts, "invalid --version %q", v)
			}
			cmd.version = n
		}
		return cmd, nil
	case "priority":
		if len(pos) != 2 {
			return nil, usageFail(helpAccounts, "accounts priority requires <account> <priority>")
		}
		cmd.account = pos[0]
		n, err := strconv.Atoi(pos[1])
		if err != nil {
			return nil, usageFail(helpAccounts, "invalid priority %q", pos[1])
		}
		cmd.priority = n
		if v, ok := fs.val("version"); ok {
			m, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return nil, usageFail(helpAccounts, "invalid --version %q", v)
			}
			cmd.version = m
		}
		return cmd, nil
	case "quota", "remove":
		if len(pos) != 1 {
			return nil, usageFail(helpAccounts, "accounts %s requires exactly one account id", sub)
		}
		cmd.account = pos[0]
		return cmd, nil
	case "select":
		if len(pos) != 2 {
			return nil, usageFail(helpAccounts, "accounts select requires <provider> <account|auto>")
		}
		cmd.id = pos[0]
		cmd.selectValue = pos[1]
		return cmd, nil
	case "auto-switch":
		if len(pos) != 2 {
			return nil, usageFail(helpAccounts, "accounts auto-switch requires <provider> <on|off>")
		}
		cmd.id = pos[0]
		switch pos[1] {
		case "on":
			cmd.autoSwitch = true
		case "off":
			cmd.autoSwitch = false
		default:
			return nil, usageFail(helpAccounts, "auto-switch state must be on or off, got %q", pos[1])
		}
		if v, ok := fs.val("threshold"); ok {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 || f > 1 {
				return nil, usageFail(helpAccounts, "threshold must be a number in [0,1], got %q", v)
			}
			cmd.threshold = f
			cmd.thresholdSet = true
		}
		return cmd, nil
	case "distribute":
		if len(pos) != 2 {
			return nil, usageFail(helpAccounts, "accounts distribute requires <provider> <quota|round-robin|fill-first>")
		}
		cmd.id = pos[0]
		switch pos[1] {
		case "quota":
			cmd.strategy = "quota"
		case "round-robin":
			cmd.strategy = "round_robin"
		case "fill-first":
			cmd.strategy = "fill_first"
		default:
			return nil, usageFail(helpAccounts, "distribution strategy must be quota, round-robin, or fill-first, got %q", pos[1])
		}
		return cmd, nil
	case "affinity":
		if len(pos) != 2 {
			return nil, usageFail(helpAccounts, "accounts affinity requires <provider> <sticky|off>")
		}
		cmd.id = pos[0]
		switch pos[1] {
		case "sticky", "off":
			cmd.affinity = pos[1]
		default:
			return nil, usageFail(helpAccounts, "affinity must be sticky or off, got %q", pos[1])
		}
		return cmd, nil
	default:
		return nil, usageFail(helpAccounts, "unknown accounts subcommand %q", sub)
	}
}

func parseProviders(args []string, spec map[string]bool) (*command, error) {
	if len(args) == 0 {
		return nil, usageFail(helpProviders, "providers requires a subcommand")
	}
	sub := args[0]
	pos, fs := scanFlags(args[1:], spec)
	if err := rejectUnknown(fs, helpProviders); err != nil {
		return nil, err
	}
	cmd := &command{verb: "providers-" + sub, json: fs.has("json")}
	switch sub {
	case "list":
		if len(pos) != 0 {
			return nil, usageFail(helpProviders, "providers list takes no arguments")
		}
		return cmd, nil
	case "add", "edit":
		if len(pos) != 1 {
			return nil, usageFail(helpProviders, "providers %s requires exactly one provider id", sub)
		}
		cmd.id = pos[0]
		if sub == "add" {
			v, ok := fs.val("wire")
			if !ok {
				return nil, usageFail(helpProviders, "providers add requires --wire <codex|antigravity|responses|chat|messages>")
			}
			if !validWire(v) {
				return nil, usageFail(helpProviders, "unknown wire %q (want codex, antigravity, responses, chat, or messages)", v)
			}
			cmd.wire = v
		} else if v, ok := fs.val("wire"); ok {
			if !validWire(v) {
				return nil, usageFail(helpProviders, "unknown wire %q (want codex, antigravity, responses, chat, or messages)", v)
			}
			cmd.wire = v
		}
		if v, ok := fs.val("endpoint"); ok {
			cmd.baseURL = v
		}
		if v, ok := fs.val("base-url"); ok {
			if cmd.baseURL != "" {
				return nil, usageFail(helpProviders, "use either --endpoint or --base-url, not both")
			}
			cmd.baseURL = v
		}
		if v, ok := fs.val("default-model"); ok {
			cmd.defaultModel = v
		}
		for _, v := range fs.all("model") {
			cmd.models = append(cmd.models, v)
		}
		if v, ok := fs.val("models"); ok {
			for _, m := range strings.Split(v, ",") {
				m = strings.TrimSpace(m)
				if m != "" {
					cmd.models = append(cmd.models, m)
				}
			}
		}
		hasFile := fs.has("credential-file")
		hasStdin := fs.has("stdin")
		if hasFile && hasStdin {
			return nil, usageFail(helpProviders, "credential input: use either --credential-file or --stdin, not both")
		}
		if hasFile {
			v, _ := fs.val("credential-file")
			if v == "" {
				return nil, usageFail(helpProviders, "--credential-file requires a path")
			}
			cmd.credFile = v
		}
		if hasStdin {
			cmd.credStdin = true
		}
		return cmd, nil
	case "enable", "disable":
		if len(pos) != 1 {
			return nil, usageFail(helpProviders, "providers %s requires exactly one provider id", sub)
		}
		cmd.id = pos[0]
		cmd.enable = sub == "enable"

		return cmd, nil
	case "remove":
		if len(pos) != 1 {
			return nil, usageFail(helpProviders, "providers remove requires exactly one provider id")
		}
		cmd.id = pos[0]
		return cmd, nil
	default:
		return nil, usageFail(helpProviders, "unknown providers subcommand %q", sub)
	}
}

func validWire(w string) bool {
	switch w {
	case "codex", "antigravity", "responses", "chat", "messages":
		return true
	}
	return false
}

func parseModels(args []string, spec map[string]bool) (*command, error) {
	if len(args) == 0 {
		return nil, usageFail(helpModels, "models requires a subcommand")
	}
	sub := args[0]
	pos, fs := scanFlags(args[1:], spec)
	if err := rejectUnknown(fs, helpModels); err != nil {
		return nil, err
	}
	cmd := &command{verb: "models-" + sub, json: fs.has("json")}
	switch sub {
	case "list":
		if len(pos) != 0 {
			return nil, usageFail(helpModels, "models list takes no arguments")
		}
		return cmd, nil
	case "enable", "disable":
		if len(pos) != 2 {
			return nil, usageFail(helpModels, "models %s requires <provider> <model>", sub)
		}
		cmd.id = pos[0]
		cmd.modelID = pos[1]
		cmd.enable = sub == "enable"

		return cmd, nil
	default:
		return nil, usageFail(helpModels, "unknown models subcommand %q", sub)
	}
}

func parseCombos(args []string, spec map[string]bool) (*command, error) {
	if len(args) == 0 {
		return nil, usageFail(helpCombos, "combos requires a subcommand")
	}
	sub := args[0]
	pos, fs := scanFlags(args[1:], spec)
	if err := rejectUnknown(fs, helpCombos); err != nil {
		return nil, err
	}
	cmd := &command{verb: "combos-" + sub, json: fs.has("json")}
	switch sub {
	case "list":
		if len(pos) != 0 {
			return nil, usageFail(helpCombos, "combos list takes no arguments")
		}
		return cmd, nil
	case "set":
		if len(pos) != 1 {
			return nil, usageFail(helpCombos, "combos set requires exactly one combo id")
		}
		cmd.comboID = pos[0]
		v, ok := fs.val("strategy")
		if !ok {
			return nil, usageFail(helpCombos, "combos set requires --strategy <failover|round_robin>")
		}
		if v != "failover" && v != "round_robin" {
			return nil, usageFail(helpCombos, "strategy must be failover or round_robin, got %q", v)
		}
		cmd.strategy = v
		if v, ok := fs.val("sticky-limit"); ok {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return nil, usageFail(helpCombos, "sticky-limit must be a non-negative integer, got %q", v)
			}
			cmd.affinity = strconv.Itoa(n)
		}
		if v, ok := fs.val("alias"); ok {
			cmd.alias = v
		}
		// --target may repeat; --models holds a comma list.
		for _, t := range fsTargets(fs) {
			tgt, err := parseTarget(t)
			if err != nil {
				return nil, usageFail(helpCombos, "%v", err)
			}
			cmd.targets = append(cmd.targets, tgt)
		}
		if len(cmd.targets) == 0 {
			return nil, usageFail(helpCombos, "combos set requires at least one --target provider/model[:weight]")
		}
		return cmd, nil
	case "remove":
		if len(pos) != 1 {
			return nil, usageFail(helpCombos, "combos remove requires exactly one combo id")
		}
		cmd.comboID = pos[0]
		return cmd, nil
	default:
		return nil, usageFail(helpCombos, "unknown combos subcommand %q", sub)
	}
}

func fsTargets(fs *flagSet) []string {
	return fs.all("target")
}

func parseTarget(s string) (management.Target, error) {
	weight := 0
	if w, ok := cutString(s, ':'); ok {
		n, err := strconv.Atoi(w)
		if err != nil || n < 0 {
			return management.Target{}, fmt.Errorf("invalid target weight in %q", s)
		}
		weight = n
		s = s[:strings.LastIndexByte(s, ':')]
	}
	provider, model, ok := strings.Cut(s, "/")
	if !ok || provider == "" || model == "" {
		return management.Target{}, fmt.Errorf("target %q must be provider/model[:weight]", s)
	}
	return management.Target{Provider: provider, Model: model, Weight: weight}, nil
}

// cutString reports whether s contains sep and returns the suffix after it.
func cutString(s string, sep byte) (string, bool) {
	if i := strings.LastIndexByte(s, sep); i >= 0 {
		return s[i+1:], true
	}
	return "", false
}

func parseRoutes(args []string, spec map[string]bool) (*command, error) {
	if len(args) == 0 {
		return nil, usageFail(helpRoutes, "routes requires a subcommand")
	}
	sub := args[0]
	pos, fs := scanFlags(args[1:], spec)
	if err := rejectUnknown(fs, helpRoutes); err != nil {
		return nil, err
	}
	cmd := &command{verb: "routes-" + sub, json: fs.has("json")}
	switch sub {
	case "list":
		if len(pos) != 0 {
			return nil, usageFail(helpRoutes, "routes list takes no arguments")
		}
		return cmd, nil
	case "set":
		if len(pos) != 2 {
			return nil, usageFail(helpRoutes, "routes set requires <key> <provider/model|combo-id>")
		}
		cmd.routeKey = pos[0]
		cmd.value = pos[1]
		return cmd, nil
	case "remove":
		if len(pos) != 1 {
			return nil, usageFail(helpRoutes, "routes remove requires exactly one route key")
		}
		cmd.routeKey = pos[0]
		return cmd, nil
	default:
		return nil, usageFail(helpRoutes, "unknown routes subcommand %q", sub)
	}
}

func parseIntegrations(args []string, spec map[string]bool) (*command, error) {
	if len(args) == 0 {
		return nil, usageFail(helpIntegrations, "integrations requires a subcommand")
	}
	sub := args[0]
	pos, fs := scanFlags(args[1:], spec)
	if err := rejectUnknown(fs, helpIntegrations); err != nil {
		return nil, err
	}
	cmd := &command{verb: "integrations-" + sub, json: fs.has("json")}
	switch sub {
	case "status":
		if len(pos) > 1 {
			return nil, usageFail(helpIntegrations, "integrations status takes at most one client")
		}
		if len(pos) == 1 {
			if !validClient(pos[0]) {
				return nil, usageFail(helpIntegrations, "unknown integration client %q (want codex, grok, or omp)", pos[0])
			}
			cmd.clientID = pos[0]
		}
		return cmd, nil
	case "apply", "rollback":
		if len(pos) != 1 {
			return nil, usageFail(helpIntegrations, "integrations %s requires exactly one client (codex, grok, or omp)", sub)
		}
		if !validClient(pos[0]) {
			return nil, usageFail(helpIntegrations, "unknown integration client %q (want codex, grok, or omp)", pos[0])
		}
		cmd.clientID = pos[0]
		return cmd, nil
	default:
		return nil, usageFail(helpIntegrations, "unknown integrations subcommand %q", sub)
	}
}

func validClient(s string) bool {
	return s == "codex" || s == "grok" || s == "omp"
}
