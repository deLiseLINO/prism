package integrations

import "strconv"

const opencodeProviderNPM = "@ai-sdk/openai-compatible"

// RenderOpencodeModels renders the shared `models` map both opencode
// generations read: one entry per prism model, keyed by the model id.
func RenderOpencodeModels(models []Model, inner string, item string, field string) []string {
	if len(models) == 0 {
		return []string{inner + `"models": {}`}
	}
	lines := []string{inner + `"models": {`}
	for i, model := range models {
		comma := ","
		if i == len(models)-1 {
			comma = ""
		}
		nameLine := field + `"name": ` + jsonString(model.Name)

		if model.ContextWindow > 0 {
			// opencode's schema takes the limit pair together or not at all;
			// an unknown window keeps the client's own defaults.
			nameLine += ","
		}
		lines = append(lines,
			item+jsonString(model.ID)+`: {`,
			nameLine,
		)
		if model.ContextWindow > 0 {
			lines = append(lines,
				field+`"limit": {`,
				field+`  "context": `+strconv.Itoa(model.ContextWindow)+`,`,
				field+`  "output": `+strconv.Itoa(maxTokensFor(model.ContextWindow)),
				field+`}`,
			)
		}
		lines = append(lines, item+`}`+comma)
	}
	lines = append(lines, inner+`}`)
	return lines
}

// RenderOpencodeLeaf renders the `provider.prism` member (singular key) of
// opencode v1's opencode.json: an openai-compatible npm provider block.
func RenderOpencodeLeaf(port int, models []Model, leafIndent int) string {
	pad := repeatSpaces(leafIndent)
	inner := repeatSpaces(leafIndent + 2)
	item := repeatSpaces(leafIndent + 4)
	field := repeatSpaces(leafIndent + 6)
	lines := []string{
		pad + `"prism": {`,
		inner + `"name": "Prism",`,
		inner + `"npm": ` + jsonString(opencodeProviderNPM) + `,`,
		inner + `"options": {`,
		item + `"baseURL": ` + jsonString(ProviderBaseUrl(port)) + `,`,
		item + `"apiKey": ` + jsonString(prismApiKey),
		inner + `},`,
	}
	lines = append(lines, RenderOpencodeModels(models, inner, item, field)...)
	lines = append(lines, pad+`}`)
	return joinStrings(lines)
}

func opencodeTransform(port int, models []Model) func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		result := UpsertJSONBlockLeaf(current, "provider", "prism", "opencode.json", func(leafIndent int) string {
			return RenderOpencodeLeaf(port, models, leafIndent)
		})
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func opencodeRollbackTransform() func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		result := RemoveJSONBlockLeaf(current, "provider", "prism", "opencode.json")
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func opencodeManagedRead(content string) ManagedRead {
	read := ReadJSONBlockLeaf(content, "provider", "prism", "opencode.json", "baseURL")
	return jsonLeafToManagedRead(read)
}

type OpencodeOptions struct {
	Port              int
	Models            []Model
	ModelsSource      func() []Model
	ConfigPath        string
	Env               Env
	Home              string
	IO                FileIO
	CrashBeforeRename bool
}

type OpencodeIntegration struct {
	id         ID
	port       int
	models     []Model
	modelsSrc  func() []Model
	io         FileIO
	configPath string
	env        Env
	home       string
}

func (o *OpencodeIntegration) paths() string {
	if o.configPath != "" {
		return o.configPath
	}
	return OpencodeConfigPath(o.env, o.home)
}

func NewOpencode(options OpencodeOptions) *OpencodeIntegration {
	return &OpencodeIntegration{id: Opencode, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, io: withLocalIO(options.IO), configPath: options.ConfigPath, env: options.Env, home: options.Home}
}

func (o *OpencodeIntegration) ID() ID { return o.id }

func (o *OpencodeIntegration) Apply() ApplyResult {
	models, refusal := resolveModels(o.models, o.modelsSrc, o.id)
	if refusal != "" {
		return ApplyResult{OK: false, ID: o.id, Reason: refusal}
	}
	outcome, err := ApplyConfigTransform(o.io, o.paths(), opencodeTransform(o.port, models), false)
	if err != nil {
		return ToApplyResult(o.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode apply", err)})
	}
	return ToApplyResult(o.id, outcome)
}

func (o *OpencodeIntegration) Status() Status {
	return ObservedIntegrationStatus(o.io, o.id, o.paths(), []string{OpencodeConfigDir(o.env, o.home)}, func(path string) ManagedRead {
		content, ok := o.io.ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return opencodeManagedRead(content)
	}, ProviderBaseUrl(o.port))
}

func (o *OpencodeIntegration) Rollback() ApplyResult {
	outcome, err := ApplyConfigTransform(o.io, o.paths(), opencodeRollbackTransform(), false)
	if err != nil {
		return ToRollbackResult(o.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode rollback", err)})
	}
	return ToRollbackResult(o.id, outcome)
}

func RecoverOpencodeConfig(io FileIO, configPath string) bool {
	return io.RecoverStaged(configPath)
}
