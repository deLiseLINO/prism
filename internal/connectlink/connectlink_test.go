package connectlink

import (
	"reflect"
	"strings"
	"testing"
)

const testKey = "sk-link-secret-value"

func TestParseValidLink(t *testing.T) {
	raw := "PRISM://connect/?v=1&id=edge&wire=responses" +
		"&endpoint=http%3A%2F%2F10.0.0.1%3A8080%2Fv1&endpoint=http://10.0.0.2:8080/v1&endpoint=https://edge.example/v1" +
		"&mode=spread&model=m1&model=m%202&defaultModel=m1&key=" + testKey
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := Link{
		ID:           "edge",
		Wire:         "responses",
		Endpoints:    []string{"http://10.0.0.1:8080/v1", "http://10.0.0.2:8080/v1", "https://edge.example/v1"},
		Spread:       true,
		Models:       []string{"m1", "m 2"},
		DefaultModel: "m1",
		Key:          testKey,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %+v, want %+v", got, want)
	}
}

func TestParseMinimalLink(t *testing.T) {
	got, err := Parse("prism://connect?v=1&id=gw&wire=chat&endpoint=http://10.0.0.1:8080/v1&mode=failover")
	if err != nil {
		t.Fatal(err)
	}
	want := Link{ID: "gw", Wire: "chat", Endpoints: []string{"http://10.0.0.1:8080/v1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %+v, want %+v", got, want)
	}
}

func TestParseWebLink(t *testing.T) {
	params := "v=1&id=edge&wire=chat&endpoint=http%3A%2F%2F10.0.0.1%3A8080%2Fv1&model=m1&modelWire=m1%3Amessages&key=" + testKey
	want, err := Parse("prism://connect?" + params)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want.ModelWires, []ModelWire{{Model: "m1", Wire: "messages"}}) {
		t.Fatalf("model wires = %+v", want.ModelWires)
	}
	for _, raw := range []string{
		"https://edge.example/connect#" + params,
		"HTTPS://edge.example:8443/connect/#" + params,
	} {
		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Parse(%q) = %+v, want %+v", raw, got, want)
		}
	}
}

func TestParseRejectsWebLink(t *testing.T) {
	params := "v=1&id=edge&wire=chat&endpoint=http://10.0.0.1:8080/v1&key=" + testKey
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"plain http", "http://edge.example/connect#" + params, "prism://connect"},
		{"wrong path", "https://edge.example/add#" + params, "https://<host>/connect#"},
		{"parameters in query", "https://edge.example/connect?" + params, "https://<host>/connect#"},
		{"query and fragment", "https://edge.example/connect?a=b#" + params, "https://<host>/connect#"},
		{"user info", "https://u:p@edge.example/connect#" + params, "https://<host>/connect#"},
		{"no host", "https:///connect#" + params, "https://<host>/connect#"},
		{"no fragment", "https://edge.example/connect", "no parameters"},
		{"empty fragment", "https://edge.example/connect#", "no parameters"},
		{"second fragment", "https://edge.example/connect#" + params + "#x", "fragment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.raw)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Fatalf("error %q contains the key", err)
			}
		})
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, l := range []Link{
		{ID: "gw", Wire: "chat", Endpoints: []string{"http://10.0.0.1:8080/v1"}},
		{ID: "edge", Wire: "messages", Endpoints: []string{"https://edge.example/v1", "http://10.0.0.2:8080/v1"}, Spread: true, Models: []string{"m1", "m 2&x"}, DefaultModel: "m 2&x", Key: "k+y/=&?#%"},
		{ID: "router", Wire: "responses", Endpoints: []string{"http://10.0.0.1:8080/v1?a=b&c=d"}, Key: testKey},
		{ID: "gw", Wire: "responses", Endpoints: []string{"http://10.0.0.1:8080/v1"}, Models: []string{"m1", "ns:m2"}, ModelWires: []ModelWire{{Model: "ns:m2", Wire: "messages"}, {Model: "m1", Wire: "chat"}}, Key: testKey},
		{ID: "gw", Wire: "responses", Endpoints: []string{"http://10.0.0.1:8080/v1"}, ModelWires: []ModelWire{{Model: "m1", Wire: "chat"}}},
	} {
		raw := l.String()
		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		if !reflect.DeepEqual(got, l) {
			t.Fatalf("round trip of %q = %+v, want %+v", raw, got, l)
		}
		if got.String() != raw {
			t.Fatalf("String not canonical: %q then %q", raw, got.String())
		}
	}
}

