package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChooseRootDefaultsToHere(t *testing.T) {
	root, err := chooseRoot("", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	here, _ := os.Getwd()
	if root != here {
		t.Errorf("got %q, want the working directory %q", root, here)
	}
}

func TestChooseRootTakesTheProfile(t *testing.T) {
	root, err := chooseRoot("", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	profile, _ := os.UserHomeDir()
	if root != profile {
		t.Errorf("got %q, want the user profile %q", root, profile)
	}
}

func TestChooseRootRefusesTwoStartingPoints(t *testing.T) {
	// Silently preferring one would send the search somewhere the person did
	// not ask for and looks like the search failing.
	if _, err := chooseRoot("C:\\", true); err == nil {
		t.Error("expected -in and -home together to be refused")
	}
}

func TestChooseRootMakesThePathAbsolute(t *testing.T) {
	dir := t.TempDir()

	// t.Chdir restores the previous directory when the test ends, which matters
	// on Windows: it will not delete a directory a process is sitting in, so a
	// hand-rolled chdir leaves t.TempDir's cleanup failing after the test has
	// already passed.
	t.Chdir(dir)

	if err := os.Mkdir("sub", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	root, err := chooseRoot("sub", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(root) {
		t.Errorf("got %q, want an absolute path", root)
	}
}

func TestChooseRootRefusesAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-directory.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := chooseRoot(file, false)
	if err == nil {
		t.Fatal("expected a file to be refused as a starting point")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("the error should say what is wrong, got %q", err)
	}
}

func TestChooseRootRefusesSomewhereThatIsNotThere(t *testing.T) {
	_, err := chooseRoot(filepath.Join(t.TempDir(), "nowhere"), false)
	if err == nil {
		t.Error("expected a missing directory to be refused")
	}
}

func TestFormatCountReadsLikeAPersonWouldSayIt(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{
		{42, "42 entries"},
		{1_500, "1.5k entries"},
		{1_900_000, "1.9M entries"},
	} {
		if got := formatCount(c.in); got != c.want {
			t.Errorf("formatCount(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPlural(t *testing.T) {
	if got := plural(1, "match", "matches"); got != "match" {
		t.Errorf("got %q, want match", got)
	}
	for _, n := range []int{0, 2, 99} {
		if got := plural(n, "match", "matches"); got != "matches" {
			t.Errorf("plural(%d) = %q, want matches", n, got)
		}
	}
}
