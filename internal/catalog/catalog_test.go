package catalog

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixture = `{
  "xai": {
    "models": {
      "grok-4": {
        "id": "grok-4",
        "limit": {"context": 256000},
        "modalities": {"input": ["text"]}
      },
      "grok-4.7": {
        "id": "grok-4.7",
        "limit": {"context": 500000},
        "modalities": {"input": ["text", "image"]}
      }
    }
  },
  "other": {
    "models": {
      "alias": {
        "id": "grok-4.7",
        "limit": {"context": 500000},
        "modalities": {"input": ["image"]}
      },
      "glm-a": {
        "id": "glm-5.3",
        "limit": {"context": 128000},
        "modalities": {"input": ["text"]}
      },
      "glm-b": {
        "id": "zai/glm-5.3",
        "limit": {"context": 200000},
        "modalities": {"input": ["text"]}
      },
      "flash-a": {
        "id": "glm-5.3-flash",
        "limit": {"context": 128000},
        "modalities": {"input": ["text", "image"]}
      },
      "flash-b": {
        "id": "glm-5.3-flash",
        "limit": {"context": 128000},
        "modalities": {"input": ["text"]}
      },
      "luna": {
        "id": "openai/gpt-5.6-luna",
        "limit": {"context": 400000},
        "modalities": {"input": ["text", "image"]}
      },
      "silent": {
        "id": "no-modalities",
        "limit": {"context": 32000}
      },
      "null-mod": {
        "id": "null-modalities",
        "limit": {"context": 16000},
        "modalities": {"input": null}
      },
      "bad-window": {
        "id": "abstain-window",
        "limit": {"context": 0},
        "modalities": {"input": ["text"]}
      },
      "string-window": {
        "id": "string-window",
        "limit": {"context": "500000"},
        "modalities": {"input": ["image"]}
      }
    }
  }
}`

func TestParseFixture(t *testing.T) {
	rows, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	x := Empty()
	if err := x.store(rows); err != nil {
		t.Fatal(err)
	}

	grok := x.Lookup("grok-4.7")
	if grok.ContextWindow != 500000 || grok.Image == nil || !*grok.Image {
		t.Fatalf("grok-4.7 = %+v, want 500000 image true", grok)
	}
	bare := x.Lookup("grok-4")
	if bare.ContextWindow != 256000 || bare.Image == nil || *bare.Image {
		t.Fatalf("grok-4 = %+v, want 256000 image false", bare)
	}
	if got := x.Lookup("grok-4.7-preview"); !reflect.DeepEqual(got, Facts{}) {
		t.Fatalf("prefix match = %+v", got)
	}

	glm := x.Lookup("glm-5.3")
	if glm.ContextWindow != 0 {
		t.Fatalf("glm-5.3 window = %d, want unknown", glm.ContextWindow)
	}
	if glm.Image == nil || *glm.Image {
		t.Fatalf("glm-5.3 image = %v, want false", glm.Image)
	}
	if got := x.Lookup("zai/glm-5.3"); got.ContextWindow != 200000 || got.Image == nil || *got.Image {
		t.Fatalf("zai/glm-5.3 = %+v", got)
	}

	flash := x.Lookup("glm-5.3-flash")
	if flash.ContextWindow != 128000 {
		t.Fatalf("flash window = %d, want 128000", flash.ContextWindow)
	}
	if flash.Image != nil {
		t.Fatalf("flash image = %v, want unknown", *flash.Image)
	}

	luna := x.Lookup("gpt-5.6-luna")
	full := x.Lookup("openai/gpt-5.6-luna")
	if luna.ContextWindow != 400000 || luna.Image == nil || !*luna.Image {
		t.Fatalf("gpt-5.6-luna = %+v", luna)
	}
	if full.ContextWindow != luna.ContextWindow || full.Image == nil || *full.Image != *luna.Image {
		t.Fatalf("full id = %+v, bare = %+v", full, luna)
	}

	silent := x.Lookup("no-modalities")
	if silent.ContextWindow != 32000 || silent.Image != nil {
		t.Fatalf("missing modalities = %+v", silent)
	}
	nullMod := x.Lookup("null-modalities")
	if nullMod.ContextWindow != 16000 || nullMod.Image != nil {
		t.Fatalf("null modalities = %+v", nullMod)
	}
	if got := x.Lookup("abstain-window"); got.ContextWindow != 0 || got.Image == nil || *got.Image {
		t.Fatalf("non-positive window = %+v", got)
	}
	if got := x.Lookup("string-window"); got.ContextWindow != 0 || got.Image == nil || !*got.Image {
		t.Fatalf("string window = %+v", got)
	}
	if got := x.Lookup("missing"); !reflect.DeepEqual(got, Facts{}) {
		t.Fatalf("unknown id = %+v", got)
	}
}

