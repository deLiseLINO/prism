package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolveStripsLeadingV(t *testing.T) {
	built := &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}
	cases := []struct {
		name    string
		ldflags string
		info    *debug.BuildInfo
		want    string
	}{
		{"ldflags with v", "v1.2.3", nil, "1.2.3"},
		{"ldflags without v", "1.2.3", nil, "1.2.3"},
		{"build info with v", DevVersion, built, "1.2.3"},
		{"build info devel", DevVersion, &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, DevVersion},
		{"no build info", DevVersion, nil, DevVersion},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolve(c.ldflags, c.info, c.info != nil); got != c.want {
				t.Fatalf("resolve = %q, want %q", got, c.want)
			}
		})
	}
	if resolve("v1.2.3", nil, false) != resolve(DevVersion, built, true) {
		t.Fatal("ldflags and build info builds of one tag must compare equal")
	}
}
