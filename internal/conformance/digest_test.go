package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFixtureDigest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "prism-wirecheck", "fixtures", "protocol-v1-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var a Authority
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	for _, c := range a.Cases {
		got := FixtureDigest([]byte(c.Fixture.Bytes))
		if got != c.Fixture.Digest {
			t.Fatalf("%s fixture digest: got %s want %s", c.ID, got, c.Fixture.Digest)
		}
		if c.InitiatingRequest != nil {
			got := FixtureDigest([]byte(c.InitiatingRequest.Bytes))
			if got != c.InitiatingRequest.Digest {
				t.Fatalf("%s initiatingRequest digest: got %s want %s", c.ID, got, c.InitiatingRequest.Digest)
			}
		}
	}
}

func TestRequestShapeFixtureDigest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "prism-wirecheck", "fixtures", "protocol-v1-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var a Authority
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	var bytes string
	for _, c := range a.Cases {
		if c.ID == "responses-core.protocol.request-shape" {
			bytes = c.Fixture.Bytes
		}
	}
	if bytes == "" {
		t.Fatal("request-shape case not found")
	}
	want := "26da2159122e8ebb263222dc1251abdbfa5b9fbb36ebad1516e94253229703a0"
	if got := FixtureDigest([]byte(bytes)); got != want {
		t.Fatalf("request-shape digest: got %s want %s", got, want)
	}
}

func TestScenarioManifestDigest(t *testing.T) {
	s := testScenario()
	want := "1d3d86666015517ca26a4b020e4135cc9f8d4ee527a776fecc879ad0bfba1d72"
	if got := ScenarioManifestDigest(s); got != want {
		t.Fatalf("scenario digest: got %s want %s", got, want)
	}
}

func TestSuiteManifestDigest(t *testing.T) {
	s := testSuiteManifest()
	want := "9be187dfee9503fba6a69df52eb3f23191e0e70fbce40a352ce98d4774daad3f"
	if got := SuiteManifestDigest(s); got != want {
		t.Fatalf("suite digest: got %s want %s", got, want)
	}
}

func testScenario() Scenario {
	return Scenario{
		SchemaVersion:    1,
		ID:               "test.scenario",
		Version:          "1.0.0",
		Suite:            ScenarioSuite{ID: "test-suite", Version: "1.0.0", EvidenceLayer: "protocol_conformance"},
		EvidenceLayer:    "protocol_conformance",
		Capability:       "test.capability",
		VerificationRole: "required",
		Requirements: Requirements{
			InboundProtocols:        []string{"openai-responses"},
			UpstreamProtocols:       []string{"openai-chat"},
			Surfaces:                []string{"responses-http"},
			RequiredClaims:          []string{},
			RequiredHarnessFeatures: []string{"adapter_vector"},
			Platforms:               []string{},
			RoutePreconditions:      []string{},
		},
		Fixtures: []FixtureRef{{
			ID:              "fx-1",
			Role:            RoleAdapterVector,
			MediaType:       "application/vnd.prism.adapter-vector+json",
			Digest:          "d1",
			ByteLength:      3,
			SyntheticMarker: SyntheticMarker,
			Provenance:      Provenance{Kind: "lab_authored", Authority: AuthorityFile, SourceCommit: "abc123"},
		}},
		ExecutionLimits: map[string]any{"totalTimeoutMs": 10000, "maxRequests": 4},
		Assertions: []Assertion{{
			ID: "a1", Operator: "json_path_equals",
			Selector: "/upstream/requests/0/json/model",
			Expected: []byte(`"fixture-model"`), Required: true,
		}},
		FailureRules:   []Rule{},
		ArtifactPolicy: map[string]any{"allowed": []any{"assertion_report"}},
		Freshness:      Freshness{},
	}
}

func testSuiteManifest() SuiteManifest {
	return SuiteManifest{
		SchemaVersion:         1,
		ID:                    "responses-core",
		Version:               "1.0.0",
		EvidenceLayer:         "protocol_conformance",
		Capability:            "protocol.responses.core",
		AssertionDSLVersion:   "1.0.0",
		EvidenceSchemaVersion: "1.0.0",
		Freshness:             Freshness{},
		ContradictionRule:     "newest-required-observation-v1",
		Scenarios: []SuiteScenarioRef{{
			ID: "responses-core.protocol.request-shape", Version: "1.0.0",
			Role: "required", ManifestDigest: "d1",
		}},
		VerificationRule: "all-applicable-required-pass-v1",
	}
}
