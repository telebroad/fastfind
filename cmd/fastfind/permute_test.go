package main

import (
	"flag"
	"slices"
	"testing"
)

// A stand-in for the real flag set, carrying one of each shape that matters:
// a boolean, a string that takes a value, and an int that takes a value.
func testFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("fastfind", flag.ContinueOnError)
	fs.Bool("i", false, "ignore case")
	fs.Bool("l", false, "list files only")
	fs.String("in", "", "directory to search under")
	fs.String("g", "", "content pattern")
	fs.Int("max", 0, "stop after this many hits")
	return fs
}

func TestPermute(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "already in order is left alone",
			args: []string{"-max", "3", "*.go"},
			want: []string{"-max", "3", "--", "*.go"},
		},
		{
			// The bug this exists for: v0.1.0 searched for the name
			// "*.go -max 3" and reported nothing found.
			name: "a flag after the pattern still parses",
			args: []string{"*.go", "-max", "3"},
			want: []string{"-max", "3", "--", "*.go"},
		},
		{
			name: "a value flag keeps its value when it moves",
			args: []string{"*.go", "-in", "C:\\src"},
			want: []string{"-in", "C:\\src", "--", "*.go"},
		},
		{
			// If a boolean stole the following argument, the pattern would
			// vanish into -i and the search would have nothing to look for.
			name: "a boolean does not swallow the pattern",
			args: []string{"-i", "TODO"},
			want: []string{"-i", "--", "TODO"},
		},
		{
			name: "booleans bunched before an operand",
			args: []string{"-i", "-l", "TODO"},
			want: []string{"-i", "-l", "--", "TODO"},
		},
		{
			name: "an operand between two flags",
			args: []string{"-i", "TODO", "-max", "5"},
			want: []string{"-i", "-max", "5", "--", "TODO"},
		},
		{
			name: "an equals form carries its own value",
			args: []string{"*.go", "-max=3"},
			want: []string{"-max=3", "--", "*.go"},
		},
		{
			name: "everything after a double dash is an operand",
			args: []string{"--", "-max", "3"},
			want: []string{"--", "-max", "3"},
		},
		{
			name: "a double dash protects a pattern that looks like a flag",
			args: []string{"-i", "--", "-weird-name"},
			want: []string{"-i", "--", "-weird-name"},
		},
		{
			name: "no arguments at all",
			args: []string{},
			want: nil,
		},
		{
			name: "flags only",
			args: []string{"-i", "-max", "3"},
			want: []string{"-i", "-max", "3"},
		},
		{
			name: "operands only",
			args: []string{"*.go"},
			want: []string{"--", "*.go"},
		},
		{
			// Left for flag.Parse to reject by name rather than quietly
			// eating the argument that follows it.
			name: "an unknown flag does not take the next argument",
			args: []string{"-bogus", "*.go"},
			want: []string{"-bogus", "--", "*.go"},
		},
		{
			name: "a trailing value flag with nothing after it",
			args: []string{"*.go", "-max"},
			want: []string{"-max", "--", "*.go"},
		},
		{
			name: "two operands both survive",
			args: []string{"-g", "TODO", "a.go", "b.go"},
			want: []string{"-g", "TODO", "--", "a.go", "b.go"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := permute(tc.args, testFlags())
			if !slices.Equal(got, tc.want) {
				t.Errorf("permute(%q)\n  got  %q\n  want %q", tc.args, got, tc.want)
			}
		})
	}
}

// The point is not the ordering itself but that parsing it produces the same
// result whichever way round it was typed.
func TestPermutedParseMatchesOrdered(t *testing.T) {
	orders := [][]string{
		{"-max", "3", "-i", "*.go"},
		{"*.go", "-max", "3", "-i"},
		{"-max", "3", "*.go", "-i"},
		{"-i", "*.go", "-max", "3"},
	}

	for _, args := range orders {
		t.Run(args[0], func(t *testing.T) {
			fs := testFlags()
			max := fs.Lookup("max")
			ignore := fs.Lookup("i")

			if err := fs.Parse(permute(args, fs)); err != nil {
				t.Fatalf("parsing %q: %v", args, err)
			}

			if got := max.Value.String(); got != "3" {
				t.Errorf("parsing %q: -max is %q, want 3", args, got)
			}
			if got := ignore.Value.String(); got != "true" {
				t.Errorf("parsing %q: -i is %q, want true", args, got)
			}
			if got := fs.Args(); !slices.Equal(got, []string{"*.go"}) {
				t.Errorf("parsing %q: operands are %q, want [*.go]", args, got)
			}
		})
	}
}

func TestTakesValue(t *testing.T) {
	fs := testFlags()

	for _, arg := range []string{"-in", "-g", "-max", "--max"} {
		if !takesValue(fs, arg) {
			t.Errorf("%s takes a value, reported that it does not", arg)
		}
	}
	for _, arg := range []string{"-i", "-l", "-nosuchflag"} {
		if takesValue(fs, arg) {
			t.Errorf("%s takes no value, reported that it does", arg)
		}
	}
}
