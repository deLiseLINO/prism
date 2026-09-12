package integrations

import "strconv"

const opencode2ProviderPackage = "@opencode-ai/ai/providers/openai-compatible"

// RenderOpencode2Leaf renders the `providers.prism` member (plural key) of
// opencode v2's opencode.json: a package-based provider with settings.
func RenderOpencode2Leaf(port int, models []Model, leafIndent int) string {
	pad := repeatSpaces(leafIndent)
	inner := repeatSpaces(leafIndent + 2)
	item := repeatSpaces(leafIndent + 4)
	field := repeatSpaces(leafIndent + 6)
	lines := []string{
		pad + `"prism": {`,
		inner + `"name": "Prism",`,
		inner + `"package": ` + jsonString(opencode2ProviderPackage) + `,`,
		inner + `"settings": {`,
		item + `"baseURL": ` + jsonString(ProviderBaseUrl(port)) + `,`,
		item + `"apiKey": ` + jsonString(prismApiKey),
		inner + `},`,
	}
	lines = append(lines, renderOpencode2Models(models, inner, item, field)...)
	lines = append(lines, pad+`}`)
	return joinStrings(lines)
}

// renderOpencode2Models writes the v2 `models` map: one entry per prism model
// with a reasoning-effort variant list when the model declares one.
func renderOpencode2Models(models []Model, inner string, item string, field string) []string {
	if len(models) == 0 {
		return []string{inner + `"models": {}`}
	}
	lines := []string{inner + `"models": {`}
	for i, model := range models {
		comma := ","
		if i == len(models)-1 {
			comma = ""
		}
		efforts := effortsFor(model, chatEffortVocabulary)
		hasReasoning := len(efforts) > 0
		nameLine := field + `"name": ` + jsonString(model.Name)

		if model.ContextWindow > 0 || hasReasoning {
			nameLine += ","
		}
		lines = append(lines,
			item+jsonString(model.ID)+`: {`,
			nameLine,
		)
		if model.ContextWindow > 0 {
			// v2's schema takes the limit pair together or not at all; an
			// unknown window keeps the client's own defaults.
			limitClose := field + `}`
			if hasReasoning {
				limitClose += ","
			}
			lines = append(lines,
				field+`"limit": {`,
				field+`  "context": `+strconv.Itoa(model.ContextWindow)+`,`,
				field+`  "output": `+strconv.Itoa(maxTokensFor(model.ContextWindow)),
				limitClose,
			)
		}
		if hasReasoning {
			// v2 surfaces the effort picker as per-model variants; the body is
			// merged into the chat wire verbatim, so the reasoning_effort key
			// must already be in its wire (snake_case) spelling.
			lines = append(lines, renderOpencode2Reasoning(efforts, field)...)
		}
		lines = append(lines, item+`}`+comma)
	}
	return append(lines, inner+`}`)
}

// renderOpencode2Reasoning emits the variant array the v2 catalog reads.
func renderOpencode2Reasoning(efforts []string, field string) []string {
	lines := []string{
		field + `"variants": [`,
	}
	for i, effort := range efforts {
		comma := ","
		if i == len(efforts)-1 {
			comma = ""
		}
		lines = append(lines,
			field+`  {`,
			field+`    "id": `+jsonString(effort)+`,`,
			field+`    "body": {`,
			field+`      "reasoning_effort": `+jsonString(effort),
			field+`    }`,
			field+`  }`+comma,
		)
	}
	return append(lines, field+`]`)
}

func opencode2Transform(port int, models []Model) func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		result := UpsertJSONBlockLeaf(current, "providers", "prism", "opencode.json", func(leafIndent int) string {
			return RenderOpencode2Leaf(port, models, leafIndent)
		})
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func opencode2RollbackTransform() func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		result := RemoveJSONBlockLeaf(current, "providers", "prism", "opencode.json")
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func opencode2ManagedRead(content string) ManagedRead {
	read := ReadJSONBlockLeaf(content, "providers", "prism", "opencode.json", "baseURL")
	return jsonLeafToManagedRead(read)
}

type Opencode2Options struct {
	Port              int
	Models            []Model
	ModelsSource      func() []Model
	ConfigPath        string
	Env               Env
	Home              string
	IO                FileIO
	CrashBeforeRename bool
}

type Opencode2Integration struct {
	id         ID
	port       int
	models     []Model
	modelsSrc  func() []Model
	io         FileIO
	configPath string
	env        Env
	home       string
}

func (o *Opencode2Integration) paths() string {
	if o.configPath != "" {
		return o.configPath
	}
	return OpencodeConfigPath(o.env, o.home)
}

func NewOpencode2(options Opencode2Options) *Opencode2Integration {
	return &Opencode2Integration{id: Opencode2, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, io: withLocalIO(options.IO), configPath: options.ConfigPath, env: options.Env, home: options.Home}
}

func (o *Opencode2Integration) ID() ID { return o.id }

func (o *Opencode2Integration) Apply() ApplyResult {
	models, refusal := resolveModels(o.models, o.modelsSrc, o.id)
	if refusal != "" {
		return ApplyResult{OK: false, ID: o.id, Reason: refusal}
	}
	outcome, err := ApplyConfigTransform(o.io, o.paths(), opencode2Transform(o.port, models), false)
	if err != nil {
		return ToApplyResult(o.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode2 apply", err)})
	}
	return ToApplyResult(o.id, outcome)
}

func (o *Opencode2Integration) Status() Status {
	return ObservedIntegrationStatus(o.io, o.id, o.paths(), []string{OpencodeConfigDir(o.env, o.home)}, func(path string) ManagedRead {
		content, ok := o.io.ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return opencode2ManagedRead(content)
	}, ProviderBaseUrl(o.port))
}

func (o *Opencode2Integration) Rollback() ApplyResult {
	outcome, err := ApplyConfigTransform(o.io, o.paths(), opencode2RollbackTransform(), false)
	if err != nil {
		return ToRollbackResult(o.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode2 rollback", err)})
	}
	return ToRollbackResult(o.id, outcome)
}

func RecoverOpencode2Config(io FileIO, configPath string) bool {
	return io.RecoverStaged(configPath)
}
