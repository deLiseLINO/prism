package conformance

import (
	"context"
	"encoding/json"
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
		if upstream != "openai-chat" {
			return fail(ClassHarnessFailure, "execution_error",
				fmt.Sprintf("unsupported adapter_vector scenario %s", c.ID))
		}
		var vector map[string]any
		if err := json.Unmarshal([]byte(c.Fixture.Bytes), &vector); err != nil {
			return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("fixture decode: %v", err))
		}
		built, err := r.Build.Build(ctx, VectorToRequest(vector), buildOpts)
		if err != nil {
			return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
		}
		codec := &CodecCheckResult{
			CaseID: c.ID, Method: built.Method, URL: built.URL, BodyBytes: len(built.Body),
		}
		want, hasGolden := opts.Goldens[c.ID]
		if hasGolden {
			codec.GoldenMatched, codec.HeaderCaseMatched, codec.Diff = goldenDiff(built, want)
		} else {
			codec.Diff = "missing golden record"
		}
		base.Codec = codec
		if codec.Diff != "" {
			return fail(ClassHarnessFailure, "contract_integrity", "codec emitted wrong wire bytes: "+codec.Diff)
		}
		obs = Empty()
		RecordUpstreamRequest(obs, built)
	case RoleUpstreamResponse:
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
