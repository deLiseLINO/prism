package conformance

import (
	"path/filepath"
	"testing"
)

func fixturePath() string {
	return filepath.Join("..", "..", "cmd", "prism-wirecheck", "fixtures", "protocol-v1-cases.json")
}

func TestLoadVendoredAuthority(t *testing.T) {
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	if a.SchemaVersion != 1 {
		t.Fatalf("schemaVersion %d", a.SchemaVersion)
	}
	if a.SourceCommit != "3b1d0040000000000000000000000000000000aa" {
		t.Fatalf("sourceCommit %s", a.SourceCommit)
	}
	if a.AssertionDSLVersion != "1.0.0" || a.EvidenceSchemaVersion != "1.0.0" {
		t.Fatalf("dsl %s evidence %s", a.AssertionDSLVersion, a.EvidenceSchemaVersion)
	}
	if len(a.Cases) != 31 {
		t.Fatalf("cases %d want 31", len(a.Cases))
	}
	suites := map[string]int{}
	fixtures := 0
	for _, c := range a.Cases {
		suites[c.Suite]++
		fixtures++
		if c.InitiatingRequest != nil {
			fixtures++
		}
	}
	if len(suites) != 7 {
		t.Fatalf("suites %d want 7", len(suites))
	}
	if fixtures != 42 {
		t.Fatalf("fixtures %d want 42", fixtures)
	}
}

func TestValidateRejectsBadDigest(t *testing.T) {
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	a.Cases[0].Fixture.Digest = "deadbeef"
	if err := Validate(a); err == nil {
		t.Fatal("expected digest mismatch error")
	}
}

func TestValidateRejectsMissingSourceCommit(t *testing.T) {
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	a.SourceCommit = ""
	if err := Validate(a); err == nil {
		t.Fatal("expected missing sourceCommit error")
	}
}

func TestValidateRejectsBadSchemaVersion(t *testing.T) {
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	a.SchemaVersion = 2
	if err := Validate(a); err == nil {
		t.Fatal("expected unsupported schemaVersion error")
	}
}

func TestValidateRejectsUpstreamResponseWithoutInitiatingRequest(t *testing.T) {
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range a.Cases {
		if a.Cases[i].Fixture.Role == RoleUpstreamResponse {
			a.Cases[i].InitiatingRequest = nil
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no upstream_response case in fixture")
	}
	if err := Validate(a); err == nil {
		t.Fatal("expected upstream_response error")
	}
}

func TestValidateRejectsUnknownRuleSet(t *testing.T) {
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	a.ManifestDefaults.FailureRuleSet = "nope"
	if err := Validate(a); err == nil {
		t.Fatal("expected unknown failureRuleSet error")
	}
}

func TestExpandScenarioRequestShape(t *testing.T) {
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	var c Case
	for _, x := range a.Cases {
		if x.ID == "responses-core.protocol.request-shape" {
			c = x
		}
	}
	if c.ID == "" {
		t.Fatal("request-shape case not found")
	}
	s, err := ExpandScenario(a, c)
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion != 1 || s.ID != c.ID || s.Version != "1.0.0" {
		t.Fatalf("scenario header %+v", s)
	}
	if s.Suite.ID != "responses-core" || s.Suite.Version != "1.0.0" || s.Suite.EvidenceLayer != "protocol_conformance" {
		t.Fatalf("suite %+v", s.Suite)
	}
	if s.EvidenceLayer != "protocol_conformance" || s.Capability != "protocol.responses.core" {
		t.Fatalf("layer/capability %s %s", s.EvidenceLayer, s.Capability)
	}
	if s.VerificationRole != "required" {
		t.Fatalf("verificationRole %s", s.VerificationRole)
	}
	if len(s.Fixtures) != 1 {
		t.Fatalf("fixtures %d want 1", len(s.Fixtures))
	}
	f := s.Fixtures[0]
	if f.ID != "rsp-request-shape" || f.Role != RoleAdapterVector || f.ByteLength != len(c.Fixture.Bytes) {
		t.Fatalf("fixture ref %+v", f)
	}
	if f.SyntheticMarker != SyntheticMarker || f.Provenance.Kind != "lab_authored" || f.Provenance.Authority != AuthorityFile || f.Provenance.SourceCommit != a.SourceCommit {
		t.Fatalf("ref provenance %+v", f.Provenance)
	}
	if len(s.FailureRules) != 5 {
		t.Fatalf("failureRules %d want 5", len(s.FailureRules))
	}
	if s.FailureRules[0].ID != RuleContractIntegrity || s.FailureRules[4].ID != RuleFallback {
		t.Fatalf("rule order %s..%s", s.FailureRules[0].ID, s.FailureRules[4].ID)
	}
	if s.ExpectedFailure != nil {
		t.Fatal("expectedFailure should be nil")
	}
	if len(s.Assertions) != 3 {
		t.Fatalf("assertions %d want 3", len(s.Assertions))
	}
}

func TestExpandScenarioInitiatingRequestFirst(t *testing.T) {
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	var c Case
	for _, x := range a.Cases {
		if x.Fixture.Role == RoleUpstreamResponse && x.InitiatingRequest != nil {
			c = x
			break
		}
	}
	if c.ID == "" {
		t.Fatal("no upstream_response case with initiatingRequest")
	}
	s, err := ExpandScenario(a, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Fixtures) != 2 {
		t.Fatalf("fixtures %d want 2", len(s.Fixtures))
	}
	if s.Fixtures[0].ID != c.InitiatingRequest.ID || s.Fixtures[1].ID != c.Fixture.ID {
		t.Fatalf("initiatingRequest must come first: %s then %s", s.Fixtures[0].ID, s.Fixtures[1].ID)
	}
}

func TestExpandScenarioSynthesis(t *testing.T) {
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	var c Case
	for _, x := range a.Cases {
		if x.ID == "vision-core.protocol.modality-gate" {
			c = x
		}
	}
	if c.ID == "" || c.ExpectedFailure == nil {
		t.Fatal("modality-gate case not found or lacks expectedFailure")
	}
	s, err := ExpandScenario(a, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.FailureRules) != 6 {
		t.Fatalf("failureRules %d want 6 (5 + synthesized)", len(s.FailureRules))
	}
	if s.FailureRules[0].ID != RuleContractIntegrity {
		t.Fatalf("first rule %s want contract-integrity", s.FailureRules[0].ID)
	}
	control := s.FailureRules[3]
	if control.ID != RuleExpectedFailureExactMatch {
		t.Fatalf("synthesized rule at index 3, got %s", control.ID)
	}
	if control.Classification != Classification(c.ExpectedFailure.ExpectedClass) {
		t.Fatalf("classification %s", control.Classification)
	}
	if control.SecondaryCode != c.ExpectedFailure.ExpectedCode {
		t.Fatalf("secondaryCode %s", control.SecondaryCode)
	}
	if control.VerdictEffect != "none" {
		t.Fatalf("verdictEffect %s", control.VerdictEffect)
	}
	if !control.Expected || control.Retry != "never" {
		t.Fatalf("control rule %+v", control)
	}
	required := s.FailureRules[4]
	if required.ID != RuleRequiredAssertion {
		t.Fatalf("required-assertion at index 4, got %s", required.ID)
	}
	if s.FailureRules[5].ID != RuleFallback {
		t.Fatalf("fallback must stay last, got %s", s.FailureRules[5].ID)
	}
}
