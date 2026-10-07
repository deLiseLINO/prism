package responses

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
)

// "none" is the Responses wire value that turns reasoning off; every egress
// now sends "none" for EffortOff, so the ingress must produce EffortOff
// (chat and messages ingress already do).
func TestEffortNoneDisablesReasoning(t *testing.T) {
	for _, v := range []string{"none", "off"} {
		req, _, _ := mustParse(t, `{"model":"p/m","input":"hi","reasoning":{"effort":"`+v+`"}}`, nil)
		if req.Reasoning.Effort != canon.EffortOff {
			t.Fatalf("effort %q -> %v, want EffortOff", v, req.Reasoning.Effort)
		}
	}
}
