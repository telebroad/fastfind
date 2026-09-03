package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Where the version comes from: nowhere, deliberately. There is no -ldflags
// -X incantation to remember and no generated file to forget to regenerate.
// The toolchain already records everything needed — `go install pkg@v1.2.3`
// stamps the module version, and a local `go build` inside the repo stamps the
// commit, its timestamp, and whether the tree was dirty. Reading it back is
// enough, and it cannot drift out of step with the build the way a hand-set
// constant does.
func versionString(bi *debug.BuildInfo, ok bool) string {
	if !ok {
		// Only happens for a binary built without module support at all.
		return fmt.Sprintf("fastfind (unknown)\n  %s %s/%s\n",
			runtime.Version(), runtime.GOOS, runtime.GOARCH)
	}

	// "(devel)" is what the toolchain records for anything not installed at a
	// tagged version, which is every build made straight from a checkout.
	release := bi.Main.Version
	if release == "" {
		release = "(devel)"
	}

	var revision, when string
	dirty := false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			when = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "fastfind %s", release)

	// A short hash is what anyone actually compares against a git log; the
	// full forty characters just make the line hard to read.
	if len(revision) >= 7 {
		fmt.Fprintf(&b, " %s", revision[:7])
	}
	if when != "" {
		fmt.Fprintf(&b, " %s", when)
	}
	if dirty {
		// Worth shouting about: a dirty build is not reproducible from any
		// commit, so a bug report quoting one cannot be chased to source.
		b.WriteString(" (uncommitted changes)")
	}

	fmt.Fprintf(&b, "\n  %s %s/%s\n", bi.GoVersion, runtime.GOOS, runtime.GOARCH)
	return b.String()
}

func version() string {
	return versionString(debug.ReadBuildInfo())
}
