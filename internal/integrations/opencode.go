package integrations

import (
	"encoding/json"
	"strconv"
)

const opencodeProviderNPM = "@ai-sdk/openai-compatible"

const opencodeProviderPackage = "@opencode/ai/providers/openai-compatible"

func renderOpencodeModernLeaf(port int, models []Model, indent int) string {
	entries := make(map[string]any, len(models))
	for _, m := range models {
		input := []string{"text"}
		if m.ImageInput {
			input = append(input, "image")
		}
		entry := map[string]any{"name": m.Name, "capabilities": map[string][]string{"input": input, "output": {"text"}}}
		if m.ContextWindow > 0 {
			entry["limit"] = map[string]int{"context": m.ContextWindow, "output": maxTokensFor(m.ContextWindow)}
		}
		efforts := effortsFor(m, chatEffortVocabulary)
		variants := make([]any, 0, len(efforts))
		for _, effort := range efforts {
			variants = append(variants, map[string]any{"id": effort, "body": map[string]string{"reasoning_effort": effort}})
		}
		entry["variants"] = variants
		if effort := defaultEffort(efforts, m.DefaultReasoningEffort); effort != "" {
			entry["settings"] = map[string]string{"reasoningEffort": effort}
		}
		entries[m.ID] = entry
	}
	provider := map[string]any{"name": "Prism", "package": opencodeProviderPackage, "settings": map[string]string{"baseURL": ProviderBaseUrl(port), "apiKey": prismApiKey}, "models": entries}
	pad := repeatSpaces(indent)
	body, _ := json.MarshalIndent(provider, pad, "  ")
	return pad + `"prism": ` + string(body)
}

// RenderOpencodeModels renders the v1 `models` map: one entry per prism model,
// keyed by the model id, with a reasoning-effort variant map when the model
// declares one.
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
		efforts := effortsFor(model, chatEffortVocabulary)
		hasReasoning := len(efforts) > 0
		nameLine := field + `"name": ` + jsonString(model.Name) + ","
		lines = append(lines,
			item+jsonString(model.ID)+`: {`,
			nameLine,
		)
		inputs := []string{"text"}
		if model.ImageInput {
			inputs = append(inputs, "image")
		}
		modalities, _ := json.Marshal(map[string][]string{"input": inputs, "output": {"text"}})
		modalityLine := field + `"modalities": ` + string(modalities)
		if model.ContextWindow > 0 || hasReasoning {
			modalityLine += ","
		}
		lines = append(lines, field+`"attachment": `+strconv.FormatBool(model.ImageInput)+`,`, modalityLine)
		if model.ContextWindow > 0 {
			// opencode's schema takes the limit pair together or not at all;
			// an unknown window keeps the client's own defaults.
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
			// v1 surfaces the effort picker as per-model variants; each value
			// names an AI SDK model option the openai-compatible package
			// lowers to a reasoning_effort wire field.
			if effort := defaultEffort(efforts, model.DefaultReasoningEffort); effort != "" {
				lines = append(lines, field+`"options": {"reasoningEffort": `+jsonString(effort)+`},`)
			}
			lines = append(lines, renderOpencodeReasoningV1(efforts, field)...)
		}
		lines = append(lines, item+`}`+comma)
	}
	lines = append(lines, inner+`}`)
	return lines
}

// renderOpencodeReasoningV1 emits the capability flag and the variant map the
// openai-compatible npm path reads.
func renderOpencodeReasoningV1(efforts []string, field string) []string {
	lines := []string{
		field + `"reasoning": true,`,
		field + `"variants": {`,
	}
	for i, effort := range efforts {
		comma := ","
		if i == len(efforts)-1 {
			comma = ""
		}
		lines = append(lines,
			field+`  `+jsonString(effort)+`: {`,
			field+`    "reasoningEffort": `+jsonString(effort),
			field+`  }`+comma,
		)
	}
	return append(lines, field+`}`)
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
		legacy := UpsertJSONBlockLeaf(current, "provider", "prism", "opencode.json", func(indent int) string { return RenderOpencodeLeaf(port, models, indent) })
		if legacy.Kind != "written" {
			return refusedTransform(legacy.Reason)
		}
		modern := UpsertJSONBlockLeaf(legacy.Next, "providers", "prism", "opencode.json", func(indent int) string { return renderOpencodeModernLeaf(port, models, indent) })
		if modern.Kind != "written" {
			return refusedTransform(modern.Reason)
		}
		return nextTransform(modern.Next, legacy.Changed || modern.Changed)
	}
}

func opencodeRollbackTransform() func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		legacy := RemoveJSONBlockLeaf(current, "provider", "prism", "opencode.json")
		if legacy.Kind != "written" {
			return refusedTransform(legacy.Reason)
		}
		modern := RemoveJSONBlockLeaf(legacy.Next, "providers", "prism", "opencode.json")
		if modern.Kind != "written" {
			return refusedTransform(modern.Reason)
		}
		return nextTransform(modern.Next, legacy.Changed || modern.Changed)
	}
}

func opencodeManagedRead(content string) ManagedRead {
	legacy := ReadJSONBlockLeaf(content, "provider", "prism", "opencode.json", "baseURL")
	modern := ReadJSONBlockLeaf(content, "providers", "prism", "opencode.json", "baseURL")
	if legacy.Kind == jsonLeafRefused {
		return jsonLeafToManagedRead(legacy)
	}
	if modern.Kind == jsonLeafRefused {
		return jsonLeafToManagedRead(modern)
	}
	if legacy.Kind == jsonLeafAbsent {
		return jsonLeafToManagedRead(modern)
	}
	if modern.Kind == jsonLeafAbsent {
		return jsonLeafToManagedRead(legacy)
	}
	if legacy.Endpoint == nil || modern.Endpoint == nil || *legacy.Endpoint != *modern.Endpoint {
		return ManagedRead{Kind: ManagedDamaged, Reason: "prism: managed client generation endpoints disagree"}
	}
	return jsonLeafToManagedRead(legacy)
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
	path := OpencodeConfigPath(o.env, o.home)
	jsonc := path + "c"
	if _, present, err := o.io.ReadText(jsonc); present || err != nil {
		return jsonc
	}
	return path
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
	outcome, err := applyRestoration(o.io, o.paths(), opencodeTransform(o.port, models), nil, false)
	if err != nil {
		return ToApplyResult(o.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode apply", err)})
	}
	return ToApplyResult(o.id, outcome)
}

func (o *OpencodeIntegration) Status() Status {
	return ObservedIntegrationStatus(o.io, o.id, o.paths(), []string{OpencodeConfigDir(o.env, o.home)}, func(path string) ManagedRead {
		content, ok, err := o.io.ReadText(path)
		if err != nil {
			return ManagedRead{Kind: ManagedUnsupported, Reason: failureReason("status read", err)}
		}
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return opencodeManagedRead(content)
	}, ProviderBaseUrl(o.port))
}

func (o *OpencodeIntegration) Rollback() ApplyResult {
	outcome, err := rollbackRestoration(o.io, o.paths(), opencodeRollbackTransform())
	if err != nil {
		return ToRollbackResult(o.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("opencode rollback", err)})
	}
	return ToRollbackResult(o.id, outcome)
}

func RecoverOpencodeConfig(io FileIO, configPath string) bool {
	return recoverConfigStage(io, configPath)
}
