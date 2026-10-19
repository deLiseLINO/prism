package integrations

import "path/filepath"

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
	IO                FileIO
	CrashBeforeRename bool
}

type OmpIntegration struct {
	id        ID
	port      int
	models    []Model
	modelsSrc func() []Model
	io        FileIO
	options   OmpOptions
}

func NewOmp(options OmpOptions) *OmpIntegration {
	return &OmpIntegration{id: Omp, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, io: withLocalIO(options.IO), options: options}
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
		canonical := filepath.Join(agentDir, "models.yml")
		_, present, readErr := o.io.ReadText(canonical)
		if readErr != nil {
			return "", "", readErr
		}
		modelsPath = canonical
		if !present {
			fallback := filepath.Join(agentDir, "models.yaml")
			_, exists, readErr := o.io.ReadText(fallback)
			if readErr != nil {
				return "", "", readErr
			}
			if exists {
				modelsPath = fallback
			}
		}
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

func WriteOmpConfig(io FileIO, options OmpOptions) WriteOutcome {
	return writeOmpConfig(io, options, false)
}

func writeOmpConfig(io FileIO, options OmpOptions, force bool) WriteOutcome {
	models, refusal := resolveModels(options.Models, options.ModelsSource, Omp)
	if refusal != "" {
		return WriteOutcome{Kind: OutcomeRefused, Reason: refusal}
	}
	unlock := lockMutation(options.ModelsPath)
	defer unlock()
	spec := NewOmpSpec(options.Port, models)
	transform := func(current string) ConfigTransform {
		return toTransform(UpsertProviderLeaf(current, "prism", spec))
	}
	if force {
		if err := inspectJournal(io, options.ModelsPath, nil); err != nil {
			refusal := recoveryRefusal("omp apply", err)
			if refusal.Conflict != ConflictJournal {
				return refusal
			}
			outcome, err := recoverRestorationUnlocked(io, options.ModelsPath, nil, nil, transform, nil, false)
			return withRecoveryBackups(outcome, outcome, "omp apply", err)
		}
	}
	outcome, err := applyRestorationUnlocked(io, options.ModelsPath, transform, nil, options.CrashBeforeRename, nil)
	if err != nil {
		return recoveryRefusal("omp apply", err)
	}
	return outcome
}

func StripOmpConfig(io FileIO, modelsPath string) WriteOutcome {
	return stripOmpConfig(io, modelsPath, false)
}

func stripOmpConfig(io FileIO, modelsPath string, force bool) WriteOutcome {
	unlock := lockMutation(modelsPath)
	defer unlock()
	transform := func(current string) ConfigTransform {
		return toTransform(RemoveProviderLeaf(current, "prism"))
	}
	if force {
		if err := inspectJournal(io, modelsPath, nil); err != nil {
			refusal := recoveryRefusal("omp rollback", err)
			if refusal.Conflict != ConflictJournal {
				return refusal
			}
			outcome, err := recoverRestorationUnlocked(io, modelsPath, nil, nil, transform, nil, true)
			return withRecoveryBackups(outcome, outcome, "omp rollback", err)
		}
	}
	outcome, err := rollbackRestorationUnlocked(io, modelsPath, transform)
	if err != nil {
		return recoveryRefusal("omp rollback", err)
	}
	return outcome
}

func RecoverOmpConfig(io FileIO, modelsPath string) bool {
	return recoverConfigStage(io, modelsPath)
}

func (o *OmpIntegration) Apply() ApplyResult { return o.applyWith(false) }

func (o *OmpIntegration) ApplyForced() ApplyResult { return o.applyWith(true) }

func (o *OmpIntegration) applyWith(force bool) ApplyResult {
	_, modelsPath, err := o.paths()
	if err != nil {
		return ToApplyResult(o.id, recoveryRefusal("omp apply", err))
	}
	return ToApplyResult(o.id, writeOmpConfig(o.io, OmpOptions{ModelsPath: modelsPath, Port: o.port, Models: o.models, ModelsSource: o.modelsSrc}, force))
}

func (o *OmpIntegration) Status() Status {
	agentDir, modelsPath, err := o.paths()
	if err != nil {
		return Status{ID: o.id, Installed: false, Managed: false, TargetPath: nil, Endpoint: nil, Drift: false, Detail: failureReason("omp status", err), Blocked: true}
	}
	return ObservedIntegrationStatus(o.io, o.id, modelsPath, []string{agentDir}, func(path string) ManagedRead {
		content, ok, err := o.io.ReadText(path)
		if err != nil {
			return ManagedRead{Kind: ManagedUnsupported, Reason: failureReason("status read", err)}
		}
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return ompManagedRead(content)
	}, ProviderBaseUrl(o.port))
}

func (o *OmpIntegration) Rollback() ApplyResult { return o.rollbackWith(false) }

func (o *OmpIntegration) RollbackForced() ApplyResult { return o.rollbackWith(true) }

func (o *OmpIntegration) rollbackWith(force bool) ApplyResult {
	_, modelsPath, err := o.paths()
	if err != nil {
		return ToRollbackResult(o.id, recoveryRefusal("omp rollback", err))
	}
	return ToRollbackResult(o.id, stripOmpConfig(o.io, modelsPath, force))
}
