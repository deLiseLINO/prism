package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"prism/internal/conformance"
	"prism/internal/conformance/codec/chat"
	"prism/internal/conformance/codec/responses"
)

func main() {
	fixturesPath := flag.String("fixtures", "cmd/prism-wirecheck/fixtures/protocol-v1-cases.json", "path to conformance fixture file")
	caseID := flag.String("case", "", "run one case by id; empty runs the responses-core, chat-core, vision-core, reasoning-core, tools-core, anthropic-core and codex-core suites")
	noSelfCheck := flag.Bool("no-self-check", false, "skip the DSL self-check gauntlet")
	flag.Parse()

	authority, err := conformance.Load(*fixturesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wirecheck: %v\n", err)
		os.Exit(1)
	}

	suites := map[string]int{}
	fixtures := 0
	var selected []conformance.Case
	for i := range authority.Cases {
		c := &authority.Cases[i]
		suites[c.Suite]++
		fixtures++
		if c.InitiatingRequest != nil {
			fixtures++
		}
		if *caseID != "" {
			if c.ID == *caseID {
				selected = append(selected, *c)
			}
			continue
		}
		if c.Suite == "responses-core" || c.Suite == "chat-core" || c.Suite == "vision-core" || c.Suite == "reasoning-core" || c.Suite == "tools-core" || c.Suite == "anthropic-core" || c.Suite == "codex-core" {
			selected = append(selected, *c)
		}
	}
	if len(selected) == 0 {
		fmt.Fprintf(os.Stderr, "wirecheck: unknown case %q\n", *caseID)
		os.Exit(1)
	}

	fmt.Printf("wirecheck: authority=%q sourceCommit=%s dsl=%s evidenceSchema=%s\n",
		conformance.AuthorityFile, authority.SourceCommit, authority.AssertionDSLVersion, authority.EvidenceSchemaVersion)
	fmt.Printf("wirecheck: manifest OK: %d cases, %d suites, %d fixtures (digests verified)\n\n",
		len(authority.Cases), len(suites), fixtures)

	goldens, err := conformance.LoadGoldens("cmd/prism-wirecheck/golden")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wirecheck: goldens: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("== live codec check ==")
	fmt.Println()

	runner := conformance.NewRoutedRunner(chat.Builder{}, responses.Builder{})
	opts := conformance.Options{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "fixture-key",
		Goldens: goldens,
	}
	passed := 0
	for i := range selected {
		res := runner.Run(context.Background(), selected[i], opts)
		printCase(selected[i], res, goldens)
		fmt.Println()
		if res.Passed {
			passed++
		}
	}

	selfCheckPassed := true
	if !*noSelfCheck {
		fmt.Println("== DSL self-check (synthetic, not part of the CL-00 authority) ==")
		fmt.Println()
		pointer, jcs, digest, operators, rules, err := runSelfCheck(authority)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wirecheck: self-check: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(pointer.line("pointer", "vectors OK", `RFC 6901 + reference quirks: ~0/~1, "-", leading zeros`))
		fmt.Println(jcs.line("jcs", "vectors OK", "RFC 8785: key order, -0, NaN reject, HTML chars unescaped"))
		fmt.Println(digest.line("digest", "vectors OK", "fixture/scenario/suite domain tags"))
		fmt.Println(operators.line("operators", "synthetic assertions OK", "13 operators x pass/fail"))
		fmt.Println(rules.line("rules", "ordered first-match OK", "incl. expected-failure synthesis before required-assertion"))
		fmt.Println()
		selfCheckPassed = pointer.ok == pointer.total &&
			jcs.ok == jcs.total &&
			digest.ok == digest.total &&
			operators.ok == operators.total &&
			rules.ok == rules.total
	}

	summary := fmt.Sprintf("wirecheck: %d codec case passed", passed)
	if *noSelfCheck {
		summary += ", DSL core self-check skipped"
	} else if selfCheckPassed {
		summary += ", DSL core self-check passed"
	} else {
		summary += ", DSL core self-check FAILED"
	}
	fmt.Println(summary)
	if passed != len(selected) || !selfCheckPassed {
		os.Exit(1)
	}
}

func printCase(c conformance.Case, res conformance.CaseResult, goldens map[string]conformance.Golden) {
	fmt.Println(res.ScenarioID)
	inbound, upstream, surface := conformance.ResolveProtocolExecutionContext(c)
	fmt.Printf("  context  inbound=%s upstream=%s surface=%s\n", inbound, upstream, surface)
	if res.Codec != nil {
		cc := res.Codec
		fmt.Printf("  codec    %s  %s %s\n", upstream, cc.Method, cc.URL)
		if cc.Diff == "" && cc.GoldenPinned {
			names := headerNames(goldens, cc.CaseID)
			if len(names) == 0 {
				fmt.Println("  golden   none pinned; assertions only")
			} else {
				fmt.Printf("  golden   body %dB exact match; headers [%s] case+order match\n", cc.BodyBytes, strings.Join(names, ", "))
			}
		} else if cc.Diff == "" {
			fmt.Printf("  golden   body %dB, no golden pinned (assertion-only case)\n", cc.BodyBytes)
		} else {
			fmt.Printf("  golden   MISMATCH (%s)\n", cc.Diff)
		}
	}
	for _, a := range res.AssertionResults {
		status := "PASS"
		if !a.Passed {
			status = "FAIL " + string(a.Reason)
		}
		selector := assertionSelector(c, a.ID)
		fmt.Printf("  assert   %-12s%-16s %-45s%s\n", a.ID, a.Operator, selector, status)
	}
	verdict := "PASS"
	if !res.Passed {
		verdict = "FAIL"
	}
	fmt.Printf("  verdict  %s  classification=%s secondary=%s (reference runner semantics)\n",
		verdict, res.Classification, res.SecondaryCode)
	for _, d := range res.Diagnostics {
		fmt.Printf("  diag     %s\n", d)
	}
}

func assertionSelector(c conformance.Case, id string) string {
	for _, a := range c.Assertions {
		if a.ID == id {
			return a.Selector
		}
	}
	return ""
}

func headerNames(goldens map[string]conformance.Golden, caseID string) []string {
	g, ok := goldens[caseID]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(g.Headers))
	for _, h := range g.Headers {
		names = append(names, h.Name)
	}
	return names
}
