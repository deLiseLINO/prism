package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const SyntheticMarker = "prism-lab-synthetic-v1"

const AuthorityFile = "022_protocol_v1_cases.json"

var mcpActionTokens = []string{
	"mcp_namespace_round_trip_v1",
	"mcp_schema_bounds_v1",
	"mcp_call_result_v1",
	"mcp_resource_round_trip_v1",
}

type FixtureRole string

const (
	RoleClientRequest    FixtureRole = "client_request"
	RoleUpstreamResponse FixtureRole = "upstream_response"
	RoleAdapterVector    FixtureRole = "adapter_vector"
	RoleSyntheticTool    FixtureRole = "synthetic_tool"
)

type Fixture struct {
	ID        string      `json:"id"`
	Role      FixtureRole `json:"role"`
	MediaType string      `json:"mediaType"`
	Bytes     string      `json:"bytesUtf8"`
	Digest    string      `json:"digest"`
}

type Assertion struct {
	ID       string          `json:"id"`
	Operator string          `json:"operator"`
	Selector string          `json:"selector"`
	Expected json.RawMessage `json:"expected"`
	Required bool            `json:"required"`
}

type ExpectedFailure struct {
	ControlKind   string   `json:"controlKind"`
	ExpectedClass string   `json:"expectedClass"`
	ExpectedCode  string   `json:"expectedCode"`
	AssertionIDs  []string `json:"assertionIds"`
	OnMatch       string   `json:"onMatch"`
	OnMismatch    string   `json:"onMismatch"`
}

type Requirements struct {
	InboundProtocols        []string `json:"inboundProtocols"`
	UpstreamProtocols       []string `json:"upstreamProtocols"`
	Surfaces                []string `json:"surfaces"`
	RequiredClaims          []string `json:"requiredClaims"`
	RequiredHarnessFeatures []string `json:"requiredHarnessFeatures"`
	Platforms               []string `json:"platforms"`
	RoutePreconditions      []string `json:"routePreconditions"`
}

type Case struct {
	ID                string           `json:"id"`
	Suite             string           `json:"suite"`
	Capability        string           `json:"capability"`
	VerificationRole  string           `json:"verificationRole,omitempty"`
	Requirements      Requirements     `json:"requirements"`
	Fixture           Fixture          `json:"fixture"`
	InitiatingRequest *Fixture         `json:"initiatingRequest,omitempty"`
	Assertions        []Assertion      `json:"assertions"`
	ExpectedFailure   *ExpectedFailure `json:"expectedFailure,omitempty"`
}

type Authority struct {
	SchemaVersion           int               `json:"schemaVersion"`
	SourceCommit            string            `json:"sourceCommit"`
	AssertionDSLVersion     string            `json:"assertionDslVersion"`
	EvidenceSchemaVersion   string            `json:"evidenceSchemaVersion"`
	FailureRuleSets         map[string][]Rule `json:"failureRuleSets"`
	ExpectedFailureTemplate RuleTemplate      `json:"expectedFailureRuleTemplate"`
	ManifestDefaults        ManifestDefaults  `json:"manifestDefaults"`
	Cases                   []Case            `json:"cases"`
}

type ManifestDefaults struct {
	Version          string         `json:"version"`
	SuiteVersion     string         `json:"suiteVersion"`
	EvidenceLayer    string         `json:"evidenceLayer"`
	VerificationRole string         `json:"verificationRole"`
	ExecutionMode    string         `json:"executionMode"`
	Freshness        Freshness      `json:"freshness"`
	ExecutionLimits  map[string]any `json:"executionLimits"`
	ArtifactPolicy   map[string]any `json:"artifactPolicy"`
	FailureRuleSet   string         `json:"failureRuleSet"`
}

type Freshness struct {
	MaxAgeMS *float64 `json:"maxAgeMs"`
}

type FixtureRef struct {
	ID              string      `json:"id"`
	Role            FixtureRole `json:"role"`
	MediaType       string      `json:"mediaType"`
	Digest          string      `json:"digest"`
	ByteLength      int         `json:"byteLength"`
	SyntheticMarker string      `json:"syntheticMarker"`
	Provenance      Provenance  `json:"provenance"`
}

type Provenance struct {
	Kind         string `json:"kind"`
	Authority    string `json:"authority"`
	SourceCommit string `json:"sourceCommit"`
}

type Scenario struct {
	SchemaVersion    int              `json:"schemaVersion"`
	ID               string           `json:"id"`
	Version          string           `json:"version"`
	Suite            ScenarioSuite    `json:"suite"`
	EvidenceLayer    string           `json:"evidenceLayer"`
	Capability       string           `json:"capability"`
	VerificationRole string           `json:"verificationRole"`
	Requirements     Requirements     `json:"requirements"`
	Fixtures         []FixtureRef     `json:"fixtures"`
	ExecutionLimits  map[string]any   `json:"executionLimits"`
	Assertions       []Assertion      `json:"assertions"`
	ExpectedFailure  *ExpectedFailure `json:"expectedFailure,omitempty"`
	FailureRules     []Rule           `json:"failureRules"`
	ArtifactPolicy   map[string]any   `json:"artifactPolicy"`
	Freshness        Freshness        `json:"freshness"`
}

type ScenarioSuite struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	EvidenceLayer string `json:"evidenceLayer"`
}

type SuiteManifest struct {
	SchemaVersion         int                `json:"schemaVersion"`
	ID                    string             `json:"id"`
	Version               string             `json:"version"`
	EvidenceLayer         string             `json:"evidenceLayer"`
	Capability            string             `json:"capability"`
	AssertionDSLVersion   string             `json:"assertionDslVersion"`
	EvidenceSchemaVersion string             `json:"evidenceSchemaVersion"`
	Freshness             Freshness          `json:"freshness"`
	ContradictionRule     string             `json:"contradictionRule"`
	Scenarios             []SuiteScenarioRef `json:"scenarios"`
	VerificationRule      string             `json:"verificationRule"`
}

