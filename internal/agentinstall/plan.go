package agentinstall

type Method string

const (
	MethodBrewCask Method = "brew-cask"
	MethodBrew     Method = "brew"
	MethodNpm      Method = "npm"
	MethodBun      Method = "bun"
	MethodScript   Method = "script"
	MethodArchive  Method = "archive"
)

type Script struct {
	URL         string
	Interpreter string
	Args        []string
	Requires    []string
	Downloader  bool
}

type Plan struct {
	Method        Method
	Tool          string
	Script        *Script
	Package       string
	Aliases       []string
	IgnoreScripts bool
}
