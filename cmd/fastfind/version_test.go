package main

import (
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// versionString is split out from version() precisely so the BuildInfo can be
// synthesised here — the real one describes whatever built the test binary,
// which is different on every machine and cannot be asserted against.
func TestVersionString(t *testing.T) {
	settings := func(rev, when, modified string) []debug.BuildSetting {
		return []debug.BuildSetting{
			{Key: "vcs.revision", Value: rev},
			{Key: "vcs.time", Value: when},
			{Key: "vcs.modified", Value: modified},
		}
	}

	tests := []struct {
		name     string
		bi       *debug.BuildInfo
		ok       bool
		contains []string
		absent   []string
	}{
		{
			name: "installed at a tagged version",
			bi: &debug.BuildInfo{
				Main:      debug.Module{Version: "v1.0.0"},
				GoVersion: "go1.26.0",
			},
			ok:       true,
			contains: []string{"fastfind v1.0.0", "go1.26.0"},
			absent:   []string{"uncommitted"},
		},
		{
			name: "built from a clean checkout",
			bi: &debug.BuildInfo{
				Main:      debug.Module{Version: "(devel)"},
				GoVersion: "go1.26.0",
				Settings:  settings("5c670197c5f4aa11bb22cc33dd44ee55ff667788", "2026-09-03T19:22:29Z", "false"),
			},
			ok: true,
			// The hash is shortened to the seven characters a git log shows.
			contains: []string{"(devel)", "5c67019", "2026-09-03T19:22:29Z"},
			absent:   []string{"uncommitted", "5c670197c5f4"},
		},
		{
			name: "built from a dirty checkout",
			bi: &debug.BuildInfo{
				Main:      debug.Module{Version: "(devel)"},
				GoVersion: "go1.26.0",
				Settings:  settings("5c670197c5f4aa11bb22cc33dd44ee55ff667788", "2026-09-03T19:22:29Z", "true"),
			},
			ok:       true,
			contains: []string{"(devel)", "5c67019", "uncommitted changes"},
		},
		{
			name:     "no build info at all",
			bi:       nil,
			ok:       false,
			contains: []string{"fastfind (unknown)", runtime.GOOS},
		},
		{
			name: "empty version reads as devel rather than blank",
			bi: &debug.BuildInfo{
				Main:      debug.Module{Version: ""},
				GoVersion: "go1.26.0",
			},
			ok:       true,
			contains: []string{"fastfind (devel)"},
		},
		{
			name: "a revision too short to shorten is left off",
			bi: &debug.BuildInfo{
				Main:      debug.Module{Version: "v1.0.0"},
				GoVersion: "go1.26.0",
				Settings:  []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}},
			},
			ok:       true,
			contains: []string{"fastfind v1.0.0"},
			absent:   []string{"abc"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := versionString(tc.bi, tc.ok)

			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in output, got:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(got, unwanted) {
					t.Errorf("did not expect %q in output, got:\n%s", unwanted, got)
				}
			}

			// Every form ends with a newline, so shell output does not run on.
			if !strings.HasSuffix(got, "\n") {
				t.Errorf("output should end with a newline, got: %q", got)
			}
			// The platform belongs on every line of it — a bug report that does
			// not say which OS the binary was for wastes a round trip.
			if !strings.Contains(got, runtime.GOARCH) {
				t.Errorf("expected the architecture in output, got:\n%s", got)
			}
		})
	}
}

// The real entry point has to work too, whatever it happens to report here.
func TestVersionDoesNotPanic(t *testing.T) {
	got := version()
	if !strings.HasPrefix(got, "fastfind ") {
		t.Errorf("expected output to start with the program name, got: %q", got)
	}
}
