package openaierr

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
)

func TestParseKeepsBothSlotsAndUnknownKeys(t *testing.T) {
	raw := []byte(`{"response":{"error":{"message":"boom","code":"server_error","type":"api_error","extra":true},"status_details":{"error":{"code":"rate_limit_exceeded","message":"Slow down.","extra":1}}}}`)
	got, ok := Parse(raw)
	if !ok {
		t.Fatal("Parse returned false")
	}
	assertJSONEqual(t, got.Error, []byte(`{"message":"boom","code":"server_error","type":"api_error","extra":true}`))
	assertJSONEqual(t, got.StatusDetails, []byte(`{"code":"rate_limit_exceeded","message":"Slow down.","extra":1}`))
	if Text(got) != "boom" {
		t.Fatalf("Text = %q, want boom", Text(got))
	}
}

func TestParseStatusDetailsOnly(t *testing.T) {
	raw := []byte(`{"response":{"status_details":{"error":{"code":"rate_limit_exceeded","message":"Slow down.","extra":1}}}}`)
	got, ok := Parse(raw)
	if !ok {
		t.Fatal("Parse returned false")
	}
	if len(got.Error) != 0 {
		t.Fatalf("Error = %s, want empty", got.Error)
	}
	assertJSONEqual(t, got.StatusDetails, []byte(`{"code":"rate_limit_exceeded","message":"Slow down.","extra":1}`))
	if Text(got) != "Slow down." {
		t.Fatalf("Text = %q", Text(got))
	}
}

func TestParseStringError(t *testing.T) {
	got, ok := Parse([]byte(`{"error":"quota"}`))
	if !ok {
		t.Fatal("Parse returned false")
	}
	assertJSONEqual(t, got.Error, []byte(`"quota"`))
	if Text(got) != "quota" {
		t.Fatalf("Text = %q", Text(got))
	}
}

func TestParseBareErrorObject(t *testing.T) {
	raw := []byte(`{"message":"nope","code":"no","nested":{"a":1}}`)
	got, ok := Parse(raw)
	if !ok {
		t.Fatal("Parse returned false")
	}
	assertJSONEqual(t, got.Error, raw)
}

func TestParseFalseWhenNoErrorValue(t *testing.T) {
	for _, raw := range []string{`{}`, `{"response":{"status":"failed"}}`, `not json`, ``, `null`, `{"error":null}`} {
		if _, ok := Parse([]byte(raw)); ok {
			t.Fatalf("Parse(%s) = true, want false", raw)
		}
	}
}

func TestHasProvider(t *testing.T) {
	empty := canon.ProviderError{}
	if (canon.Failure{Provider: &empty}).HasProvider() {
		t.Fatal("empty provider must not count")
	}
	set := canon.ProviderError{StatusDetails: []byte(`{"code":"x"}`)}
	if !(canon.Failure{Provider: &set}).HasProvider() {
		t.Fatal("status details must count")
	}
	if (canon.Failure{}).HasProvider() {
		t.Fatal("nil provider must not count")
	}
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("got is not JSON: %s", got)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatalf("want is not JSON: %s", want)
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if !bytes.Equal(ab, bb) {
		t.Fatalf("json = %s, want %s", ab, bb)
	}
}
