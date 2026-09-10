package integrations

const ompPrismApiKey = "prism-loopback"
const ompPrismApi = "openai-completions"

type OmpOptions struct {
	Port              int
	Models            []Model
	ModelsSource      func() []Model
	AgentDir          string
	ModelsPath        string
	Env               Env
	Home              string
	CrashBeforeRename bool
}

type OmpIntegration struct {
	id        ID
	port      int
	models    []Model
	modelsSrc func() []Model
	options   OmpOptions
}

func NewOmp(options OmpOptions) *OmpIntegration {
	return &OmpIntegration{id: Omp, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, options: options}
}

func (o *OmpIntegration) ID() ID { return o.id }

func (o *OmpIntegration) paths() (agentDir string, modelsPath string, err error) {
	agentDir = o.options.AgentDir
	if agentDir == "" {
		agentDir, err = OmpAgentDir(o.options.Env, o.options.Home)
		if err != nil {
			return "", "", err
		}
	}
	modelsPath = o.options.ModelsPath
	if modelsPath == "" {
		modelsPath = OmpModelsConfigPath(agentDir, FileExists)
	}
	return agentDir, modelsPath, nil
}

func NewOmpSpec(port int, models []Model) OmpProviderSpec {
	return OmpProviderSpec{BaseURL: ProviderBaseUrl(port), API: ompPrismApi, APIKey: ompPrismApiKey, Models: models}
}

func toTransform(result YamlLeafPatch) ConfigTransform {
	if result.Kind == "written" {
		return nextTransform(result.Next, result.Changed)
	}
	return refusedTransform(result.Reason)
}

func ompManagedRead(content string) ManagedRead {
	leaf := ReadProviderLeaf(content, "prism")
	switch leaf.Kind {
	case LeafAbsent:
		return ManagedRead{Kind: ManagedAbsent}
	case LeafRefused:
		return ManagedRead{Kind: ManagedUnsupported, Reason: leaf.Reason}
	}
	return ManagedRead{Kind: ManagedPresent, Endpoint: leaf.BaseURL}
}

func WriteOmpConfig(options OmpOptions) WriteOutcome {
	models, refusal := resolveModels(options.Models, options.ModelsSource, Omp)
	if refusal != "" {
		return WriteOutcome{Kind: OutcomeRefused, Reason: refusal}
	}
	spec := NewOmpSpec(options.Port, models)
	outcome, err := ApplyConfigTransform(options.ModelsPath, func(current string) ConfigTransform {
		return toTransform(UpsertProviderLeaf(current, "prism", spec))
	}, options.CrashBeforeRename)
	if err != nil {
		return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("omp apply", err)}
	}
	return outcome
}

func StripOmpConfig(modelsPath string) WriteOutcome {
	outcome, err := ApplyConfigTransform(modelsPath, func(current string) ConfigTransform {
		return toTransform(RemoveProviderLeaf(current, "prism"))
	}, false)
	if err != nil {
		return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("omp rollback", err)}
	}
	return outcome
}

func RecoverOmpConfig(modelsPath string) bool {
	return RecoverStaged(modelsPath)
}

func (o *OmpIntegration) Apply() ApplyResult {
	_, modelsPath, err := o.paths()
	if err != nil {
		return ApplyResult{OK: false, ID: o.id, Reason: failureReason("omp apply", err)}
	}
	return ToApplyResult(o.id, WriteOmpConfig(OmpOptions{ModelsPath: modelsPath, Port: o.port, Models: o.models, ModelsSource: o.modelsSrc}))
}

func (o *OmpIntegration) Status() Status {
	agentDir, modelsPath, err := o.paths()
	if err != nil {
		return Status{ID: o.id, Installed: false, Managed: false, TargetPath: nil, Endpoint: nil, Drift: false, Detail: failureReason("omp status", err)}
	}
	return ObservedIntegrationStatus(o.id, modelsPath, []string{agentDir}, func(path string) ManagedRead {
		content, ok := ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return ompManagedRead(content)
	}, ProviderBaseUrl(o.port))
}

func (o *OmpIntegration) Rollback() ApplyResult {
	_, modelsPath, err := o.paths()
	if err != nil {
		return ApplyResult{OK: false, ID: o.id, Reason: failureReason("omp rollback", err)}
	}
	return ToRollbackResult(o.id, StripOmpConfig(modelsPath))
}
