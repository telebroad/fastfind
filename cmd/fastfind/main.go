// Command fastfind searches for files by name, or inside them by content.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/telebroad/fastfind/internal/grep"
	"github.com/telebroad/fastfind/internal/walk"
)

const usage = `fastfind — find files, and search inside them

  fastfind <pattern>                 find by name, under the current directory
  fastfind -g <regex>                search file CONTENTS, like grep -r
  fastfind -g <regex> -name '*.go'   ...only inside files whose name matches

Where to look
  -in <dir>     start here (default: the current directory)
  -home         start at the user profile
  -all          do not skip node_modules, .git, Windows and the rest
  -hidden       include dot-files and dot-directories
  -cores <n>    cores to use (default: every core but one, minimum 1)

Finding by name (the default)
  The pattern matches anywhere in the name and ignores case. Give it * or ?
  and it is matched as a glob against the whole name instead.

  -d            directories only
  -f            files only
  -max <n>      stop after this many hits

Searching contents — these flags mean what they mean in grep(1)
  -g <regex>    the pattern to search for inside files
  -name <glob>  only look inside files whose name matches
  -i            ignore case
  -w            whole words only
  -v            lines that do NOT match
  -F            treat the pattern literally, not as a regex
  -l            list the files with matches, nothing else
  -c            count matching lines per file
  -n            show line numbers
  -A <n>        also print n lines after each match
  -B <n>        also print n lines before each match
  -C <n>        both, n lines either side

Config files, without printing what is in them
  -json         describe JSON files: every key path, no values
  -get <path>   print ONE value, by dotted path: -get db.host config.json
  -mask         in -g output, hide the values and keep the keys

  A value is shown only when it cannot carry a secret — a bool, a null.
  Everything else becomes its type and its length, and anything under a key
  named like a credential is refused outright. Made for reading a config in
  front of somebody, or in a session whose transcript is stored.

Output
  -abs          absolute paths rather than paths relative to the root
  -0            NUL-separated, for xargs -0
  -q            paths only, no summary line
  -color <when> always | never | auto (default: auto — on when a terminal)
  -version      which build this is, and what it was built from

Skipped unless -all: node_modules, .git, .angular, .next, .nuxt, .gradle,
.venv, venv, __pycache__, vendor, target, dist, build, Windows, WinSxS,
$Recycle.Bin, System Volume Information.

Never followed, ever: junctions, symlinks, and the virtual filesystems that
Google Drive and OneDrive project. Following those is how a search never
returns.

Examples
  fastfind cache.go                       this file, somewhere below here
  fastfind -home '*.pem'                  every key in the profile
  fastfind -g 'func main' -name '*.go'    where main is defined
  fastfind -g TODO -i -n                  every TODO, with line numbers
  fastfind -g panic -l                    just the files that panic
  fastfind -g 'err != nil' -c             how often each file checks an error
  fastfind -cores 2 -g X                  leave the rest of the machine alone
  fastfind -json config.json              what keys are in here? (no values)
  fastfind -json '*.json'                 ...across every JSON file below here
  fastfind -get database.host cfg.json    one value, deliberately
  fastfind -g PASSWORD -mask -name '.env' find the key, not the secret
`

