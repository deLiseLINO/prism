package conformance

import "testing"

func testRuleSet() RuleSet {
	return RuleSet{
		ID: "protocol-v1-default",
		Rules: []Rule{
			{
				ID: RuleContractIntegrity, Match: []Symptom{SymptomFixtureDigestMismatch, SymptomManifestDigestMismatch, SymptomFixtureDecodeFailure, SymptomHarnessFailure, SymptomSanitizerFailure},
				Classification: ClassHarnessFailure, SecondaryCode: "contract_integrity", VerdictEffect: "none", Retry: "never",
			},
			{
				ID: RuleTimeLimit, Match: []Symptom{SymptomConnectTimeout, SymptomFirstByteTimeout, SymptomInactivityTimeout, SymptomTotalTimeout},
				Classification: ClassTimeout, SecondaryCode: "scenario_time_limit", VerdictEffect: "none", Retry: "never",
			},
			{
				ID: RuleResourceLimit, Match: []Symptom{SymptomRequestLimit, SymptomInputByteLimit, SymptomOutputByteLimit, SymptomOutputTokenLimit, SymptomToolCallLimit, SymptomArtifactByteLimit},
				Classification: ClassBudgetExhausted, SecondaryCode: "scenario_resource_limit", VerdictEffect: "none", Retry: "never",
			},
			{
				ID: RuleRequiredAssertion, Match: []Symptom{SymptomRequiredAssertionFailed},
				Classification: ClassProtocolFailure, SecondaryCode: "deterministic_assertion", VerdictEffect: "degraded", Retry: "never",
			},
			{
				ID: RuleFallback, Match: []Symptom{SymptomNoPriorRule},
				Classification: ClassInconclusive, SecondaryCode: "unclassified", VerdictEffect: "none", Retry: "never",
			},
		},
	}
}

func TestClassifyFirstMatch(t *testing.T) {
	rs := testRuleSet()
	cases := []struct {
		symptom Symptom
		want    RuleID
	}{
		{SymptomFixtureDigestMismatch, RuleContractIntegrity},
		{SymptomHarnessFailure, RuleContractIntegrity},
		{SymptomTotalTimeout, RuleTimeLimit},
		{SymptomInactivityTimeout, RuleTimeLimit},
		{SymptomOutputTokenLimit, RuleResourceLimit},
		{SymptomArtifactByteLimit, RuleResourceLimit},
		{SymptomRequiredAssertionFailed, RuleRequiredAssertion},
		{SymptomNoPriorRule, RuleFallback},
	}
	for _, tc := range cases {
		got := rs.Classify(tc.symptom)
		if got == nil || got.ID != tc.want {
			t.Fatalf("Classify(%s) = %v want %s", tc.symptom, got, tc.want)
		}
	}
	if got := rs.Classify(Symptom("not_a_symptom")); got != nil {
		t.Fatalf("unknown symptom matched rule %s", got.ID)
	}
}

func TestClassifyFallbackSemantics(t *testing.T) {
	rs := testRuleSet()
	got := rs.Classify(SymptomNoPriorRule)
	if got.Classification != ClassInconclusive || got.SecondaryCode != "unclassified" {
		t.Fatalf("fallback %+v", got)
	}
}

func TestExpandFailureRulesSynthesisOrder(t *testing.T) {
	a := &Authority{
		FailureRuleSets: map[string][]Rule{"protocol-v1-default": testRuleSet().Rules},
		ManifestDefaults: ManifestDefaults{FailureRuleSet: "protocol-v1-default"},
		ExpectedFailureTemplate: RuleTemplate{
			ID: RuleExpectedFailureExactMatch, Match: []Symptom{SymptomExpectedFailureExactMatch}, Retry: "never", Expected: true,
		},
	}
	none := Case{
		ID: "control-none",
		ExpectedFailure: &ExpectedFailure{
			ExpectedClass: "capability_failure", ExpectedCode: "modality_gate", OnMatch: "pass",
		},
	}
	rules, err := ExpandFailureRules(a, none)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 6 {
		t.Fatalf("rules %d want 6", len(rules))
	}
	if rules[0].ID != RuleContractIntegrity {
		t.Fatalf("first rule %s", rules[0].ID)
	}
	control := rules[3]
	if control.ID != RuleExpectedFailureExactMatch {
		t.Fatalf("synthesis must insert before required-assertion, got index 3 = %s", control.ID)
	}
	if control.Classification != ClassCapabilityFailure || control.SecondaryCode != "modality_gate" || control.VerdictEffect != "none" {
		t.Fatalf("synthesized rule %+v", control)
	}
	if rules[4].ID != RuleRequiredAssertion || rules[5].ID != RuleFallback {
		t.Fatalf("tail order broken: %s, %s", rules[4].ID, rules[5].ID)
	}

	unsupported := Case{
		ID: "control-unsupported",
		ExpectedFailure: &ExpectedFailure{
			ExpectedClass: "capability_failure", ExpectedCode: "x", OnMatch: "unsupported",
		},
	}
	rules, err = ExpandFailureRules(a, unsupported)
	if err != nil {
		t.Fatal(err)
	}
	if rules[3].VerdictEffect != "unsupported" {
		t.Fatalf("verdictEffect %s want unsupported", rules[3].VerdictEffect)
	}
}

func TestExpandFailureRulesNoSynthesis(t *testing.T) {
	a := &Authority{
		FailureRuleSets:  map[string][]Rule{"protocol-v1-default": testRuleSet().Rules},
		ManifestDefaults: ManifestDefaults{FailureRuleSet: "protocol-v1-default"},
	}
	rules, err := ExpandFailureRules(a, Case{ID: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 5 {
		t.Fatalf("rules %d want 5", len(rules))
	}
	if rules[0].ID != RuleContractIntegrity {
		t.Fatalf("first rule %s", rules[0].ID)
	}
}

func TestExpandFailureRulesAppendsWhenNoRequiredAssertion(t *testing.T) {
	a := &Authority{
		FailureRuleSets: map[string][]Rule{"rs": {
			{ID: RuleFallback, Match: []Symptom{SymptomNoPriorRule}, Classification: ClassInconclusive},
		}},
		ManifestDefaults: ManifestDefaults{FailureRuleSet: "rs"},
		ExpectedFailureTemplate: RuleTemplate{
			ID: RuleExpectedFailureExactMatch, Match: []Symptom{SymptomExpectedFailureExactMatch}, Retry: "never", Expected: true,
		},
	}
	rules, err := ExpandFailureRules(a, Case{
		ID: "c", ExpectedFailure: &ExpectedFailure{ExpectedClass: "capability_failure", ExpectedCode: "x", OnMatch: "pass"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[1].ID != RuleExpectedFailureExactMatch {
		t.Fatalf("control rule must append when required-assertion absent: %+v", rules)
	}
}

func TestExpandFailureRulesUnknownSet(t *testing.T) {
	a := &Authority{ManifestDefaults: ManifestDefaults{FailureRuleSet: "missing"}}
	if _, err := ExpandFailureRules(a, Case{ID: "c"}); err == nil {
		t.Fatal("expected unknown failureRuleSet error")
	}
}