func TestParseRejects(t *testing.T) {
	base := "v=1&id=edge&wire=chat&endpoint=http://10.0.0.1:8080/v1"
	key := "&key=" + testKey
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"wrong scheme", "https://connect?" + base + key, "prism://connect"},
		{"wrong host", "prism://link?" + base + key, "prism://connect"},
		{"host with port", "prism://connect:80?" + base + key, "prism://connect"},
		{"path", "prism://connect/add?" + base + key, "path"},
		{"missing version", "prism://connect?id=edge&wire=chat&endpoint=http://10.0.0.1:8080/v1" + key, "no version"},
		{"unsupported version", "prism://connect?v=2&id=edge&wire=chat&endpoint=http://10.0.0.1:8080/v1" + key, "version \"2\" is not supported"},
		{"missing id", "prism://connect?v=1&wire=chat&endpoint=http://10.0.0.1:8080/v1" + key, "no provider id"},
		{"bad id", "prism://connect?v=1&id=edge:a&wire=chat&endpoint=http://10.0.0.1:8080/v1" + key, "provider id"},
		{"missing wire", "prism://connect?v=1&id=edge&endpoint=http://10.0.0.1:8080/v1" + key, "no wire"},
		{"non-custom wire", "prism://connect?v=1&id=edge&wire=codex&endpoint=http://10.0.0.1:8080/v1" + key, "wire \"codex\" is not supported"},
		{"missing endpoint", "prism://connect?v=1&id=edge&wire=chat" + key, "no endpoint"},
		{"non-http endpoint", "prism://connect?" + base + "&endpoint=ftp://10.0.0.2/v1" + key, "not an absolute http or https URL"},
		{"relative endpoint", "prism://connect?v=1&id=edge&wire=chat&endpoint=/v1" + key, "not an absolute http or https URL"},
		{"duplicate endpoint", "prism://connect?" + base + "&endpoint=http://10.0.0.1:8080/v1/" + key, "repeats another endpoint"},
		{"bad mode", "prism://connect?" + base + "&mode=random" + key, "endpoint mode"},
		{"unknown parameter", "prism://connect?" + base + "&modle=m1" + key, "unsupported parameter"},
		{"bare fragment", "prism://connect?" + base + "&key=abc#", "fragment"},
		{"repeated id", "prism://connect?" + base + "&id=gw" + key, "more than once"},
		{"empty model", "prism://connect?" + base + "&model=" + key, "empty model"},
		{"empty key", "prism://connect?" + base + "&key=", "empty key"},
		{"bad escape", "prism://connect?" + base + key + "%zz", "percent-encoded"},
		{"over-long link", "prism://connect?" + base + key + "&model=" + strings.Repeat("m", MaxLength), "longer than"},
		{"modelWire without colon", "prism://connect?" + base + "&modelWire=m1" + key, "without ':'"},
		{"modelWire empty model", "prism://connect?" + base + "&modelWire=:chat" + key, "empty model"},
		{"modelWire unknown wire", "prism://connect?" + base + "&modelWire=m1:codex" + key, "wire \"codex\" for model \"m1\" is not supported"},
		{"modelWire empty wire", "prism://connect?" + base + "&modelWire=m1:" + key, "is not supported"},
		{"modelWire unlisted model", "prism://connect?" + base + "&model=m1&modelWire=m2:chat" + key, "not one of the link's models"},
		{"modelWire repeated model", "prism://connect?" + base + "&modelWire=m1:chat&modelWire=m1:messages" + key, "more than once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.raw)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Fatalf("error %q contains the key", err)
			}
		})
	}
}

func TestParseErrorNeverQuotesLink(t *testing.T) {
	for _, raw := range []string{
		"prism://conn ect?key=" + testKey,
		"prism://connect?v=1&id=edge&wire=chat&endpoint=http://10.0.0.1:8080/v1&key=" + testKey + "#frag",
		"%zzprism://connect?key=" + testKey,
	} {
		_, err := Parse(raw)
		if err == nil {
			t.Fatalf("Parse(%q) succeeded", raw)
		}
		if strings.Contains(err.Error(), testKey) {
			t.Fatalf("error %q contains the key", err)
		}
	}
}

func TestParseErrorHidesKeyWithUnencodedAmpersand(t *testing.T) {
	raw := "prism://connect?v=1&id=edge&wire=chat&endpoint=http://10.0.0.1:8080/v1&key=sk-abc&XyZ9secretTail"
	_, err := Parse(raw)
	if err == nil {
		t.Fatalf("Parse(%q) succeeded", raw)
	}
	if strings.Contains(err.Error(), "XyZ9secretTail") || strings.Contains(err.Error(), "sk-abc") {
		t.Fatalf("error %q contains part of the key", err)
	}
}

func TestMaskKey(t *testing.T) {
	for key, want := range map[string]string{
		"":          "",
		"abc":       "****",
		"abcd":      "****",
		"sk-abcdef": "sk-a…",
		"ключ-1234": "ключ…",
	} {
		if got := MaskKey(key); got != want {
			t.Errorf("MaskKey(%q) = %q, want %q", key, got, want)
		}
	}
}
