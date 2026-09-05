package antigravity

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"prism/internal/canon"
)

const grokFixturePath = "/tmp/grok-req.json"

func TestSanitizeTypeArrayBecomesScalar(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "integer null union",
			in:   `{"type":"object","properties":{"q":{"type":["integer","null"],"format":"uint64"}}}`,
			want: `{"type":"object","properties":{"q":{"type":"integer","nullable":true,"format":"uint64"}}}`,
		},
		{
			name: "single entry array",
			in:   `{"type":"object","properties":{"q":{"type":["string"]}}}`,
			want: `{"type":"object","properties":{"q":{"type":"string"}}}`,
		},
		{
			name: "disallowed type dropped",
			in:   `{"type":"object","properties":{"q":{"type":"not-a-type"}}}`,
			want: `{"type":"object","properties":{"q":{}}}`,
		},
		{
			name: "root without properties gains empty bag",
			in:   `{"type":"object"}`,
			want: `{"type":"object","properties":{}}`,
		},
		{
			name: "unknown keyword dropped",
			in:   `{"type":"object","properties":{"q":{"type":"string","minimum":0}},"additionalProperties":false}`,
			want: `{"type":"object","properties":{"q":{"type":"string"}}}`,
		},
		{
			name: "const becomes enum",
			in:   `{"type":"object","properties":{"q":{"type":"string","const":"x"}}}`,
			want: `{"type":"object","properties":{"q":{"type":"string","enum":["x"]}}}`,
		},
		{
			name: "required filtered to surviving properties",
			in:   `{"type":"object","properties":{"a":{"type":"string"}},"required":["a","ghost"]}`,
			want: `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`,
		},
		{
			name: "items sanitized",
			in:   `{"type":"object","properties":{"q":{"type":"array","items":{"type":["integer","null"]}}}}`,
			want: `{"type":"object","properties":{"q":{"type":"array","items":{"type":"integer","nullable":true}}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(sanitizeToolParameters([]byte(tc.in)))
			if got != tc.want {
				t.Fatalf("sanitize mismatch:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSanitizeAnyOfCollapses(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single non-null branch collapses to nullable",
			in:   `{"type":"object","properties":{"q":{"anyOf":[{"type":"integer"},{"type":"null"}]}}}`,
			want: `{"type":"object","properties":{"q":{"type":"integer","nullable":true}}}`,
		},
		{
			name: "enum-only union merges",
			in:   `{"type":"object","properties":{"q":{"anyOf":[{"type":"string","enum":["a"]},{"type":"string","enum":["b","a"]}]}}}`,
			want: `{"type":"object","properties":{"q":{"type":"string","enum":["a","b"]}}}`,
		},
		{
			name: "heterogeneous union widens to empty",
			in:   `{"type":"object","properties":{"q":{"anyOf":[{"type":"integer"},{"type":"string"}]}}}`,
			want: `{"type":"object","properties":{"q":{}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(sanitizeToolParameters([]byte(tc.in)))
			if got != tc.want {
				t.Fatalf("anyOf mismatch:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSanitizeRefInlining(t *testing.T) {
	in := `{"type":"object","$defs":{"item":{"type":["integer","null"]}},"properties":{"q":{"$ref":"#/$defs/item"}}}`
	want := `{"type":"object","properties":{"q":{"type":"integer","nullable":true}}}`
	got := string(sanitizeToolParameters([]byte(in)))
	if got != want {
		t.Fatalf("ref inline mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestSanitizeRefCycleTerminates(t *testing.T) {
	in := `{"type":"object","$defs":{"loop":{"$ref":"#/$defs/loop"}},"properties":{"q":{"$ref":"#/$defs/loop"}}}`
	got := string(sanitizeToolParameters([]byte(in)))
	if !strings.Contains(got, `"q":{}`) {
		t.Fatalf("cycle must widen to empty schema: %s", got)
	}
}

func TestSanitizeDepthLimitWidens(t *testing.T) {
	deep := `{"type":"object"}`
	for i := 0; i < 30; i++ {
		deep = `{"type":"object","properties":{"q":` + deep + `}}`
	}
	got := string(sanitizeToolParameters([]byte(deep)))
	if !strings.Contains(got, `"q":{}`) {
		t.Fatalf("depth overflow must widen to empty: %s", got)
	}
}

func TestSanitizeNodeBudgetStops(t *testing.T) {
	props := make(map[string]any)
	for i := 0; i < 2000; i++ {
		props[string(rune('a'+i%26))+string(rune('0'+i/26))] = map[string]any{"type": "string"}
	}
	root := map[string]any{"type": "object", "properties": props}
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	got := string(sanitizeToolParameters(raw))
	if !strings.Contains(got, `"type":"object"`) || len(got) > 60000 {
		t.Fatalf("budget exhaustion must stop early and keep a valid root: len=%d", len(got))
	}
}

func TestSanitizePropertyNameBagNotKeyword(t *testing.T) {
	in := `{"type":"object","properties":{"type":{"type":"string"},"enum":{"type":"integer"}}}`
	want := `{"type":"object","properties":{"enum":{"type":"integer"},"type":{"type":"string"}}}`
	got := string(sanitizeToolParameters([]byte(in)))
	if got != want {
		t.Fatalf("property names must survive as names:\n got %s\nwant %s", got, want)
	}
}

func TestSanitizeInvalidJSONFallsBack(t *testing.T) {
	got := string(sanitizeToolParameters([]byte(`{not json`)))
	if got != rootSchemaFallback {
		t.Fatalf("invalid input must fall back: %s", got)
	}
}

func TestEnvelopeSanitizesGrokFixtureTools(t *testing.T) {
	if _, err := os.Stat(grokFixturePath); err != nil {
		t.Skipf("grok fixture not present: %v", err)
	}
	raw, err := os.ReadFile(grokFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Tools []struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Tools) < 2 {
		t.Fatalf("fixture must carry at least 2 tools, got %d", len(payload.Tools))
	}
	tools := make([]canon.Tool, 0, len(payload.Tools))
	for _, tm := range payload.Tools {
		tools = append(tools, canon.FunctionTool{Name: canon.ToolName(tm.Name), Parameters: tm.Parameters})
	}
	req := baseRequest()
	req.Tools = tools
	body, err := BuildEnvelope(req, "p", "r", "-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"type":[`) {
		t.Fatalf("envelope must not carry array types to the wire: %s", body[:400])
	}
	var envelope struct {
		Request struct {
			Tools []struct {
				Declarations []struct {
					Parameters json.RawMessage `json:"parameters"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		} `json:"request"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, tool := range envelope.Request.Tools {
		total += len(tool.Declarations)
	}
	if total != len(payload.Tools) {
		t.Fatalf("all declarations must survive: want %d got %d", len(payload.Tools), total)
	}
}
