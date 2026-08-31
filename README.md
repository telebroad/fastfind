# fastfind

Find files by name, or search inside them, without waiting. Installs as `ff`.

```
ff cache.go                       find by name, under the current directory
ff -g 'func main' -name '*.go'    search file contents, like grep -r
ff -home '*.pem'                  the whole user profile
```

Measured against GNU `find` on the same tree, returning the same 165 results:

| | `find` | `ff` |
|---|---|---|
| A Go module cache | 11.8s | **1.1s** |
| A 1.9M-entry user profile | — | **4.2s** (~450k entries/sec) |
| One file in a mid-sized repo | — | **15ms** |

## Install

```bash
go install github.com/telebroad/fastfind/cmd/ff@latest
```

Windows, Linux and macOS. Tested on all three in CI.

## Why it's faster

Barely any of it is a cleverer search. It's three things `find` doesn't do.

**It reads directories concurrently.** A tree walk is *latency*-bound, not
CPU-bound: a worker spends nearly all its life blocked inside a directory read,
using no core at all. Sizing a worker pool to the core count — the reflex for
compute work — leaves the disk idle waiting on the few workers that happen to be
runnable. `ff` runs 8 workers per allowed core, because goroutines are cheap
enough that a couple of hundred blocked ones cost a few hundred kilobytes of
stack and nothing else.

**It never follows a reparse point.** Junctions, symlinks, and the virtual trees
that Google Drive, OneDrive and Dropbox project are all reparse points. Windows
leaves junctions all over a user profile — `Application Data` and `My Documents`
point back at their own parents — so a walk that follows them never terminates.
Worse, descending into a cloud provider's projection asks that service to
*materialise every file it is holding remotely*, which is how a search for a
source file ends up pulling gigabytes over the network.

**It skips what holds most of the files and none of the answers.** On a
development machine `node_modules` alone is usually a clear majority of every
file present.

The single biggest win was none of those three, though. The reparse check reads
the attribute word that `FindNextFile` **already returned** — Go keeps it on the
`DirEntry`, so `entry.Info()` costs nothing — rather than asking Windows again
with `GetFileAttributes`. Deleting that one redundant syscall per directory took
a user-profile scan from **36.6s to 4.2s**.

## Searching inside files

`-g` switches from names to contents, and from there the flags mean what they
mean in `grep(1)`:

```bash
ff -g 'func main' -name '*.go'    # only inside Go files
ff -g TODO -i -n                  # ignore case, show line numbers
ff -g panic -l                    # just the files that panic
ff -g 'err != nil' -c             # count matching lines per file
ff -g Deprecated -C 2             # two lines of context either side
ff -g 'computed(' -F              # a literal, not a regex
```

The grep runs *inside* the walk, on the goroutine that found each file, so the
content search finishes about when the walk does rather than starting then.

Two things it does that `grep` on Windows doesn't:

- **It reads UTF-16.** PowerShell redirection, Notepad and much of Windows still
  write UTF-16LE with a BOM. Every other byte is NUL, so a byte-oriented search
  calls the whole file binary — `grep` reports "binary file matches" and stops.
  This decodes it and searches the text.
- **When your pattern isn't valid regex, it says what to do.** Searching for
  `computed(` or `foo[0]` is the most common thing anyone types, and every one of
  those is a broken regex. The error names the fix rather than just the fault.

## All the options

```
Where to look
  -in <dir>     start here (default: the current directory)
  -home         start at the user profile
  -all          do not skip node_modules, .git, Windows and the rest
  -hidden       descend into dot-directories too
  -cores <n>    cores to use (default: every core but one, minimum 1)

Finding by name (the default)
  -d            directories only
  -f            files only
  -max <n>      stop after this many hits

Searching contents
  -g <regex>    the pattern to search for inside files
  -name <glob>  only look inside files whose name matches
  -i -w -v -F   ignore case / whole words / invert / literal
  -l -c -n      files only / count per file / line numbers
  -A -B -C <n>  context after / before / either side

Output
  -abs          absolute paths rather than relative to the root
  -0            NUL-separated, for xargs -0
  -q            no summary line
  -color <when> always | never | auto (default: auto)
```

A **name** pattern matches anywhere in the name and ignores case; give it `*` or
`?` and it's a glob against the whole name. A **content** pattern is a regex and
is case-*sensitive* by default — that's grep's rule, and muscle memory is worth
more than internal consistency.

Skipped unless `-all`: `node_modules`, `.git`, `.angular`, `.next`, `.nuxt`,
`.gradle`, `.venv`, `venv`, `__pycache__`, `vendor`, `target`, `dist`, `build`,
`Windows`, `WinSxS`, `$Recycle.Bin`, `System Volume Information`.

Exit status follows grep — 0 found, 1 not found, 2 bad usage — so it composes:

```bash
ff -q -f '*.pem' && echo "keys present"
ff -0 -q '*.tmp' | xargs -0 rm
```

## Politeness

The default is **every core but one**. A file search is something you run while
doing something else, and taking the whole machine to save a second is a bad
trade — the editor stutters, the build crawls, and the search was going to finish
either way. One core in reserve costs almost nothing here: the walk is
latency-bound, so the cores sit idle waiting on the disk regardless. `-cores n`
sets it explicitly, and it caps `GOMAXPROCS` rather than only the worker count,
so `-cores 2` really does stay out of the way.

## The MFT reader

`internal/ntfs` holds a direct reader for the NTFS Master File Table — the trick
WizFile and Everything use. NTFS keeps one flat table with a ~1KB record per file
on the volume, so a whole-drive answer is a single sequential read rather than
millions of directory calls.

It's deliberately **not** the default. Raw volume access (`\\.\C:`) needs an
elevated prompt, and it reads hundreds of megabytes of table to answer a question
a scoped walk answers in milliseconds. Most of the time you don't want the whole
system — you want your own directory.

Three details there produce plausible-looking numbers rather than errors when you
get them wrong, so each has a test:

- Data-run offsets are **signed** and **relative to the previous run**. A later
  run can sit earlier on the disk.
- The boot sector's `clustersPerRecord` is a **signed** byte: −10 means 2¹⁰ =
  1024 bytes. Read unsigned it's 246, and every offset after it is wrong.
- Every record needs its **fixups** applied — NTFS overwrites the last two bytes
  of each sector with a torn-write counter and keeps the originals in an array at
  the top of the record. Skip that and attribute lengths read as sequence numbers.

## Portability

The walker builds and runs on Windows, Linux and macOS; the reparse-point
handling is Windows-specific and falls back to Go's own symlink check elsewhere.
The MFT reader is Windows-and-NTFS only, by nature. CI runs the suite on all
three.

## Tests

```bash
go test ./...
go test -race ./...
```

## Licence

MIT — see [LICENSE](LICENSE).
