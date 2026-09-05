package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"prism/internal/conformance"
)

type selfcheckFile struct {
	Pointer   []pointerVector  `json:"pointer"`
	JCS       []jcsVector      `json:"jcs"`
	Digest    []digestVector   `json:"digest"`
	Operators []operatorVector `json:"operators"`
	Rules     []ruleVector     `json:"rules"`
}

type pointerVector struct {
	Name     string `json:"name"`
	Doc      any    `json:"doc"`
	Selector string `json:"selector"`
	Present  bool   `json:"present"`
	Expected any    `json:"expected"`
}

type jcsVector struct {
	Name      string `json:"name"`
	Value     any    `json:"value"`
	Canonical string `json:"canonical"`
}

type digestVector struct {
	Name     string          `json:"name"`
	Domain   string          `json:"domain"`
	Payload  json.RawMessage `json:"payload"`
	Expected string          `json:"expected"`
}

type operatorVector struct {
	Name        string                `json:"name"`
	Assertion   conformance.Assertion `json:"assertion"`
	Observation json.RawMessage       `json:"observation"`
	Pass        bool                  `json:"pass"`
	Reason      string                `json:"reason"`
}

type ruleVector struct {
	Name        string       `json:"name"`
	Symptom     string       `json:"symptom"`
	Synthesize  *syntheticEF `json:"synthesize"`
	ExpectRule  string       `json:"expectRule"`
	ExpectClass string       `json:"expectClass"`
	ExpectCode  string       `json:"expectCode"`
}

type syntheticEF struct {
	ExpectedClass string `json:"expectedClass"`
	ExpectedCode  string `json:"expectedCode"`
	OnMatch       string `json:"onMatch"`
}

type sectionReport struct {
	ok, total int
	firstErr  string
}

func (s *sectionReport) add(ok bool, detail string) {
	s.total++
	if ok {
		s.ok++
	} else if s.firstErr == "" {
		s.firstErr = detail
	}
}

func (s sectionReport) line(label, verb, note string) string {
	if s.ok == s.total {
		return fmt.Sprintf("  %-11s %d/%d %s (%s)", label, s.ok, s.total, verb, note)
	}
	return fmt.Sprintf("  %-11s %d/%d %s FAILED (first: %s)", label, s.ok, s.total, verb, s.firstErr)
}

func runSelfCheck(a *conformance.Authority) (sectionReport, sectionReport, sectionReport, sectionReport, sectionReport, error) {
	raw, err := os.ReadFile("cmd/prism-wirecheck/testdata/dsl-selfcheck.json")
	if err != nil {
		return sectionReport{}, sectionReport{}, sectionReport{}, sectionReport{}, sectionReport{}, err
	}
	var sc selfcheckFile
	if err := json.Unmarshal(raw, &sc); err != nil {
		return sectionReport{}, sectionReport{}, sectionReport{}, sectionReport{}, sectionReport{}, err
	}

	pointer := sectionReport{}
	for _, v := range sc.Pointer {
		got, ok := conformance.Resolve(v.Doc, v.Selector)
		if ok != v.Present {
			pointer.add(false, fmt.Sprintf("%s: present=%v want %v", v.Name, ok, v.Present))
			continue
		}
		if ok && v.Expected != nil && !conformance.Equal(got, v.Expected) {
			pointer.add(false, fmt.Sprintf("%s: value mismatch", v.Name))
			continue
		}
		pointer.add(true, "")
	}

	jcs := sectionReport{}
	for _, v := range sc.JCS {
		out, err := conformance.JCS(v.Value)
		if err != nil {
			jcs.add(false, fmt.Sprintf("%s: %v", v.Name, err))
			continue
		}
		jcs.add(string(out) == v.Canonical, fmt.Sprintf("%s: got %s", v.Name, out))
	}
	for _, v := range []any{math.NaN(), math.Inf(1), math.Inf(-1), "invalid\xffutf8"} {
		_, err := conformance.JCS(v)
		jcs.add(err != nil, fmt.Sprintf("reject %v: err=%v", v, err))
	}

	digest := sectionReport{}
	for _, v := range sc.Digest {
		got := ""
		var err error
		switch v.Domain {
		case "fixture":
			var payload string
			if err := json.Unmarshal(v.Payload, &payload); err == nil {
				got = conformance.FixtureDigest([]byte(payload))
			} else {
				err = fmt.Errorf("fixture payload must be a string")
			}
		case "scenario":
			var s conformance.Scenario
			if uerr := json.Unmarshal(v.Payload, &s); uerr == nil {
				got = conformance.ScenarioManifestDigest(s)
			} else {
				err = uerr
			}
		case "suite":
			var s conformance.SuiteManifest
			if uerr := json.Unmarshal(v.Payload, &s); uerr == nil {
				got = conformance.SuiteManifestDigest(s)
			} else {
				err = uerr
			}
		default:
			err = fmt.Errorf("unknown digest domain %s", v.Domain)
		}
		if err != nil {
			digest.add(false, fmt.Sprintf("%s: %v", v.Name, err))
			continue
		}
		digest.add(got == v.Expected, fmt.Sprintf("%s: got %s", v.Name, got))
	}

	operators := sectionReport{}
	for _, v := range sc.Operators {
		obs, ok := decodeMap(v.Observation)
		if !ok {
			operators.add(false, fmt.Sprintf("%s: observation must be an object", v.Name))
			continue
		}
		ev := conformance.Evaluator{Obs: obs}
		res := ev.Evaluate([]conformance.Assertion{v.Assertion})[0]
		ok = res.Passed == v.Pass && string(res.Reason) == v.Reason
		detail := fmt.Sprintf("%s: passed=%v reason=%s", v.Name, res.Passed, res.Reason)
		if !ok {
			detail = fmt.Sprintf("%s: passed=%v reason=%s want passed=%v reason=%s", v.Name, res.Passed, res.Reason, v.Pass, v.Reason)
		}
		operators.add(ok, detail)
	}

	rules := sectionReport{}
	for _, v := range sc.Rules {
		base, ok := a.FailureRuleSets[a.ManifestDefaults.FailureRuleSet]
		if !ok {
			rules.add(false, fmt.Sprintf("%s: unknown rule set", v.Name))
			continue
		}
		ruleList := base
		if v.Synthesize != nil {
			ef := &conformance.ExpectedFailure{
				ExpectedClass: v.Synthesize.ExpectedClass,
				ExpectedCode:  v.Synthesize.ExpectedCode,
				OnMatch:       v.Synthesize.OnMatch,
			}
			expanded, err := conformance.ExpandFailureRules(a, conformance.Case{ID: "selfcheck", ExpectedFailure: ef})
			if err != nil {
				rules.add(false, fmt.Sprintf("%s: %v", v.Name, err))
				continue
			}
			ruleList = expanded
		}
		set := conformance.RuleSet{Rules: ruleList}
		got := set.Classify(conformance.Symptom(v.Symptom))
		matchOK := got != nil && string(got.ID) == v.ExpectRule && string(got.Classification) == v.ExpectClass && got.SecondaryCode == v.ExpectCode
		detail := fmt.Sprintf("%s: no match", v.Name)
		if got != nil {
			detail = fmt.Sprintf("%s: rule=%s class=%s code=%s", v.Name, got.ID, got.Classification, got.SecondaryCode)
		}
		rules.add(matchOK, detail)
	}

	return pointer, jcs, digest, operators, rules, nil
}

func decodeMap(raw []byte) (map[string]any, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}
