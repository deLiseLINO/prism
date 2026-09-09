package integrations

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
	lines = append(lines, RenderOpencodeModels(models, inner, item, field)...)
	lines = append(lines, pad+`}`)
	return joinStrings(lines)
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
