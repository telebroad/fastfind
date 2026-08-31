package walk

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// tree builds a small directory tree and returns its root.
//
// Every path is given relative to the root, with a trailing slash meaning a
// directory. Files get a byte of content so a size is something rather than
// nothing.
func tree(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()

	for _, path := range paths {
		full := filepath.Join(root, filepath.FromSlash(path))
		if path[len(path)-1] == '/' {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", full, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return root
}

// gather runs a walk and returns the hits as root-relative slash paths, sorted.
//
// The emit callback is called from many goroutines at once, so it locks: a test
// that races here would fail somewhere else entirely and look like a bug in the
// walker.
func gather(t *testing.T, q Query) []string {
	t.Helper()

	var (
		mu   sync.Mutex
		hits []string
	)
	Walk(q, func(r Result) {
		mu.Lock()
		defer mu.Unlock()
		rel, err := filepath.Rel(q.Root, r.Path)
		if err != nil {
			rel = r.Path
		}
		hits = append(hits, filepath.ToSlash(rel))
	})
	slices.Sort(hits)
	return hits
}

func TestMatchesIsSubstringAndCaseInsensitive(t *testing.T) {
	q := Query{Pattern: "Cache"}

	for _, name := range []string{"cache.go", "ipInfoCache.go", "CACHE"} {
		if !q.matches(name) {
			t.Errorf("expected %q to match the substring %q", name, q.Pattern)
		}
	}
	if q.matches("readme.md") {
		t.Error("expected readme.md not to match")
	}
}

func TestMatchesAsGlobWhenAsked(t *testing.T) {
	q := Query{Pattern: "*.pem", Glob: true}

	if !q.matches("server.PEM") {
		t.Error("a glob should ignore case like the substring match does")
	}
	// The whole name has to match a glob, which is the difference from the
	// substring default: *.pem does not match a file merely containing ".pem".
	if q.matches("server.pem.bak") {
		t.Error("expected *.pem not to match server.pem.bak")
	}
}

func TestEmptyPatternMatchesEverything(t *testing.T) {
	q := Query{}
	if !q.matches("anything at all") {
		t.Error("an empty pattern is a listing, not a filter")
	}
}

func TestWalkFindsNestedFiles(t *testing.T) {
	root := tree(t, "a/b/c/cache.go", "a/other.go", "top.go")

	hits := gather(t, Query{Root: root, Pattern: "cache.go"})

	if len(hits) != 1 || hits[0] != "a/b/c/cache.go" {
		t.Errorf("got %v, want [a/b/c/cache.go]", hits)
	}
}

func TestWalkSkipsTheHeavyDirectoriesByDefault(t *testing.T) {
	root := tree(t,
		"src/cache.go",
		"node_modules/pkg/cache.go",
		".git/objects/cache.go",
		"api/vendor/lib/cache.go",
	)

	hits := gather(t, Query{Root: root, Pattern: "cache.go"})

	// These three hold most of the files on a dev machine and almost none of
	// the answers, which is the whole reason the default exists.
	if len(hits) != 1 || hits[0] != "src/cache.go" {
		t.Errorf("got %v, want only src/cache.go", hits)
	}
}

func TestWalkSearchesEverythingWhenAllIsSet(t *testing.T) {
	root := tree(t, "src/cache.go", "node_modules/pkg/cache.go", "api/vendor/lib/cache.go")

	hits := gather(t, Query{Root: root, Pattern: "cache.go", All: true})

	if len(hits) != 3 {
		t.Errorf("got %d hits, want 3: %v", len(hits), hits)
	}
}

func TestWalkCanNarrowToFilesOrDirectories(t *testing.T) {
	root := tree(t, "build/keep.txt", "build.go")

	dirs := gather(t, Query{Root: root, Pattern: "build", OnlyDirs: true})
	if len(dirs) != 1 || dirs[0] != "build" {
		t.Errorf("directories: got %v, want [build]", dirs)
	}

	files := gather(t, Query{Root: root, Pattern: "build", OnlyFiles: true})
	if len(files) != 1 || files[0] != "build.go" {
		t.Errorf("files: got %v, want [build.go]", files)
	}
}

func TestWalkStopsAtTheLimit(t *testing.T) {
	root := tree(t,
		"a/hit.go", "b/hit.go", "c/hit.go", "d/hit.go", "e/hit.go",
		"f/hit.go", "g/hit.go", "h/hit.go",
	)

	hits := gather(t, Query{Root: root, Pattern: "hit.go", Limit: 3})

	// The walk runs on many goroutines, so a few extra can be emitted between
	// one worker hitting the limit and the others noticing. What must hold is
	// that it stopped rather than finishing the tree.
	if len(hits) < 3 {
		t.Errorf("got %d hits, want at least the limit of 3", len(hits))
	}
	if len(hits) == 8 {
		t.Error("the limit did not stop the walk: every file was returned")
	}
}

func TestWalkCountsWhatItScanned(t *testing.T) {
	root := tree(t, "a/one.go", "a/two.go", "b/three.go")

	// Three files plus the two directories holding them.
	if scanned := Walk(Query{Root: root, Pattern: "nothing"}, func(Result) {}); scanned != 5 {
		t.Errorf("scanned %d entries, want 5", scanned)
	}
}

func TestWalkSurvivesADirectoryItCannotRead(t *testing.T) {
	root := tree(t, "readable/found.go")
	missing := filepath.Join(root, "gone")

	// A path that vanishes between the listing and the read is ordinary on a
	// live filesystem; the walk reports what it can rather than failing.
	hits := gather(t, Query{Root: missing, Pattern: "found.go"})
	if len(hits) != 0 {
		t.Errorf("a missing root should yield nothing, got %v", hits)
	}

	hits = gather(t, Query{Root: root, Pattern: "found.go"})
	if len(hits) != 1 {
		t.Errorf("got %v, want the one readable hit", hits)
	}
}

func TestWalkHandlesATreeDeeperThanTheQueue(t *testing.T) {
	// The queue is buffered, and a deep tree can outrun it. A send that would
	// block walks the directory inline instead; without that the workers all
	// block trying to enqueue and nobody is left to drain.
	var paths []string
	deep := ""
	for range 200 {
		deep = filepath.ToSlash(filepath.Join(deep, "d"))
		paths = append(paths, deep+"/f.go")
	}
	root := tree(t, paths...)

	hits := gather(t, Query{Root: root, Pattern: "f.go"})

	if len(hits) != 200 {
		t.Errorf("got %d hits down a 200-deep tree, want 200", len(hits))
	}
}
