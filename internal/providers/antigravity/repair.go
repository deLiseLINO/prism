package antigravity

import (
	"encoding/json"
	"regexp"
	"strconv"
)

var schemaErrorPattern = regexp.MustCompile(`(?:input[_ ]schema|json schema|function[_ ]declarations?|x-mcp-header)`)

var thinkingErrorPattern = regexp.MustCompile(`thinking[_ ]?(?:config|level)`)

var declarationIndexPattern = regexp.MustCompile(`function[_]?declarations(?:\.|\[)(\d+)`)

const emptyObjectSchema = `{"type":"object","properties":{}}`

var signatureErrorPattern = regexp.MustCompile(`missing a thought_signature in functionCall parts`)

func repairEnvelope(body []byte, errorPayload string) ([]byte, bool) {
	schemaError := schemaErrorPattern.MatchString(errorPayload)
	thinkingError := thinkingErrorPattern.MatchString(errorPayload)
	signatureError := signatureErrorPattern.MatchString(errorPayload)
	if !schemaError && !thinkingError && !signatureError {
		return nil, false
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, false
	}
	changed := false
	if thinkingError && env.Request.GenerationConfig != nil && len(env.Request.GenerationConfig.ThinkingConfig) > 0 {
		env.Request.GenerationConfig.ThinkingConfig = nil
		if env.Request.GenerationConfig.empty() {
			env.Request.GenerationConfig = nil
		}
		changed = true
	}
	if schemaError {
		changed = repairDeclarations(&env, errorPayload) || changed
	}
	if signatureError {
		changed = repairSignatures(&env) || changed
	}
	if !changed {
		return nil, false
	}
	repaired, err := marshalCompact(env)
	if err != nil {
		return nil, false
	}
	return repaired, true
}

func repairDeclarations(env *envelope, errorPayload string) bool {
	var declarations []*geminiFunctionDeclaration
	for ti := range env.Request.Tools {
		for di := range env.Request.Tools[ti].FunctionDeclarations {
			decl := &env.Request.Tools[ti].FunctionDeclarations[di]
			if len(decl.Parameters) > 0 {
				declarations = append(declarations, decl)
			}
		}
	}
	if len(declarations) == 0 {
		return false
	}
	index := -1
	if m := declarationIndexPattern.FindStringSubmatch(errorPayload); m != nil {
		index, _ = strconv.Atoi(m[1])
	}
	targets := declarations
	if index >= 0 && index < len(declarations) {
		targets = []*geminiFunctionDeclaration{declarations[index]}
	}
	for _, decl := range targets {
		decl.Parameters = json.RawMessage(emptyObjectSchema)
		decl.ParametersJSONSchema = nil
	}
	return true
}

func repairSignatures(env *envelope) bool {
	changed := false
	for ci := range env.Request.Contents {
		for pi := range env.Request.Contents[ci].Parts {
			part := &env.Request.Contents[ci].Parts[pi]
			if part.FunctionCall == nil || part.ThoughtSignature != "" {
				continue
			}
			part.ThoughtSignature = signatureSentinel
			changed = true
		}
	}
	return changed
}