// Exit codes follow grep: 0 found, 1 not found, 2 something was wrong with the
// asking. That is what makes `fastfind -q x && echo yes` work in a shell.
const (
	exitFound    = 0
	exitNotFound = 1
	exitUsage    = 2
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		in     = flag.String("in", "", "directory to search under")
		home   = flag.Bool("home", false, "search the user profile")
		all    = flag.Bool("all", false, "do not skip anything")
		hidden = flag.Bool("hidden", false, "include dot-files")
		cores  = flag.Int("cores", 0, "cores to use")

		dirs  = flag.Bool("d", false, "directories only")
		files = flag.Bool("f", false, "files only")
		max   = flag.Int("max", 0, "stop after this many hits")

		pattern    = flag.String("g", "", "search file contents for this")
		nameGlob   = flag.String("name", "", "only look inside files matching this")
		ignoreCase = flag.Bool("i", false, "ignore case")
		word       = flag.Bool("w", false, "whole words only")
		invert     = flag.Bool("v", false, "lines that do not match")
		fixed      = flag.Bool("F", false, "treat the pattern literally")
		listOnly   = flag.Bool("l", false, "list files with matches")
		countOnly  = flag.Bool("c", false, "count matching lines per file")
		numbers    = flag.Bool("n", false, "show line numbers")
		after      = flag.Int("A", 0, "lines after each match")
		before     = flag.Int("B", 0, "lines before each match")
		around     = flag.Int("C", 0, "lines either side of each match")

		asJSON  = flag.Bool("json", false, "describe JSON files without printing their values")
		getPath = flag.String("get", "", "print one value from a JSON file, by dotted path")
		mask    = flag.Bool("mask", false, "hide values in matched lines")

		abs   = flag.Bool("abs", false, "absolute paths")
		zero  = flag.Bool("0", false, "NUL-separated")
		quiet = flag.Bool("q", false, "paths only, no summary line")
		color = flag.String("color", "auto", "always | never | auto")

		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.CommandLine.Parse(permute(os.Args[1:], flag.CommandLine))

	// Answered before anything else can fail. "What am I running?" is usually
	// asked *because* something else has already gone wrong, so it must not
	// depend on the rest of the arguments making sense.
	if *showVersion {
		fmt.Print(version())
		return exitFound
	}

	root, err := chooseRoot(*in, *home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fastfind:", err)
		return exitUsage
	}

	// The cap is honoured for real, not merely in the worker count. Without
	// setting GOMAXPROCS the runtime still schedules across every core, and a
	// `-cores 2` search still warms the whole machine.
	allowed := walk.Cores(*cores)
	runtime.GOMAXPROCS(allowed)

	// -C is shorthand for both sides, but an explicit -A or -B wins over it.
	if *around > 0 {
		if *before == 0 {
			*before = *around
		}
		if *after == 0 {
			*after = *around
		}
	}

	searching := *pattern != ""
	loose := strings.Join(flag.Args(), " ")

	// `fastfind -g foo '*.go'` is a reasonable thing to type. Take the loose argument
	// as the name filter rather than ignoring it in silence.
	if searching && loose != "" && *nameGlob == "" {
		*nameGlob = loose
	}
	if *asJSON && loose == "" {
		loose = "*.json"
	}
	if !searching && !*asJSON && loose == "" {
		flag.Usage()
		return exitUsage
	}

	namePattern := loose
	if searching {
		namePattern = *nameGlob
	}

	query := walk.Query{
		Root:      root,
		Pattern:   namePattern,
		Glob:      strings.ContainsAny(namePattern, "*?["),
		OnlyDirs:  *dirs,
		OnlyFiles: *files || searching,
		All:       *all,
		Hidden:    *hidden,
		Limit:     *max,
		Cores:     allowed,
	}

	if query.Glob {
		if _, err := filepath.Match(namePattern, "probe"); err != nil {
			fmt.Fprintf(os.Stderr, "fastfind: %q is not a valid pattern: %v\n", namePattern, err)
			return exitUsage
		}
	}

	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()

	show := &printer{
		out:     out,
		root:    root,
		abs:     *abs,
		zero:    *zero,
		numbers: *numbers,
		mask:    *mask,
		color:   useColor(*color),
	}

	started := time.Now()

	// Both forms, because English does not pluralise by adding an s: "match"
	// becomes "matches", and deriving it gave "5 matchs".
	var (
		matched  int
		scanned  int64
		one      = "match"
		multiple = "matches"
	)

	// -get is the deliberate escape hatch and takes one file, so it is settled
	// before anything walks.
	if *getPath != "" {
		return showValue(loose, *getPath, out)
	}

	if *asJSON {
		matched, scanned = describeJSON(query, show)
		one, multiple = "key", "keys"
	} else if searching {
		matcher, err := grep.Compile(grep.Options{
			Pattern:    *pattern,
			Fixed:      *fixed,
			IgnoreCase: *ignoreCase,
			Word:       *word,
			Invert:     *invert,
			Before:     *before,
			After:      *after,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "fastfind:", err)
			return exitUsage
		}
		matched, scanned = searchContents(query, matcher, show, *listOnly, *countOnly)
		one, multiple = "line", "lines"
		if *listOnly || *countOnly {
			one, multiple = "file", "files"
		}
	} else {
		matched, scanned = searchNames(query, show)
	}

	if !*quiet {
		out.Flush()
		fmt.Fprintf(os.Stderr, "\n%d %s in %s — %s scanned on %d %s\n",
			matched, plural(matched, one, multiple),
			time.Since(started).Round(time.Millisecond),
			formatCount(scanned), allowed, plural(allowed, "core", "cores"))
	}

	if matched == 0 {
		return exitNotFound
	}
	return exitFound
}

// searchNames walks and prints paths.
func searchNames(query walk.Query, show *printer) (matched int, scanned int64) {
	results := make(chan walk.Result, 1024)
	done := make(chan int)

	go func() {
		count := 0
		for hit := range results {
			show.path(hit.Path)
			count++
		}
		done <- count
	}()

	scanned = walk.Walk(query, func(hit walk.Result) { results <- hit })
	close(results)
	return <-done, scanned
}

// searchContents walks, and greps each file on the goroutine that found it.
//
// The grep runs INSIDE the walk rather than after it, so the content search
// finishes about when the walk does instead of starting then. Printing stays on
// one goroutine: os.Stdout is not safe to write from several at once, and
// interleaved writes splice two files' lines together in a way that looks like a
// bug in the search rather than in the printing.
func searchContents(
	query walk.Query,
	matcher *grep.Matcher,
	show *printer,
	listOnly, countOnly bool,
) (matched int, scanned int64) {
	results := make(chan *grep.FileResult, 256)
	done := make(chan int)

	go func() {
		count := 0
		for found := range results {
			switch {
			case listOnly:
				show.path(found.Path)
				count++
			case countOnly:
				show.count(found.Path, found.Count)
				count++
			default:
				show.block(found)
				count += found.Count
			}
		}
		done <- count
	}()

	scanned = walk.Walk(query, func(hit walk.Result) {
		if hit.IsDir {
			return
		}
		found, err := matcher.Search(hit.Path)
		if err != nil || found == nil {
			// Unreadable or binary. grep skips both without comment, and on a
			// Windows walk a line of noise per locked file is worse than none.
			return
		}
		results <- found
	})

	close(results)
	return <-done, scanned
}

// chooseRoot settles where the walk starts.
func chooseRoot(in string, home bool) (string, error) {
	switch {
	case in != "" && home:
		return "", fmt.Errorf("-in and -home both name a starting point; pick one")
	case home:
		profile, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding the user profile: %w", err)
		}
		return profile, nil
	case in != "":
		full, err := filepath.Abs(in)
		if err != nil {
			return "", fmt.Errorf("resolving %q: %w", in, err)
		}
		info, err := os.Stat(full)
		if err != nil {
			return "", fmt.Errorf("%s: %w", full, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s is a file, not a directory", full)
		}
		return full, nil
	default:
		return os.Getwd()
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// formatCount writes a count the way a person reads one.
func formatCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM entries", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk entries", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d entries", n)
	}
}
