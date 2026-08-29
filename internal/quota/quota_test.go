package quota_test

import (
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"prism/internal/quota"
)

func sourceMembers() map[quota.Source]string {
	return map[quota.Source]string{
		quota.SourceHeader:   "SourceHeader",
		quota.SourceEndpoint: "SourceEndpoint",
		quota.SourceReport:   "SourceReport",
		quota.SourceProbe:    "SourceProbe",
	}
}

func TestLimitInvariants(t *testing.T) {
	zero := int64(0)
	positive := int64(1000)

	unknown := quota.Snapshot{Limit: nil}
	unlimited := quota.Snapshot{Limit: &zero}
	bounded := quota.Snapshot{Limit: &positive}

	if unknown.Limit != nil {
		t.Fatalf("nil Limit = %v, want nil (unknown)", unknown.Limit)
	}
	if unlimited.Limit == nil {
		t.Fatal("zero-pointer Limit = nil, want non-nil (unlimited)")
	}
	if *unlimited.Limit != 0 {
		t.Fatalf("unlimited Limit = %d, want 0", *unlimited.Limit)
	}
	if bounded.Limit == nil || *bounded.Limit != 1000 {
		t.Fatalf("bounded Limit = %v, want 1000", bounded.Limit)
	}
	if reflect.DeepEqual(unknown, unlimited) {
		t.Fatal("nil Limit and zero-pointer Limit must never compare equal")
	}
	if reflect.DeepEqual(bounded, unlimited) || reflect.DeepEqual(bounded, unknown) {
		t.Fatal("bounded Limit conflated with unknown or unlimited")
	}
}

func TestZeroSnapshotSane(t *testing.T) {
	var s quota.Snapshot
	if s.Used != 0 {
		t.Fatalf("zero Used = %d, want 0", s.Used)
	}
	if s.Limit != nil {
		t.Fatalf("zero Limit = %v, want nil (unknown)", s.Limit)
	}
	if !s.WindowEnd.IsZero() {
		t.Fatalf("zero WindowEnd = %v, want zero time", s.WindowEnd)
	}
	if s.Source != 0 {
		t.Fatalf("zero Source = %d, want 0 (no member)", s.Source)
	}
	if _, ok := sourceMembers()[s.Source]; ok {
		t.Fatal("zero Source must not claim a member")
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	limit := int64(5000)
	windowEnd := time.Date(2026, 8, 29, 23, 59, 59, 0, time.UTC)
	s := quota.Snapshot{Used: 1200, Limit: &limit, WindowEnd: windowEnd, Source: quota.SourceEndpoint}
	if s.Used != 1200 {
		t.Fatalf("Used = %d, want 1200", s.Used)
	}
	if s.Limit == nil || *s.Limit != 5000 {
		t.Fatalf("Limit = %v, want 5000", s.Limit)
	}
	if !s.WindowEnd.Equal(windowEnd) {
		t.Fatalf("WindowEnd = %v, want %v", s.WindowEnd, windowEnd)
	}
	if s.Source != quota.SourceEndpoint {
		t.Fatalf("Source = %d, want SourceEndpoint", s.Source)
	}
}

func TestSourceMembersConstructible(t *testing.T) {
	members := sourceMembers()
	if len(members) != 4 {
		t.Fatalf("distinct members = %d, want 4", len(members))
	}
	for v := quota.Source(1); v <= 4; v++ {
		if _, ok := members[v]; !ok {
			t.Fatalf("no member with value %d", v)
		}
	}
	for _, v := range []quota.Source{0, 5, 255} {
		if _, ok := members[v]; ok {
			t.Fatalf("value %d is a member outside the declared enum", v)
		}
	}
	for src := range members {
		s := quota.Snapshot{Used: 10, WindowEnd: time.Now(), Source: src}
		if s.Source != src {
			t.Fatalf("Source = %d, want %d", s.Source, src)
		}
	}
}

func TestPackageExportsClosed(t *testing.T) {
	fset := token.NewFileSet()
	var files []*ast.File
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	cfg := types.Config{Importer: importer.Default()}
	pkg, err := cfg.Check("quota", fset, files, nil)
	if err != nil {
		t.Fatal(err)
	}

	wantConsts := map[string]int64{
		"SourceHeader":   1,
		"SourceEndpoint": 2,
		"SourceReport":   3,
		"SourceProbe":    4,
	}
	wantTypes := map[string]bool{"Snapshot": true, "Source": true}

	seen := map[string]bool{}
	for _, name := range pkg.Scope().Names() {
		if !ast.IsExported(name) {
			continue
		}
		seen[name] = true
		switch o := pkg.Scope().Lookup(name).(type) {
		case *types.Const:
			want, ok := wantConsts[name]
			if !ok {
				t.Fatalf("unexpected exported constant %s", name)
			}
			v, ok := constant.Int64Val(o.Val())
			if !ok {
				t.Fatalf("constant %s value %s is not an int64", name, o.Val())
			}
			if v != want {
				t.Fatalf("constant %s = %d, want %d", name, v, want)
			}
		case *types.TypeName:
			if !wantTypes[name] {
				t.Fatalf("unexpected exported type %s", name)
			}
		default:
			t.Fatalf("unexpected exported identifier %s of kind %T", name, o)
		}
	}
	for name := range wantConsts {
		if !seen[name] {
			t.Fatalf("missing exported constant %s", name)
		}
	}
	for name := range wantTypes {
		if !seen[name] {
			t.Fatalf("missing exported type %s", name)
		}
	}
	if len(seen) != len(wantConsts)+len(wantTypes) {
		t.Fatalf("exported identifiers %v, want only Snapshot, Source, and the four members", seen)
	}
}
