package integrations

import (
	"errors"
	"path/filepath"
)

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

func ompRefusal(scope string, err error) WriteOutcome {
	outcome := WriteOutcome{Kind: OutcomeRefused, Reason: failureReason(scope, err)}
	var conflict *journalConflictError
	if errors.As(err, &conflict) {
		outcome.Retryable = true
		outcome.Conflict = ConflictJournal
	}
	return outcome
}

func WriteOmpConfig(io FileIO, options OmpOptions) WriteOutcome {
	models, refusal := resolveModels(options.Models, options.ModelsSource, Omp)
	if refusal != "" {
		return WriteOutcome{Kind: OutcomeRefused, Reason: refusal}
	}
	spec := NewOmpSpec(options.Port, models)
	outcome, err := applyRestoration(io, options.ModelsPath, func(current string) ConfigTransform {
		return toTransform(UpsertProviderLeaf(current, "prism", spec))
	}, nil, options.CrashBeforeRename)
	if err != nil {
		return ompRefusal("omp apply", err)
	}
	return outcome
}

func StripOmpConfig(io FileIO, modelsPath string) WriteOutcome {
	outcome, err := rollbackRestoration(io, modelsPath, func(current string) ConfigTransform {
		return toTransform(RemoveProviderLeaf(current, "prism"))
	})
	if err != nil {
		return ompRefusal("omp rollback", err)
	}
	return outcome
}

func RecoverOmpConfig(io FileIO, modelsPath string) bool {
	return recoverConfigStage(io, modelsPath)
}

func (o *OmpIntegration) writeConfig(modelsPath string) WriteOutcome {
	return WriteOmpConfig(o.io, OmpOptions{ModelsPath: modelsPath, Port: o.port, Models: o.models, ModelsSource: o.modelsSrc})
}

func (o *OmpIntegration) Apply() ApplyResult {
	_, modelsPath, err := o.paths()
	if err != nil {
		return ApplyResult{OK: false, ID: o.id, Reason: failureReason("omp apply", err)}
	}
	return ToApplyResult(o.id, o.writeConfig(modelsPath))
}

func (o *OmpIntegration) ApplyForced() ApplyResult {
	_, modelsPath, err := o.paths()
	if err != nil {
		return ApplyResult{OK: false, ID: o.id, Reason: failureReason("omp apply", err)}
	}
	outcome := o.writeConfig(modelsPath)
	if outcome.Conflict == "" {
		return ToApplyResult(o.id, outcome)
	}
	backup, err := retireJournal(o.io, modelsPath)
	if err != nil {
		return ApplyResult{OK: false, ID: o.id, Reason: failureReason("omp apply", err)}
	}
	return withBackup(ToApplyResult(o.id, o.writeConfig(modelsPath)), backup)
}

func (o *OmpIntegration) Status() Status {
	agentDir, modelsPath, err := o.paths()
	if err != nil {
		return Status{ID: o.id, Installed: false, Managed: false, TargetPath: nil, Endpoint: nil, Drift: false, Detail: failureReason("omp status", err)}
	}
	status := ObservedIntegrationStatus(o.io, o.id, modelsPath, []string{agentDir}, func(path string) ManagedRead {
		content, ok, err := o.io.ReadText(path)
		if err != nil {
			return ManagedRead{Kind: ManagedUnsupported, Reason: failureReason("status read", err)}
		}
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return ompManagedRead(content)
	}, ProviderBaseUrl(o.port))
	if journalConflicts(o.io, modelsPath) {
		status.Conflict = ConflictJournal
	}
	return status
}

func (o *OmpIntegration) Rollback() ApplyResult {
	_, modelsPath, err := o.paths()
	if err != nil {
		return ApplyResult{OK: false, ID: o.id, Reason: failureReason("omp rollback", err)}
	}
	return ToRollbackResult(o.id, StripOmpConfig(o.io, modelsPath))
}

func (o *OmpIntegration) RollbackForced() ApplyResult {
	_, modelsPath, err := o.paths()
	if err != nil {
		return ApplyResult{OK: false, ID: o.id, Reason: failureReason("omp rollback", err)}
	}
	outcome := StripOmpConfig(o.io, modelsPath)
	if outcome.Conflict == "" {
		return ToRollbackResult(o.id, outcome)
	}
	backup, err := retireJournal(o.io, modelsPath)
	if err != nil {
		return ApplyResult{OK: false, ID: o.id, Reason: failureReason("omp rollback", err)}
	}
	return withBackup(ToRollbackResult(o.id, StripOmpConfig(o.io, modelsPath)), backup)
}

func withBackup(result ApplyResult, backup string) ApplyResult {
	if backup == "" {
		return result
	}
	result.Backup = backup
	if !result.OK {
		result.Reason += "; old journal kept at " + backup
	}
	return result
}