type SuiteScenarioRef struct {
	ID             string `json:"id"`
	Version        string `json:"version"`
	Role           string `json:"role"`
	ManifestDigest string `json:"manifestDigest"`
}

func Load(path string) (*Authority, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a Authority
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := Validate(&a); err != nil {
		return nil, err
	}
	return &a, nil
}

func Validate(a *Authority) error {
	if a.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schemaVersion %d", a.SchemaVersion)
	}
	if a.SourceCommit == "" {
		return fmt.Errorf("missing sourceCommit")
	}
	if len(a.Cases) == 0 {
		return fmt.Errorf("no cases")
	}
	for _, c := range a.Cases {
		var errs []string
		errs = append(errs, validateFixtureDigests(c)...)
		errs = append(errs, validateMcpHarnessFeatures(c)...)
		expanded, err := ExpandScenario(a, c)
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			for i, ref := range expanded.Fixtures {
				src := c.Fixture.Bytes
				if i == 0 && c.InitiatingRequest != nil {
					src = c.InitiatingRequest.Bytes
				}
				errs = append(errs, validateExpandedFixtureRef(ref, a, src)...)
			}
		}
		if c.Fixture.Role == RoleUpstreamResponse && c.InitiatingRequest == nil {
			errs = append(errs, "upstream_response without initiatingRequest")
		}
		if len(errs) > 0 {
			return fmt.Errorf("%s: %s", c.ID, strings.Join(errs, "; "))
		}
	}
	return nil
}

func validateFixtureDigests(c Case) []string {
	var errs []string
	check := func(f Fixture, label string) {
		got := FixtureDigest([]byte(f.Bytes))
		if got != f.Digest {
			errs = append(errs, fmt.Sprintf("%s digest mismatch: expected %s, got %s", label, f.Digest, got))
		}
	}
	check(c.Fixture, c.Fixture.ID)
	if c.InitiatingRequest != nil {
		check(*c.InitiatingRequest, c.InitiatingRequest.ID)
	}
	return errs
}

func validateMcpHarnessFeatures(c Case) []string {
	if c.Suite != "mcp-core" {
		return nil
	}
	count := 0
	for _, f := range c.Requirements.RequiredHarnessFeatures {
		for _, token := range mcpActionTokens {
			if f == token {
				count++
			}
		}
	}
	if count != 1 {
		return []string{fmt.Sprintf("%s: invalid_manifest MCP action token count %d", c.ID, count)}
	}
	if c.Fixture.Role != RoleSyntheticTool {
		return []string{fmt.Sprintf("%s: MCP cases require synthetic_tool fixture role", c.ID)}
	}
	return nil
}

func validateExpandedFixtureRef(ref FixtureRef, a *Authority, bytes string) []string {
	var errs []string
	if ref.SyntheticMarker != SyntheticMarker {
		errs = append(errs, fmt.Sprintf("invalid syntheticMarker: %s", ref.SyntheticMarker))
	}
	p := ref.Provenance
	if p.Kind != "lab_authored" {
		errs = append(errs, "invalid provenance kind")
	} else if p.Authority != AuthorityFile {
		errs = append(errs, fmt.Sprintf("invalid provenance authority: %s", p.Authority))
	} else if p.SourceCommit != a.SourceCommit {
		errs = append(errs, fmt.Sprintf("invalid provenance sourceCommit: %s", p.SourceCommit))
	}
	if ref.Digest != FixtureDigest([]byte(bytes)) {
		errs = append(errs, "fixture digest mismatch in expanded ref")
	}
	if ref.ByteLength != len([]byte(bytes)) {
		errs = append(errs, "fixture byteLength mismatch in expanded ref")
	}
	return errs
}

func ExpandScenario(a *Authority, c Case) (Scenario, error) {
	rules, err := ExpandFailureRules(a, c)
	if err != nil {
		return Scenario{}, err
	}
	d := a.ManifestDefaults
	fixtures := []FixtureRef{fixtureRef(c.Fixture, a)}
	if c.InitiatingRequest != nil {
		fixtures = []FixtureRef{fixtureRef(*c.InitiatingRequest, a), fixtureRef(c.Fixture, a)}
	}
	role := c.VerificationRole
	if role == "" {
		role = d.VerificationRole
	}
	return Scenario{
		SchemaVersion:    a.SchemaVersion,
		ID:               c.ID,
		Version:          d.Version,
		Suite:            ScenarioSuite{ID: c.Suite, Version: d.SuiteVersion, EvidenceLayer: d.EvidenceLayer},
		EvidenceLayer:    d.EvidenceLayer,
		Capability:       c.Capability,
		VerificationRole: role,
		Requirements:     c.Requirements,
		Fixtures:         fixtures,
		ExecutionLimits:  d.ExecutionLimits,
		Assertions:       c.Assertions,
		ExpectedFailure:  c.ExpectedFailure,
		FailureRules:     rules,
		ArtifactPolicy:   d.ArtifactPolicy,
		Freshness:        d.Freshness,
	}, nil
}

func fixtureRef(f Fixture, a *Authority) FixtureRef {
	return FixtureRef{
		ID:              f.ID,
		Role:            f.Role,
		MediaType:       f.MediaType,
		Digest:          f.Digest,
		ByteLength:      len([]byte(f.Bytes)),
		SyntheticMarker: SyntheticMarker,
		Provenance:      Provenance{Kind: "lab_authored", Authority: AuthorityFile, SourceCommit: a.SourceCommit},
	}
}
