package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type Options struct {
	BaseURL string
	APIKey  string
	Goldens map[string]Golden
}

type CodecCheckResult struct {
	CaseID            string
	Method            string
	URL               string
	BodyBytes         int
	GoldenMatched     bool
	HeaderCaseMatched bool
	Diff              string
}

type CaseResult struct {
	ScenarioID             string
	Suite                  string
	Passed                 bool
	Classification         Classification
	SecondaryCode          string
	AssertionResults       []AssertionResult
	ExpectedFailureMatched *bool
	Diagnostics            []string
	Codec                  *CodecCheckResult
}

func ResolveProtocolExecutionContext(c Case) (inbound, upstream, surface string) {
	inbound = "openai-responses"
	if len(c.Requirements.InboundProtocols) > 0 {
		inbound = c.Requirements.InboundProtocols[0]
	}
	upstream = "openai-chat"
	if len(c.Requirements.UpstreamProtocols) > 0 {
		upstream = c.Requirements.UpstreamProtocols[0]
	}
	surface = "responses-http"
	if len(c.Requirements.Surfaces) > 0 {
		surface = c.Requirements.Surfaces[0]
	}
	if c.ID == "responses-core.protocol.json-sse-equivalence" {
		surface = "responses-sse"
	}
	return inbound, upstream, surface
}

type Runner struct {
	Build          RequestBuilder
	ResponsesBuild RequestBuilder
}

func NewRunner(b RequestBuilder) *Runner {
	return &Runner{Build: b}
}

func NewRoutedRunner(b RequestBuilder, responses RequestBuilder) *Runner {
	return &Runner{Build: b, ResponsesBuild: responses}
}

func (r *Runner) builderFor(upstream string) RequestBuilder {
	if upstream == "openai-responses" && r.ResponsesBuild != nil {
		return r.ResponsesBuild
	}
	return r.Build
}

