package walk

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// Walking a subtree, in parallel, without the traps that make `find` crawl.
//
// Reading the MFT (see ntfs.go) is the right answer when the question really is
// "somewhere on this 1.8TB drive". It is the wrong answer for the question
// actually asked most of the time, which is "somewhere under my code folder" —
// there it needs an elevated prompt and reads a few hundred megabytes of table
// to answer something a scoped walk answers in milliseconds.
//
// Three things separate this from `find`, and only the first is about speed:
//
//   - It walks directories CONCURRENTLY. A tree walk is latency-bound, not
//     CPU-bound: most of the time is spent waiting on a directory read. One
//     goroutine per directory keeps the queue full.
//   - It does not follow REPARSE POINTS. Junctions, symlinks and the virtual
//     filesystems that Google Drive and OneDrive project are all reparse points,
//     and following them is how a walk ends up somewhere it can never finish —
//     or in a cycle it can never leave.
//   - It skips the directories that hold most of the files and almost none of
//     the answers. On a dev machine node_modules alone is usually a clear
//     majority of every file present.

// Skipped is the set of directory names passed over unless -all is given.
//
// Each of these is somewhere that holds an enormous number of files that are
// almost never what is being looked for. They are matched by name at any depth
// rather than by path, because they turn up at every depth.
var Skipped = map[string]bool{
	"node_modules":              true,
	".git":                      true,
	".angular":                  true,
	".next":                     true,
	".nuxt":                     true,
	".gradle":                   true,
	".venv":                     true,
	"venv":                      true,
	"__pycache__":               true,
	"vendor":                    true,
	"target":                    true,
	"dist":                      true,
	"build":                     true,
	"$Recycle.Bin":              true,
	"System Volume Information": true,
	"Windows":                   true,
	"WinSxS":                    true,
}

// Query is one search.
type Query struct {
	// Root is where the walk starts.
	Root string
	// Pattern is matched against the base name, case-insensitively.
	Pattern string
	// Glob is true when Pattern holds * or ?, in which case it is matched as a
	// pattern rather than as a substring.
	Glob bool
	// OnlyDirs and OnlyFiles narrow what counts as a hit.
	OnlyDirs  bool
	OnlyFiles bool
	// All disables the skip list.
	All bool
	// Hidden includes dot-files and dot-directories.
	//
	// Unlike fd, these are searched BY DEFAULT: on a developer's machine the
	// dot-directories are where the configuration lives, and .env, .gitignore
	// and .npmrc are things people go looking for. The flag exists for the
	// opposite preference, and for parity with tools that default the other way.
	Hidden bool
	// Limit stops the walk after this many hits; zero means no limit.
	Limit int
	// Cores caps how much of the machine the search may use. Zero means the
	// default, which is every core but one. See Cores.
	Cores int
}

// Cores settles how many cores a search may use.
//
// The default deliberately leaves one alone. A file search is the sort of thing
// somebody runs while doing something else, and a tool that takes the whole
// machine to save a second is a bad trade — the editor stutters, the build
// crawls, and the search was going to finish either way. One core in reserve
// costs very little: the walk is latency-bound, so the cores are mostly idle
// waiting on the disk regardless.
//
// Below one is meaningless, so it clamps.
func Cores(requested int) int {
	if requested > 0 {
		return requested
	}
	if spare := runtime.NumCPU() - 1; spare > 0 {
		return spare
	}
	return 1
}

// Result is one match.
type Result struct {
	Path  string
	IsDir bool
	Size  int64
}

// matches decides whether one name is a hit.
func (q *Query) matches(name string) bool {
	if q.Pattern == "" {
		return true
	}
	lower := strings.ToLower(name)
	if q.Glob {
		// A bad pattern matches nothing rather than erroring out mid-walk: the
		// pattern was already checked when the query was built.
		ok, _ := filepath.Match(strings.ToLower(q.Pattern), lower)
		return ok
	}
	return strings.Contains(lower, strings.ToLower(q.Pattern))
}

