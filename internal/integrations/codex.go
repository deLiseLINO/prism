package integrations

import "strings"

var CodexFence = Fence{
	Begin: "# >>> prism managed block (codex) — do not edit (removed by prism rollback) >>>",
	End:   "# <<< prism managed block (codex) <<<",
}

func codexManagedBlock(port int) string {
	lines := []string{
		"[model_providers.prism]",
		`name = "prism"`,
		"base_url = " + tomlString(ProviderBaseUrl(port)),
		`wire_api = "responses"`,
	}
	return strings.Join(lines, "\n")
}

func codexTransform(port int) func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		result := UpsertFencedBlock(current, CodexFence, codexManagedBlock(port))
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func codexRollbackTransform() func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		lookup := FindFencedRegion(current, CodexFence)
		if lookup.Kind == FencedOrphaned {
			return refusedTransform(DamagedFenceRollback)
		}
		next, changed := RemoveFencedBlock(current, CodexFence)
		return nextTransform(next, changed)
	}
}

func codexManagedRead(content string) ManagedRead {
	lookup := FindFencedRegion(content, CodexFence)
	switch lookup.Kind {
	case FencedAbsent:
		return ManagedRead{Kind: ManagedAbsent}
	case FencedOrphaned:
		return ManagedRead{Kind: ManagedDamaged, Reason: DamagedFenceApply}
	}
	endpoint, ok := ParseTomlStringField(lookup.Region.Inner, "base_url")
	if !ok {
		return ManagedRead{Kind: ManagedPresent, Endpoint: nil}
	}
	return ManagedRead{Kind: ManagedPresent, Endpoint: &endpoint}
}

func WriteCodexConfig(options CodexOptions) WriteOutcome {
	outcome, err := ApplyConfigTransform(options.ConfigPath, codexTransform(options.Port), options.CrashBeforeRename)
	if err != nil {
		return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("codex apply", err)}
	}
	return outcome
}

func StripCodexConfig(configPath string) WriteOutcome {
	outcome, err := ApplyConfigTransform(configPath, codexRollbackTransform(), false)
	if err != nil {
		return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("codex rollback", err)}
	}
	return outcome
}

func RecoverCodexConfig(configPath string) bool {
	return RecoverStaged(configPath)
}

type CodexOptions struct {
	Port              int
	ConfigPath        string
	Env               Env
	Home              string
	CrashBeforeRename bool
}

type CodexIntegration struct {
	id         ID
	port       int
	configPath string
	env        Env
	home       string
}

func NewCodex(options CodexOptions) *CodexIntegration {
	options = normalizeCodexOptions(options)
	return &CodexIntegration{id: Codex, port: options.Port, configPath: options.ConfigPath, env: options.Env, home: options.Home}
}

func normalizeCodexOptions(options CodexOptions) CodexOptions {
	if options.ConfigPath == "" {
		options.ConfigPath = CodexConfigPath(options.Env, options.Home)
	}
	return options
}

func (c *CodexIntegration) ID() ID { return c.id }

func (c *CodexIntegration) Apply() ApplyResult {
	return ToApplyResult(c.id, WriteCodexConfig(CodexOptions{Port: c.port, ConfigPath: c.configPath}))
}

func (c *CodexIntegration) Status() Status {
	return ObservedIntegrationStatus(c.id, c.configPath, []string{CodexHome(c.env, c.home)}, func(path string) ManagedRead {
		content, ok := ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return codexManagedRead(content)
	}, ProviderBaseUrl(c.port))
}

func (c *CodexIntegration) Rollback() ApplyResult {
	return ToRollbackResult(c.id, StripCodexConfig(c.configPath))
}
