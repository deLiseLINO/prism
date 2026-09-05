package conformance

import "fmt"

type RuleID string

const (
	RuleContractIntegrity         RuleID = "contract-integrity"
	RuleTimeLimit                 RuleID = "time-limit"
	RuleResourceLimit             RuleID = "resource-limit"
	RuleRequiredAssertion         RuleID = "required-assertion"
	RuleFallback                  RuleID = "fallback"
	RuleExpectedFailureExactMatch RuleID = "expected-failure-exact-match"
)

type Symptom string

const (
	SymptomFixtureDigestMismatch     Symptom = "fixture_digest_mismatch"
	SymptomManifestDigestMismatch    Symptom = "manifest_digest_mismatch"
	SymptomFixtureDecodeFailure      Symptom = "fixture_decode_failure"
	SymptomHarnessFailure            Symptom = "harness_failure"
	SymptomSanitizerFailure          Symptom = "sanitizer_failure"
	SymptomConnectTimeout            Symptom = "connect_timeout"
	SymptomFirstByteTimeout          Symptom = "first_byte_timeout"
	SymptomInactivityTimeout         Symptom = "inactivity_timeout"
	SymptomTotalTimeout              Symptom = "total_timeout"
	SymptomRequestLimit              Symptom = "request_limit"
	SymptomInputByteLimit            Symptom = "input_byte_limit"
	SymptomOutputByteLimit           Symptom = "output_byte_limit"
	SymptomOutputTokenLimit          Symptom = "output_token_limit"
	SymptomToolCallLimit             Symptom = "tool_call_limit"
	SymptomArtifactByteLimit         Symptom = "artifact_byte_limit"
	SymptomRequiredAssertionFailed   Symptom = "required_assertion_failed"
	SymptomNoPriorRule               Symptom = "no_prior_rule"
	SymptomExpectedFailureExactMatch Symptom = "expected_failure_exact_match"
)

type Classification string

const (
	ClassHarnessFailure        Classification = "harness_failure"
	ClassTimeout               Classification = "timeout"
	ClassInactivityTimeout     Classification = "inactivity_timeout"
	ClassBudgetExhausted       Classification = "budget_exhausted"
	ClassSandboxViolation      Classification = "sandbox_violation"
	ClassMalformedProducer     Classification = "malformed_producer_outcome"
	ClassLayerSubjectMismatch  Classification = "layer_subject_mismatch"
	ClassProtocolFailure       Classification = "protocol_failure"
	ClassCapabilityFailure     Classification = "capability_failure"
	ClassBehavioralFailure     Classification = "behavioral_failure"
	ClassAuthenticationBlocked Classification = "authentication_blocked"
	ClassQuotaBlocked          Classification = "quota_blocked"
	ClassRegionBlocked         Classification = "region_blocked"
	ClassNetworkFailure        Classification = "network_failure"
	ClassProviderTransient     Classification = "provider_transient"
	ClassInconclusive          Classification = "inconclusive"
)

type Rule struct {
	ID             RuleID         `json:"id"`
	Match          []Symptom      `json:"match"`
	Classification Classification `json:"classification"`
	SecondaryCode  string         `json:"secondaryCode"`
	VerdictEffect  string         `json:"verdictEffect"`
	Retry          string         `json:"retry"`
	Expected       bool           `json:"expected"`
}

type RuleTemplate struct {
	ID       RuleID    `json:"id"`
	Match    []Symptom `json:"match"`
	Retry    string    `json:"retry"`
	Expected bool      `json:"expected"`
}

type RuleSet struct {
	ID    string
	Rules []Rule
}

type FailReason string

const (
	ReasonSelectorMissing               FailReason = "selector_missing"
	ReasonSelectorTypeMismatch          FailReason = "selector_type_mismatch"
	ReasonSelectorPresent               FailReason = "selector_present"
	ReasonValueMismatch                 FailReason = "value_mismatch"
	ReasonEventSequenceMismatch         FailReason = "event_sequence_mismatch"
	ReasonEventCountMismatch            FailReason = "event_count_mismatch"
	ReasonIDGrammarMismatch             FailReason = "id_grammar_mismatch"
	ReasonIDNotStable                   FailReason = "id_not_stable"
	ReasonIDCorrelationMismatch         FailReason = "id_correlation_mismatch"
	ReasonToolResultCorrelationMismatch FailReason = "tool_result_correlation_mismatch"
	ReasonInvalidExpected               FailReason = "invalid_expected"
	ReasonEvaluationError               FailReason = "evaluation_error"
	ReasonUnknownOperator               FailReason = "unknown_operator"
)

func (rs RuleSet) Classify(s Symptom) *Rule {
	for i := range rs.Rules {
		for _, m := range rs.Rules[i].Match {
			if m == s {
				return &rs.Rules[i]
			}
		}
	}
	return nil
}

func ExpandFailureRules(a *Authority, c Case) ([]Rule, error) {
	setName := a.ManifestDefaults.FailureRuleSet
	ruleSet, ok := a.FailureRuleSets[setName]
	if !ok {
		return nil, fmt.Errorf("harness_failure: contract_integrity unknown failureRuleSet %s", setName)
	}
	base := make([]Rule, len(ruleSet))
	copy(base, ruleSet)
	if c.ExpectedFailure == nil {
		return base, nil
	}
	t := a.ExpectedFailureTemplate
	ef := c.ExpectedFailure
	verdict := "none"
	if ef.OnMatch == "unsupported" {
		verdict = "unsupported"
	}
	control := Rule{
		ID:             t.ID,
		Match:          append([]Symptom(nil), t.Match...),
		Classification: Classification(ef.ExpectedClass),
		SecondaryCode:  ef.ExpectedCode,
		VerdictEffect:  verdict,
		Retry:          t.Retry,
		Expected:       t.Expected,
	}
	idx := -1
	for i := range base {
		if base[i].ID == RuleRequiredAssertion {
			idx = i
			break
		}
	}
	if idx >= 0 {
		base = append(base[:idx], append([]Rule{control}, base[idx:]...)...)
	} else {
		base = append(base, control)
	}
	return base, nil
}
