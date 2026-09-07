package integrations

const hermesPrismApiKey = "prism-loopback"

// RenderHermesLeaf renders the `providers.prism` member of hermes' config.yaml:
// an OpenAI chat-completions provider with a supplied model list, matching the
// block shape hermes itself documents for custom providers.
func RenderHermesLeaf(port int, models []Model, indent int) string {
	pad := repeatSpaces(indent)
	body := repeatSpaces(indent + 2)
	item := repeatSpaces(indent + 4)
	lines := []string{
		pad + "prism:",
		body + "api: " + ProviderBaseUrl(port),
		body + "api_key: " + hermesPrismApiKey,
		body + "api_mode: chat_completions",
		body + "discover_models: false",
	}
	if len(models) == 0 {
		lines = append(lines, body+"models: []")
	} else {
		lines = append(lines, body+"models:")
		for _, model := range models {
			lines = append(lines, item+"- "+model.ID)
		}
	}
	return joinStrings(lines)
}

func hermesTransform(port int, models []Model) func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		result := UpsertProviderLeafBody(current, "prism", "config.yaml", func(indent int) string {
			return RenderHermesLeaf(port, models, indent)
		})
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func hermesRollbackTransform() func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		result := RemoveProviderLeafBody(current, "prism", "config.yaml")
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func hermesManagedRead(content string) ManagedRead {
	read := ReadProviderLeafBody(content, "prism", "config.yaml", "api")
	switch read.Kind {
	case LeafAbsent:
		return ManagedRead{Kind: ManagedAbsent}
	case LeafRefused:
		return ManagedRead{Kind: ManagedUnsupported, Reason: read.Reason}
	}
	return ManagedRead{Kind: ManagedPresent, Endpoint: read.BaseURL}
}

type HermesOptions struct {
	Port              int
	Models            []Model
	ModelsSource      func() []Model
	ConfigPath        string
	Env               Env
	Home              string
	CrashBeforeRename bool
}

type HermesIntegration struct {
	id         ID
	port       int
	models     []Model
	modelsSrc  func() []Model
	configPath string
	env        Env
	home       string
}

func (h *HermesIntegration) paths() string {
	if h.configPath != "" {
		return h.configPath
	}
	return HermesConfigPath(h.env, h.home)
}

func NewHermes(options HermesOptions) *HermesIntegration {
	return &HermesIntegration{id: Hermes, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, configPath: options.ConfigPath, env: options.Env, home: options.Home}
}
func (h *HermesIntegration) ID() ID { return h.id }

func (h *HermesIntegration) Apply() ApplyResult {
	models, refusal := resolveModels(h.models, h.modelsSrc, h.id)
	if refusal != "" {
		return ApplyResult{OK: false, ID: h.id, Reason: refusal}
	}
	outcome, err := ApplyConfigTransform(h.paths(), hermesTransform(h.port, models), false)
	if err != nil {
		return ToApplyResult(h.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("hermes apply", err)})
	}
	return ToApplyResult(h.id, outcome)
}

func (h *HermesIntegration) Status() Status {
	return ObservedIntegrationStatus(h.id, h.paths(), []string{HermesHome(h.env, h.home)}, func(path string) ManagedRead {
		content, ok := ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return hermesManagedRead(content)
	}, ProviderBaseUrl(h.port))
}

func (h *HermesIntegration) Rollback() ApplyResult {
	outcome, err := ApplyConfigTransform(h.paths(), hermesRollbackTransform(), false)
	if err != nil {
		return ToRollbackResult(h.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("hermes rollback", err)})
	}
	return ToRollbackResult(h.id, outcome)
}

func RecoverHermesConfig(configPath string) bool {
	return RecoverStaged(configPath)
}
