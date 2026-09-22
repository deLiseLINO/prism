package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"prism/internal/canon"
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
	GoldenPinned      bool
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

	if upstream != "openai-chat" && upstream != "openai-responses" {
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
	if upstream == "openai-responses" {
		buildOpts.BaseURL = "http://127.0.0.1:1/v1"
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
		var built *UpstreamRequest
		var pre []*UpstreamRequest
		var bridged []NormalizedEvent
		var err error
		switch c.ID {
		case "codex-core.protocol.tool-continuation":
			if r.ResponsesBuild == nil {
				return fail(ClassHarnessFailure, "execution_error", "codec build: responses builder unavailable")
			}
			built, err = buildCodexToolContinuation(ctx, vector, r.ResponsesBuild, buildOpts)
		case "codex-core.protocol.previous-response-replay":
			if r.ResponsesBuild == nil {
				return fail(ClassHarnessFailure, "execution_error", "codec build: responses builder unavailable")
			}
			built, err = buildCodexPreviousResponseReplay([]byte(c.Fixture.Bytes))
		case "codex-core.protocol.apply-patch-turn":
			pre = append(pre, codexApplyPatchRequest0())
			built, bridged, err = buildCodexApplyPatch(ctx, vector, r.Build, buildOpts)
		default:
			requests, rerr := r.buildReasoningVector(ctx, c, vector, buildOpts)
			if rerr != nil && !errors.Is(rerr, errUnhandledAdapterVector) {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", rerr))
			}
			if errors.Is(rerr, errUnhandledAdapterVector) {
				if upstream != "openai-chat" {
					return fail(ClassHarnessFailure, "execution_error",
						fmt.Sprintf("unsupported adapter_vector scenario %s", c.ID))
				}
				built, rerr = r.Build.Build(ctx, VectorToRequest(vector), buildOpts)
				if rerr != nil {
					return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", rerr))
				}
				requests = []*UpstreamRequest{built}
			}
			built = requests[len(requests)-1]
			pre = requests[:len(requests)-1]
		}
		if err != nil {
			return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
		}
		for _, w := range built.Warnings {
			base.Diagnostics = append(base.Diagnostics, w)
		}
		codec := &CodecCheckResult{
			CaseID: c.ID, Method: built.Method, URL: built.URL, BodyBytes: len(built.Body),
		}
		want, hasGolden := opts.Goldens[c.ID]
		if hasGolden {
			codec.GoldenMatched, codec.HeaderCaseMatched, codec.Diff = goldenDiff(built, want)
		}
		base.Codec = codec
		if codec.Diff != "" {
			return fail(ClassHarnessFailure, "contract_integrity", "codec emitted wrong wire bytes: "+codec.Diff)
		}
		obs = Empty()
		for _, p := range pre {
			RecordUpstreamRequest(obs, p)
		}
		RecordUpstreamRequest(obs, built)
		if bridged != nil {
			FinalizeObservation(obs, bridged, nil, 200)
		}
		AttachVerifiers(obs, c)
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
		if inbound == "anthropic-messages" {
			if upstream != "openai-responses" {
				return fail(ClassHarnessFailure, "execution_error",
					fmt.Sprintf("client_request ingress: upstream protocol %s not implemented", upstream))
			}
			req, err := DecodeMessagesRequest([]byte(c.Fixture.Bytes))
			if err != nil {
				return fail(ClassHarnessFailure, "fixture_decode_failure", fmt.Sprintf("fixture decode: %v", err))
			}
			built, err := r.builderFor(upstream).Build(ctx, req, buildOpts)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
			}
			codec := &CodecCheckResult{
				CaseID: c.ID, Method: built.Method, URL: built.URL, BodyBytes: len(built.Body),
			}
			if want, hasGolden := opts.Goldens[c.ID]; hasGolden {
				codec.GoldenPinned = true
				codec.GoldenMatched, codec.HeaderCaseMatched, codec.Diff = goldenDiff(built, want)
				if codec.Diff != "" {
					return fail(ClassHarnessFailure, "contract_integrity", "codec emitted wrong wire bytes: "+codec.Diff)
				}
			}
			base.Codec = codec
			obs = Empty()
			RecordUpstreamRequest(obs, built)
			AttachVerifiers(obs, c)
			break
		}
		if upstream != "openai-chat" {
			return fail(ClassHarnessFailure, "execution_error",
				fmt.Sprintf("unit 2 (SSE egress): client_request upstream %s not implemented in unit 1", upstream))
		}
		req, ingressWarnings, err := responsesBodyToRequest([]byte(c.Fixture.Bytes))
		if err != nil {
			return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("ingress parse: %v", err))
		}
		base.Diagnostics = append(base.Diagnostics, ingressWarnings...)
		built, err := r.Build.Build(ctx, req, buildOpts)
		if err != nil {
			return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
		}
		for _, w := range built.Warnings {
			base.Diagnostics = append(base.Diagnostics, w)
		}
		codec := &CodecCheckResult{
			CaseID: c.ID, Method: built.Method, URL: built.URL, BodyBytes: len(built.Body),
		}
		if want, hasGolden := opts.Goldens[c.ID]; hasGolden {
			codec.GoldenMatched, codec.HeaderCaseMatched, codec.Diff = goldenDiff(built, want)
			if codec.Diff != "" {
				return fail(ClassHarnessFailure, "contract_integrity", "codec emitted wrong wire bytes: "+codec.Diff)
			}
		}
		base.Codec = codec
		obs = Empty()
		RecordUpstreamRequest(obs, built)
		AttachVerifiers(obs, c)
	case RoleUpstreamResponse:
		if c.ID == "tools-core.protocol.parallel-correlation" {
			built, err := runToolsCoreChatSse(c, r, buildOpts)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("tools-core sse: %v", err))
			}
			obs = built
			break
		}
		if c.ID == "codex-core.protocol.streaming-turn" {
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
			events, err := NormalizeSseBytes([]byte(c.Fixture.Bytes), "openai-chat")
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("sse normalize: %v", err))
			}
			events = BridgeChatSse(events)
			FinalizeObservation(obs, events, nil, 200)
			AttachVerifiers(obs, c)
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
		if upstream != "openai-responses" {
			return fail(ClassHarnessFailure, "execution_error",
				fmt.Sprintf("SSE egress: upstream protocol %s not implemented", upstream))
		}
		if inbound != "openai-responses" && inbound != "anthropic-messages" {
			return fail(ClassHarnessFailure, "execution_error",
				fmt.Sprintf("SSE egress: inbound protocol %s not implemented", inbound))
		}
		events, err := NormalizeSseBytes([]byte(c.Fixture.Bytes), upstream)
		if err != nil {
			return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("sse normalize: %v", err))
		}
		if inbound == "anthropic-messages" {
			events = TranslateResponsesEvents(events)
		}
		obs = Empty()
		if c.InitiatingRequest != nil {
			var req canon.Request
			switch inbound {
			case "anthropic-messages":
				req, err = DecodeMessagesRequest([]byte(c.InitiatingRequest.Bytes))
				if err != nil {
					return fail(ClassHarnessFailure, "fixture_decode_failure", fmt.Sprintf("initiating request decode: %v", err))
				}
			default:
				var vector map[string]any
				if err := json.Unmarshal([]byte(c.InitiatingRequest.Bytes), &vector); err != nil {
					return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("initiating request decode: %v", err))
				}
				req = VectorToRequest(vector)
			}
			built, err := r.builderFor(upstream).Build(ctx, req, buildOpts)
			if err != nil {
				return fail(ClassHarnessFailure, "execution_error", fmt.Sprintf("codec build: %v", err))
			}
			RecordUpstreamRequest(obs, built)
		}
		FinalizeObservation(obs, events, nil, 200)
		AttachVerifiers(obs, c)
	default:
		return fail(ClassHarnessFailure, "execution_error",
			fmt.Sprintf("fixture role %s not implemented", c.Fixture.Role))
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