func TestParseMajorityBeatsDissent(t *testing.T) {
	body := `{
	  "a": {"models": {"m": {"id": "glm-5.3", "limit": {"context": 1000000}, "modalities": {"input": ["text"]}}}},
	  "b": {"models": {"m": {"id": "glm-5.3", "limit": {"context": 1000000}, "modalities": {"input": ["text", "image"]}}}},
	  "c": {"models": {"m": {"id": "glm-5.3", "limit": {"context": 1048576}, "modalities": {"input": ["text"]}}}}
	}`
	rows, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	got := rows["glm-5.3"]
	if got.ContextWindow != 1000000 {
		t.Fatalf("window = %d, want 1000000", got.ContextWindow)
	}
	if got.Image == nil || *got.Image {
		t.Fatalf("image = %v, want false", got.Image)
	}
}

func TestParseTieStaysUnknown(t *testing.T) {
	body := `{
	  "a": {"models": {"m": {"id": "glm-5.3", "limit": {"context": 1000000}, "modalities": {"input": ["text", "image"]}}}},
	  "b": {"models": {"m": {"id": "glm-5.3", "limit": {"context": 1048576}, "modalities": {"input": ["text"]}}}}
	}`
	rows, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rows["glm-5.3"]; ok {
		t.Fatalf("tie stored %+v", rows["glm-5.3"])
	}
}

func TestParseProviderSplitAbstains(t *testing.T) {
	body := `{
	  "split": {"models": {
	    "a": {"id": "glm-5.3", "limit": {"context": 1000000}, "modalities": {"input": ["text"]}},
	    "b": {"id": "zai/glm-5.3", "limit": {"context": 1048576}, "modalities": {"input": ["text", "image"]}}
	  }},
	  "one": {"models": {"m": {"id": "glm-5.3", "limit": {"context": 1000000}, "modalities": {"input": ["text"]}}}},
	  "two": {"models": {"m": {"id": "glm-5.3", "limit": {"context": 1000000}, "modalities": {"input": ["text"]}}}}
	}`
	rows, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	got := rows["glm-5.3"]
	if got.ContextWindow != 1000000 {
		t.Fatalf("window = %d, want 1000000 from the agreeing providers", got.ContextWindow)
	}
	if got.Image == nil || *got.Image {
		t.Fatalf("image = %v, want false", got.Image)
	}
	full := rows["zai/glm-5.3"]
	if full.ContextWindow != 1048576 || full.Image == nil || !*full.Image {
		t.Fatalf("full id = %+v", full)
	}
}

func TestRefreshTwiceSameBody(t *testing.T) {
	dir := t.TempDir()
	x, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	client := fixtureClient(fixture)
	if err := x.Refresh(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	first := x.Lookup("grok-4.7")
	if err := x.Refresh(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	second := x.Lookup("grok-4.7")
	if !sameFacts(first, second) {
		t.Fatalf("second refresh = %+v, first = %+v", second, first)
	}
	reloaded, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Lookup("grok-4.7"); !sameFacts(got, first) {
		t.Fatalf("cache = %+v, want %+v", got, first)
	}
}

func TestFailedFetchKeepsLoadedIndex(t *testing.T) {
	dir := t.TempDir()
	x, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Refresh(context.Background(), fixtureClient(fixture)); err != nil {
		t.Fatal(err)
	}
	before := x.Lookup("grok-4.7")
	cache, err := os.ReadFile(filepath.Join(dir, cacheName))
	if err != nil {
		t.Fatal(err)
	}
	fail := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, os.ErrDeadlineExceeded
	})}
	if err := x.Refresh(context.Background(), fail); err == nil {
		t.Fatal("failed fetch returned nil")
	}
	if got := x.Lookup("grok-4.7"); !sameFacts(got, before) {
		t.Fatalf("memory after failed fetch = %+v", got)
	}
	after, err := os.ReadFile(filepath.Join(dir, cacheName))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(cache) {
		t.Fatal("failed fetch rewrote the cache")
	}
}

func TestOpenMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	missing, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := missing.Lookup("grok-4.7"); !reflect.DeepEqual(got, Facts{}) {
		t.Fatalf("missing cache = %+v", got)
	}
	path := filepath.Join(dir, cacheName)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	corrupt, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := corrupt.Lookup("grok-4.7"); !reflect.DeepEqual(got, Facts{}) {
		t.Fatalf("corrupt cache = %+v", got)
	}
	left, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != "{not json" {
		t.Fatalf("corrupt file rewritten: %s", left)
	}
}

func TestParseRejectsNonObject(t *testing.T) {
	if _, err := Parse([]byte(`[]`)); err == nil {
		t.Fatal("array body parsed")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("garbage parsed")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureClient(body string) *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != Endpoint {
			return nil, os.ErrInvalid
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       ioBody(body),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
}

type strBody struct {
	s string
	i int
}

func ioBody(s string) *strBody { return &strBody{s: s} }

func (b *strBody) Read(p []byte) (int, error) {
	if b.i >= len(b.s) {
		return 0, io.EOF
	}
	n := copy(p, b.s[b.i:])
	b.i += n
	if b.i >= len(b.s) {
		return n, io.EOF
	}
	return n, nil
}

func (b *strBody) Close() error { return nil }

func sameFacts(a, b Facts) bool {
	if a.ContextWindow != b.ContextWindow {
		return false
	}
	if a.Image == nil || b.Image == nil {
		return a.Image == nil && b.Image == nil
	}
	return *a.Image == *b.Image
}

func effortBody(values ...string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = `"` + v + `"`
	}
	return `[{"type":"effort","values":[` + strings.Join(quoted, ",") + `]}]`
}

func provider(models ...string) string {
	return `{"models":{` + strings.Join(models, ",") + `}}`
}

func effortRow(key, id string, values ...string) string {
	return `"` + key + `":{"id":"` + id + `","reasoning_options":` + effortBody(values...) + `}`
}

func TestParseEffortsMajorityAndNormalization(t *testing.T) {
	body := `{
	  "a": ` + provider(effortRow("m", "glm-5.3", "high", "low", "max")) + `,
	  "b": ` + provider(effortRow("m", "glm-5.3", "low", "high", "max")) + `,
	  "c": ` + provider(effortRow("m", "glm-5.3", "none", "low", "medium", "high")) + `,
	  "d": ` + provider(effortRow("m", "sol", "none", "low", "turbo", "high")) + `
	}`
	rows, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"low", "high", "max"}
	if got := rows["glm-5.3"].Efforts; !reflect.DeepEqual(got, want) {
		t.Fatalf("glm-5.3 efforts = %v, want %v", got, want)
	}
	if got := rows["sol"].Efforts; !reflect.DeepEqual(got, []string{"off", "low", "high"}) {
		t.Fatalf("none must become off and unknown rungs drop: %v", got)
	}
}

func TestParseEffortsTieAndProviderSplitStayUnknown(t *testing.T) {
	tie := `{
	  "a": ` + provider(effortRow("m", "x", "low", "high")) + `,
	  "b": ` + provider(effortRow("m", "x", "high", "max")) + `
	}`
	rows, err := Parse([]byte(tie))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rows["x"]; ok {
		t.Fatalf("tie stored %+v", rows["x"])
	}

	split := `{
	  "split": ` + provider(effortRow("a", "x", "low", "high"), effortRow("b", "x", "high", "max")) + `,
	  "one": ` + provider(effortRow("m", "x", "low", "high")) + `,
	  "two": ` + provider(effortRow("m", "x", "low", "high")) + `
	}`
	rows, err = Parse([]byte(split))
	if err != nil {
		t.Fatal(err)
	}
	if got := rows["x"].Efforts; !reflect.DeepEqual(got, []string{"low", "high"}) {
		t.Fatalf("split provider must abstain, got %v", got)
	}
}

func TestParseEffortsIgnoresBoolReasoningAndMissingOptions(t *testing.T) {
	body := `{"a": ` + provider(`"m":{"id":"y","reasoning":true,"limit":{"context":1000}}`) + `}`
	rows, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := rows["y"]; len(got.Efforts) != 0 || got.ContextWindow != 1000 {
		t.Fatalf("y = %+v", got)
	}
}