// skip decides whether to descend into a directory.
func (q *Query) skip(name string) bool {
	if q.All {
		return false
	}
	return Skipped[name]
}

// Walk searches the tree under q.Root, calling emit for every match.
//
// emit is called from several goroutines at once, so it must be safe to call
// concurrently; the caller here serialises through a channel. The walk stops
// early once Limit hits have been emitted — searching the rest of a drive to
// find a fiftieth copy of a file is work nobody asked for.
func Walk(q Query, emit func(Result)) (scanned int64) {
	// Far more workers than cores, on purpose.
	//
	// A tree walk is LATENCY-bound, not CPU-bound: a worker spends almost all of
	// its life blocked inside a directory read, using no core at all. Sizing the
	// pool to the core count — the reflex for compute — leaves the disk mostly
	// idle waiting for the few workers that happen to be runnable.
	// Oversubscribing keeps enough reads outstanding that the queue stays full,
	// which is exactly what goroutines are cheap enough to allow: a few hundred
	// blocked ones cost a few hundred kilobytes of stack and nothing else.
	//
	// The multiplier is applied to whatever the search was allowed, so -cores 1
	// really does stay small rather than quietly running 128 workers on one
	// core.
	cores := Cores(q.Cores)
	workers := cores * 8
	if workers > 256 {
		workers = 256
	}

	var (
		queue   = make(chan string, 4096)
		pending sync.WaitGroup
		found   atomic.Int64
		files   atomic.Int64
		stop    = make(chan struct{})
		once    sync.Once
	)

	halt := func() { once.Do(func() { close(stop) }) }

	// The queue is buffered but a deep tree can still outrun it, so a send that
	// would block is handled by walking that directory inline instead. Without
	// this a full queue deadlocks: every worker is blocked trying to enqueue and
	// nobody is left to drain.
	var push func(dir string)
	push = func(dir string) {
		pending.Add(1)
		select {
		case queue <- dir:
		default:
			pending.Done()
			walkOne(q, dir, push, emit, &found, &files, halt, stop)
		}
	}

	var workGroup sync.WaitGroup
	for i := 0; i < workers; i++ {
		workGroup.Add(1)
		go func() {
			defer workGroup.Done()
			for dir := range queue {
				walkOne(q, dir, push, emit, &found, &files, halt, stop)
				pending.Done()
			}
		}()
	}

	push(q.Root)

	pending.Wait()
	close(queue)
	workGroup.Wait()

	return files.Load()
}

// walkOne reads a single directory and queues its subdirectories.
func walkOne(
	q Query,
	dir string,
	push func(string),
	emit func(Result),
	found, files *atomic.Int64,
	halt func(),
	stop <-chan struct{},
) {
	select {
	case <-stop:
		return
	default:
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// A directory that cannot be read is not worth reporting: on Windows a
		// walk crosses plenty of them — locked profiles, "Application Data"
		// junctions left for XP compatibility — and a line of noise each is
		// worse than silence.
		return
	}

	for _, entry := range entries {
		select {
		case <-stop:
			return
		default:
		}

		name := entry.Name()
		full := filepath.Join(dir, name)
		isDir := entry.IsDir()

		files.Add(1)

		if q.matches(name) && !(q.OnlyDirs && !isDir) && !(q.OnlyFiles && isDir) {
			emit(Result{Path: full, IsDir: isDir})

			if q.Limit > 0 && int(found.Add(1)) >= q.Limit {
				halt()
				return
			}
		}

		if !isDir || q.skip(name) {
			continue
		}

		if !q.Hidden && len(name) > 1 && name[0] == '.' {
			// Only for descending. A dot-file that MATCHES is still reported —
			// somebody searching for ".env" means it — but walking into every
			// dot-directory is what makes a home directory slow.
			continue
		}

		// The one check that keeps this from wandering off the volume, read from
		// the attributes the directory scan already returned rather than from a
		// fresh look at the file. See isReparse.
		if info, err := entry.Info(); err != nil || isReparse(info) {
			continue
		}

		push(full)
	}
}
