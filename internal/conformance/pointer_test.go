package conformance

import "testing"

var pointerDoc = decodeJSON([]byte(`{
	"foo": {"bar": 1, "a/b": "slash", "m~n": "tilde", "a~1b": "decoded", "x~2": "literal2", "x~": "trail", "x~3y": "literal3", "": "empty-key", "日本語": "jp", "a b": "space"},
	"list": [10, {"name": "two"}, {"name": "three"}],
	"flag": true,
	"pi": 3.14,
	"nil": null,
	"scalar": 5,
	"empty": []
}`))

const rootJSON = `{"empty":[],"flag":true,"foo":{"":"empty-key","a b":"space","a/b":"slash","a~1b":"decoded","bar":1,"m~n":"tilde","x~":"trail","x~2":"literal2","x~3y":"literal3","日本語":"jp"},"list":[10,{"name":"two"},{"name":"three"}],"nil":null,"pi":3.14,"scalar":5}`

func TestResolve(t *testing.T) {
	cases := []struct {
		name     string
		selector string
		ok       bool
		want     string
	}{
		{"root", "/", true, rootJSON},
		{"empty pointer", "", false, ""},
		{"relative", "foo", false, ""},
		{"missing key", "/foo/nope", false, ""},
		{"traverse scalar", "/scalar/0", false, ""},
		{"traverse null", "/nil/x", false, ""},
		{"traverse array with key", "/list/name", false, ""},
		{"array index", "/list/0", true, `10`},
		{"array out of range", "/list/3", false, ""},
		{"array on empty", "/empty/0", false, ""},
		{"array minus", "/list/-", false, ""},
		{"array leading zero", "/list/01", false, ""},
		{"array double zero", "/list/00", false, ""},
		{"array negative", "/list/-1", false, ""},
		{"array non numeric", "/list/1a", false, ""},
		{"array element path", "/list/1/name", true, `"two"`},
		{"array element missing", "/list/1/nope", false, ""},
		{"tilde1", "/foo/a~1b", true, `"slash"`},
		{"tilde0", "/foo/m~0n", true, `"tilde"`},
		{"tilde01", "/foo/a~01b", true, `"decoded"`},
		{"literal tilde2", "/foo/x~2", true, `"literal2"`},
		{"trailing tilde", "/foo/x~", true, `"trail"`},
		{"literal tilde3", "/foo/x~3y", true, `"literal3"`},
		{"root not empty-key", "/", true, rootJSON},
		{"unicode key", "/foo/日本語", true, `"jp"`},
		{"space key", "/foo/a b", true, `"space"`},
		{"nested object", "/foo/bar", true, `1`},
		{"bool", "/flag", true, `true`},
		{"float", "/pi", true, `3.14`},
		{"null value", "/nil", true, `null`},
		{"array of objects", "/list/2/name", true, `"three"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Resolve(pointerDoc, tc.selector)
			if ok != tc.ok {
				t.Fatalf("Resolve(%s) ok=%v want %v", tc.selector, ok, tc.ok)
			}
			if !ok {
				return
			}
			gotJSON, err := JCS(got)
			if err != nil {
				t.Fatalf("JCS(%v): %v", got, err)
			}
			if string(gotJSON) != tc.want {
				t.Fatalf("Resolve(%s) = %s want %s", tc.selector, gotJSON, tc.want)
			}
		})
	}
}

func TestExists(t *testing.T) {
	if !Exists(pointerDoc, "/foo/bar") {
		t.Fatal("/foo/bar should exist")
	}
	if Exists(pointerDoc, "/foo/missing") {
		t.Fatal("/foo/missing should not exist")
	}
	if Exists(pointerDoc, "/list/-") {
		t.Fatal("/list/- should not exist")
	}
	if !Exists(pointerDoc, "/list/1/name") {
		t.Fatal("/list/1/name should exist")
	}
	if Exists(pointerDoc, "/list/01") {
		t.Fatal("/list/01 should not exist")
	}
}
