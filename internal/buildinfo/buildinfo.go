package buildinfo

import (
	"runtime/debug"
	"strings"
)

const DevVersion = "0.0.0-dev"

var Version = DevVersion

func init() {
	info, ok := debug.ReadBuildInfo()
	Version = resolve(Version, info, ok)
}

func resolve(ldflags string, info *debug.BuildInfo, ok bool) string {
	if v := strings.TrimPrefix(ldflags, "v"); v != DevVersion {
		return v
	}
	if ok {
		if v := strings.TrimPrefix(info.Main.Version, "v"); v != "" && v != "(devel)" {
			return v
		}
	}
	return DevVersion
}