func (r *Runner) Run(ctx context.Context, c Case, opts Options) CaseResult {
	inbound, upstream, _ := ResolveProtocolExecutionContext(c)
	base := CaseResult{ScenarioID: c.ID, Suite: c.Suite}
	fail := func(class Classification, code, diag string) CaseResult {
		base.Classification = class
		base.SecondaryCode = code
		base.Diagnostics = append(base.Diagnostics, diag)
		return base
	}

	if upstream != "openai-chat" && upstream != "openai-responses" && upstream != "proto-stub" {
		return fail(ClassHarnessFailure, "execution_error",
			fmt.Sprintf("unit 2 (SSE egress): upstream protocol %s not implemented in unit 1", upstream))
	}
	if FixtureDigest([]byte(c.Fixture.Bytes)) != c.Fixture.Digest {
		return fail(ClassHarnessFailure, "contract_integrity",
			fmt.Sprintf("fixture digest mismatch: expected %s, got %s", c.Fixture.Digest, FixtureDigest([]byte(c.Fixture.Bytes))))
	}
	if c.InitiatingRequest != nil && FixtureDigest([]byte(c.InitiatingRequest.Bytes)) != c.InitiatingRequest.Digest {
		return fail(ClassHarnessFailure, "contract_integrity",
			fmt.Sprintf("initiatingRequest digest mismatch: expected %s, got %s", c.InitiatingRequest.Digest, FixtureDigest([]byte(c.InitiatingRequest.Bytes))))
	}

	buildOpts := BuildOptions{
		BaseURL:          opts.BaseURL,
		APIKey:           opts.APIKey,
		UpstreamProtocol: upstream,
	}
	if buildOpts.BaseURL == "" {
		buildOpts.BaseURL = "https://api.openai.com/v1"
	}
	if buildOpts.APIKey == "" {
		buildOpts.APIKey = "fixture-key"
	}

	var obs *Observation
	switch c.Fixture.Role {
	case RoleAdapterVector:
		if c.Suite == "tools-core" {
			built, err := runToolsCoreAdapterVector(c, r, buildOpts)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("tools-core adapter vector: %v", err))
			}
			obs = built
			break
		}
		if c.ID == "responses-core.protocol.json-sse-equivalence" {
			var vector map[string]any
			if err := json.Unmarshal([]byte(c.Fixture.Bytes), &vector); err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("fixture decode: %v", err))
			}
			sse, _ := vector["sse"].(string)
			events, err := NormalizeSseBytes([]byte(sse), "openai-responses")
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("sse normalize: %v", err))
			}
			obs = Empty()
			FinalizeObservation(obs, events, vector["json"], 200)
			AttachVerifiers(obs, c)
			break
		}
		if c.ID == "vision-core.protocol.modality-gate" {
			obs = Empty()
			AttachVerifiers(obs, c)
			break
		}
		var vector map[string]any
		if err := json.Unmarshal([]byte(c.Fixture.Bytes), &vector); err != nil {
			return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("fixture decode: %v", err))
		}
		if c.ID == "vision-core.protocol.tool-result-image" {
			req, err := toolResultRequest(vector)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("tool-result vector: %v", err))
			}
			built, err := r.Build.Build(ctx, req, buildOpts)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
			}
			base.Codec = &CodecCheckResult{
				CaseID: c.ID, Method: built.Method, URL: built.URL, BodyBytes: len(built.Body),
			}
			obs = Empty()
			RecordUpstreamRequest(obs, built)
			AttachVerifiers(obs, c)
			break
		}
		requests, err := r.buildReasoningVector(ctx, c, vector, buildOpts)
		if err != nil && !errors.Is(err, errUnhandledAdapterVector) {
			return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
		}
		if errors.Is(err, errUnhandledAdapterVector) {
			if upstream != "openai-chat" {
				return fail(ClassHarnessFailure, "execution_error",
					fmt.Sprintf("unsupported adapter_vector scenario %s", c.ID))
			}
			built, err := r.Build.Build(ctx, VectorToRequest(vector), buildOpts)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
			}
			requests = []*UpstreamRequest{built}
		}
		last := requests[len(requests)-1]
		codec := &CodecCheckResult{
			CaseID: c.ID, Method: last.Method, URL: last.URL, BodyBytes: len(last.Body),
		}
		want, hasGolden := opts.Goldens[c.ID]
		if hasGolden {
			codec.GoldenMatched, codec.HeaderCaseMatched, codec.Diff = goldenDiff(last, want)
		}
		base.Codec = codec
		if codec.Diff != "" {
			return fail(ClassHarnessFailure, "contract_integrity", "codec emitted wrong wire bytes: "+codec.Diff)
		}
		obs = Empty()
		for _, req := range requests {
			RecordUpstreamRequest(obs, req)
		}
	case RoleClientRequest:
		if c.ID == "vision-core.protocol.input-image" {
			req, err := RequestFromWire(inbound, []byte(c.Fixture.Bytes))
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("ingress parse: %v", err))
			}
			built, err := r.builderFor(upstream).Build(ctx, req, buildOpts)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
			}
			base.Codec = &CodecCheckResult{
				CaseID: c.ID, Method: built.Method, URL: built.URL, BodyBytes: len(built.Body),
			}
			if want, ok := opts.Goldens[c.ID]; ok {
				base.Codec.GoldenMatched, base.Codec.HeaderCaseMatched, base.Codec.Diff = goldenDiff(built, want)
				if base.Codec.Diff != "" {
					return fail(ClassHarnessFailure, "contract_integrity", "codec emitted wrong wire bytes: "+base.Codec.Diff)
				}
			}
			obs = Empty()
			RecordUpstreamRequest(obs, built)
			AttachVerifiers(obs, c)
			break
		}
		if c.ID == "tools-core.protocol.choice-and-allowed-set" {
			var vector map[string]any
			if err := json.Unmarshal([]byte(c.Fixture.Bytes), &vector); err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("fixture decode: %v", err))
			}
			built, err := r.Build.Build(ctx, VectorToRequest(vector), buildOpts)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
			}
			obs = Empty()
			RecordUpstreamRequest(obs, built)
			AttachVerifiers(obs, c)
			break
		}
		return fail(ClassHarnessFailure, "execution_error",
			fmt.Sprintf("unit 2 (SSE egress): fixture role %s not implemented in unit 1", c.Fixture.Role))
	case RoleUpstreamResponse:
		if c.ID == "tools-core.protocol.parallel-correlation" {
			built, err := runToolsCoreChatSse(c, r, buildOpts)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("tools-core sse: %v", err))
			}
			obs = built
			break
		}
		if upstream == "openai-chat" {
			obs = Empty()
			if c.InitiatingRequest != nil {
				var vector map[string]any
				if err := json.Unmarshal([]byte(c.InitiatingRequest.Bytes), &vector); err != nil {
					return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("initiating request decode: %v", err))
				}
				built, err := r.Build.Build(ctx, VectorToRequest(vector), buildOpts)
				if err != nil {
					return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
				}
				RecordUpstreamRequest(obs, built)
			}
			if c.Fixture.MediaType == "text/event-stream" {
				events, err := NormalizeSseBytes([]byte(c.Fixture.Bytes), "openai-chat")
				if err != nil {
					return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("sse normalize: %v", err))
				}
				FinalizeChatObservation(obs, events, nil, 200)
			} else {
				var body any
				if err := json.Unmarshal([]byte(c.Fixture.Bytes), &body); err != nil {
					return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("response decode: %v", err))
				}
				FinalizeChatObservation(obs, nil, body, 200)
			}
			AttachVerifiers(obs, c)
			break
		}
		if upstream != "openai-responses" || inbound != "openai-responses" {
			return fail(ClassHarnessFailure, "execution_error",
				fmt.Sprintf("unit 2 (SSE egress): upstream %s inbound %s not implemented in unit 1", upstream, inbound))
		}
		events, err := NormalizeSseBytes([]byte(c.Fixture.Bytes), upstream)
		if err != nil {
			return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("sse normalize: %v", err))
		}
		obs = Empty()
		if c.InitiatingRequest != nil {
			var vector map[string]any
			if err := json.Unmarshal([]byte(c.InitiatingRequest.Bytes), &vector); err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("initiating request decode: %v", err))
			}
			built, err := r.builderFor(upstream).Build(ctx, VectorToRequest(vector), buildOpts)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
			}
			RecordUpstreamRequest(obs, built)
		}
		FinalizeObservation(obs, events, nil, 200)
		AttachVerifiers(obs, c)
	case RoleSyntheticTool:
		stubObs, err := ExecuteMcpSyntheticAction(c)
		if err != nil {
			return fail(ClassHarnessFailure, "execution_error", err.Error())
		}
		obs = stubObs
		AttachMcpVerifiers(obs, c)
		AttachVerifiers(obs, c)
	default:
		return fail(ClassHarnessFailure, "execution_error",
			fmt.Sprintf("unit 2 (SSE egress): fixture role %s not implemented in unit 1", c.Fixture.Role))
	}

	obsJSON, err := obs.JSON()
	if err != nil {
		return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("observation serialize: %v", err))
	}
	obsTree, ok := decodeJSON(obsJSON).(map[string]any)
	if !ok {
		return fail(ClassHarnessFailure, "execution_error", "observation must decode to an object")
	}
	results := (&Evaluator{Obs: obsTree}).Evaluate(c.Assertions)
	base.AssertionResults = results
	requiredFailures := 0
	for _, res := range results {
		if res.Required && !res.Passed {
			requiredFailures++
		}
	}
	if c.ExpectedFailure != nil {
		if len(c.ExpectedFailure.AssertionIDs) == 0 {
			return fail(ClassHarnessFailure, "execution_error",
				fmt.Sprintf("invalid_manifest: negative control %s lists no assertionIds", c.ID))
		}
		controlPassed := true
		for _, id := range c.ExpectedFailure.AssertionIDs {
			found := false
			for _, res := range results {
				if res.ID == id && res.Passed {
					found = true
				}
			}
			if !found {
				controlPassed = false
			}
		}
		matched := controlPassed && requiredFailures == 0
		base.ExpectedFailureMatched = &matched
		base.Passed = matched
		if matched {
			base.Classification = Classification(c.ExpectedFailure.ExpectedClass)
			base.SecondaryCode = c.ExpectedFailure.ExpectedCode
		} else {
			base.Classification = ClassProtocolFailure
			base.SecondaryCode = "deterministic_assertion"
		}
		return base
	}
	base.Passed = requiredFailures == 0
	if base.Passed {
		base.Classification = ClassInconclusive
		base.SecondaryCode = "unclassified"
	} else {
		base.Classification = ClassProtocolFailure
		base.SecondaryCode = "deterministic_assertion"
	}
	return base
}
