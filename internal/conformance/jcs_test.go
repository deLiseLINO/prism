package conformance

import (
	"encoding/json"
	"math"
	"testing"
)

func jcsMust(t *testing.T, v any) string {
	t.Helper()
	out, err := JCS(v)
	if err != nil {
		t.Fatalf("JCS(%v): %v", v, err)
	}
	return string(out)
}

func jcsReject(t *testing.T, v any) {
	t.Helper()
	if _, err := JCS(v); err == nil {
		t.Fatalf("JCS(%v): expected error, got none", v)
	}
}

func decode(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func TestJCSCorners(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"key order", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"minus zero", `{"n":-0}`, `{"n":0}`},
		{"html unescaped", `{"s":"<>&"}`, `{"s":"<>&"}`},
		{"escapes", `{"s":"a\"b\\c\nd\te\ff\rg\u0000h"}`, `{"s":"a\"b\\c\nd\te\ff\rg\u0000h"}`},
		{"line separators", `{"s":"\u2028\u2029"}`, `{"s":"\u2028\u2029"}`},
		{"big exponent", `{"n":1e21}`, `{"n":1e+21}`},
		{"small exponent", `{"n":1.5e-7}`, `{"n":1.5e-7}`},
		{"plain fraction", `{"n":0.1}`, `{"n":0.1}`},
		{"int64 precision loss", `{"n":9007199254740993}`, `{"n":9007199254740992}`},
		{"nested", `{"d":"x","a":[1,{"c":null,"b":true}]}`, `{"a":[1,{"b":true,"c":null}],"d":"x"}`},
		{"empty object", `{}`, `{}`},
		{"empty array", `[]`, `[]`},
		{"bool", `true`, `true`},
		{"null", `null`, `null`},
		{"routerl char", `{"s":"\ud834\udd1e"}`, `{"s":"𝄞"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jcsMust(t, decode(t, tc.raw)); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestJCSRejects(t *testing.T) {
	jcsReject(t, math.NaN())
	jcsReject(t, math.Inf(1))
	jcsReject(t, math.Inf(-1))
	jcsReject(t, "invalid\xffutf8")
}

func TestJCSDirectValues(t *testing.T) {
	if got := jcsMust(t, 1e21); got != "1e+21" {
		t.Fatalf("1e21: got %s", got)
	}
	if got := jcsMust(t, 1.5e-7); got != "1.5e-7" {
		t.Fatalf("1.5e-7: got %s", got)
	}
	if got := jcsMust(t, float64(10000)); got != "10000" {
		t.Fatalf("10000: got %s", got)
	}
	if got := jcsMust(t, float64(123456789012345680000)); got != "123456789012345680000" {
		t.Fatalf("1.2345678901234568e20: got %s", got)
	}
}

func TestJCSKeySortCodePoint(t *testing.T) {
	got := jcsMust(t, decode(t, `{"\u00e9":1,"e":2}`))
	if got != `{"e":2,"é":1}` {
		t.Fatalf("code-point sort: got %s", got)
	}
}

func TestEqual(t *testing.T) {
	if !Equal(decode(t, `{"b":1,"a":2}`), decode(t, `{"a":2,"b":1}`)) {
		t.Fatal("key order must not matter")
	}
	if Equal(decode(t, `[1,2]`), decode(t, `[2,1]`)) {
		t.Fatal("array order must matter")
	}
	if !Equal(decode(t, `0`), decode(t, `-0`)) {
		t.Fatal("-0 must equal 0")
	}
	if !Equal(decode(t, `0`), decode(t, `0.0`)) {
		t.Fatal("0 must equal 0.0")
	}
	if Equal(decode(t, `{"a":1}`), decode(t, `{"a":2}`)) {
		t.Fatal("different values must differ")
	}
	if Equal(decode(t, `{"a":1}`), decode(t, `{"a":1,"b":2}`)) {
		t.Fatal("different keys must differ")
	}
}
