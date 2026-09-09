package integrations

import (
	"path/filepath"
	"strconv"
	"strings"
)

const piPrismApiKey = "prism-loopback"
const piPrismApi = "openai-completions"

// RenderPiLeaf renders the `providers.prism` member of pi's models.json:
// baseUrl/api/apiKey plus the model list with the input modalities pi's
// validator requires, the thinking-level map its effort picker reads, and
// token budgets clamped to a known context window.
func RenderPiLeaf(port int, models []Model, leafIndent int) string {
	pad := strings.Repeat(" ", leafIndent)
	inner := strings.Repeat(" ", leafIndent+2)
	item := strings.Repeat(" ", leafIndent+4)
	field := strings.Repeat(" ", leafIndent+6)
	lines := []string{
		pad + `"prism": {`,
		inner + `"baseUrl": ` + jsonString(ProviderBaseUrl(port)) + `,`,
		inner + `"api": ` + jsonString(piPrismApi) + `,`,
		inner + `"apiKey": ` + jsonString(piPrismApiKey) + `,`,
	}
	if len(models) == 0 {
		lines = append(lines, inner+`"models": []`)
	} else {
		lines = append(lines, inner+`"models": [`)
		for i, model := range models {
			comma := ","
			if i == len(models)-1 {
				comma = ""
			}
			lines = append(lines,
				item+`{`,
				field+`"id": `+jsonString(model.ID)+`,`,
				field+`"name": `+jsonString(model.Name)+`,`,
				field+`"input": [`,
			)
			inputs := []string{field + `  "text"`}
			if model.ImageInput {
				inputs = append(inputs, field+`  "image"`)
			}
			for _, entry := range inputs {
				lines = append(lines, entry+`,`)
			}
			lines[len(lines)-1] = strings.TrimSuffix(lines[len(lines)-1], ",")
			lines = append(lines, field+`]`)
			efforts := effortsFor(model, chatEffortVocabulary)
			if len(efforts) > 0 {
				lines[len(lines)-1] += ","
				lines = append(lines,
					field+`"reasoning": true,`,
					field+`"thinkingLevelMap": {`,
				)
				for _, level := range piThinkingLevels {
					value := "null"
					if containsString(efforts, level) {
						value = jsonString(level)
					}
					sep := ","
					if level == piThinkingLevels[len(piThinkingLevels)-1] {
						sep = ""
					}
					lines = append(lines, field+`  "`+level+`": `+value+sep)
				}
				lines = append(lines, field+`}`)
			}
			if model.ContextWindow > 0 {
				lines[len(lines)-1] += ","
				lines = append(lines,
					field+`"contextWindow": `+strconv.Itoa(model.ContextWindow)+`,`,
					field+`"maxTokens": `+strconv.Itoa(maxTokensFor(model.ContextWindow)),
				)
			}
			lines = append(lines, item+`}`+comma)
		}
		lines = append(lines, inner+`]`)
	}
	lines = append(lines, pad+`}`)
	return strings.Join(lines, "\n")
}

func piTransform(port int, models []Model) func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		result := UpsertJSONBlockLeaf(current, "providers", "prism", "models.json", func(leafIndent int) string {
			return RenderPiLeaf(port, models, leafIndent)
		})
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func piRollbackTransform() func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		result := RemoveJSONBlockLeaf(current, "providers", "prism", "models.json")
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func piManagedRead(content string) ManagedRead {
	return jsonLeafToManagedRead(ReadJSONBlockLeaf(content, "providers", "prism", "models.json", "baseUrl"))
}

type PiOptions struct {
	Port              int
	Models            []Model
	ModelsSource      func() []Model
	AgentDir          string
	ConfigPath        string
	Env               Env
	Home              string
	CrashBeforeRename bool
}

type PiIntegration struct {
	id         ID
	port       int
	models     []Model
	modelsSrc  func() []Model
	configPath string
	env        Env
	home       string
}

func (p *PiIntegration) paths() (agentDir string, configPath string, err error) {
	agentDir = p.home
	if p.configPath != "" {
		return agentDir, p.configPath, nil
	}
	configPath, err = PiConfigPath(p.env, p.home)
	if err != nil {
		return "", "", err
	}
	agentDir, err = PiAgentDir(p.env, p.home)
	if err != nil {
		return "", "", err
	}
	return agentDir, configPath, nil
}

func NewPi(options PiOptions) *PiIntegration {
	return &PiIntegration{id: Pi, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, configPath: options.ConfigPath, env: options.Env, home: options.Home}
}

func (p *PiIntegration) ID() ID { return p.id }

func (p *PiIntegration) Apply() ApplyResult {
	_, configPath, err := p.paths()
	if err != nil {
		return ApplyResult{OK: false, ID: p.id, Reason: failureReason("pi apply", err)}
	}
	models, refusal := resolveModels(p.models, p.modelsSrc, p.id)
	if refusal != "" {
		return ApplyResult{OK: false, ID: p.id, Reason: refusal}
	}
	outcome, err := ApplyConfigTransform(configPath, piTransform(p.port, models), false)
	if err != nil {
		return ToApplyResult(p.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("pi apply", err)})
	}
	return ToApplyResult(p.id, outcome)
}

func (p *PiIntegration) Status() Status {
	agentDir, configPath, err := p.paths()
	if err != nil {
		return Status{ID: p.id, Installed: false, Managed: false, TargetPath: nil, Endpoint: nil, Drift: false, Detail: failureReason("pi status", err)}
	}
	var detectDirs []string
	if _, hasOverride := p.env.lookup("PI_CODING_AGENT_DIR"); hasOverride {
		detectDirs = []string{agentDir}
	} else {
		detectDirs = []string{filepath.Join(p.home, ".pi")}
	}
	return ObservedIntegrationStatus(p.id, configPath, detectDirs, func(path string) ManagedRead {
		content, ok := ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return piManagedRead(content)
	}, ProviderBaseUrl(p.port))
}

func (p *PiIntegration) Rollback() ApplyResult {
	_, configPath, err := p.paths()
	if err != nil {
		return ApplyResult{OK: false, ID: p.id, Reason: failureReason("pi rollback", err)}
	}
	outcome, err := ApplyConfigTransform(configPath, piRollbackTransform(), false)
	if err != nil {
		return ToRollbackResult(p.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("pi rollback", err)})
	}
	return ToRollbackResult(p.id, outcome)
}

func RecoverPiConfig(configPath string) bool {
	return RecoverStaged(configPath)
}
