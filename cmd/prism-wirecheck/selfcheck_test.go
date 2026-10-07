package main

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/conformance"
)

func TestSelfCheckFixtures(t *testing.T) {
	t.Chdir("../..")
	authority, err := conformance.Load("cmd/prism-wirecheck/fixtures/protocol-v1-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	pointer, jcs, digest, operators, rules, err := runSelfCheck(authority)
	if err != nil {
		t.Fatal(err)
	}
	for name, report := range map[string]sectionReport{"pointer": pointer, "jcs": jcs, "digest": digest, "operators": operators, "rules": rules} {
		if report.total == 0 || report.ok != report.total {
			t.Errorf("%s self-check: %d/%d, %s", name, report.ok, report.total, report.firstErr)
		}
	}
}
