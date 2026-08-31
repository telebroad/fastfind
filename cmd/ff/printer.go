package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/telebroad/fastfind/internal/grep"
	"github.com/telebroad/fastfind/internal/redact"
)

// ANSI, kept to the four colours a 1980s terminal had, because those are the
// ones every terminal since has agreed on. Anything fancier renders as garbage
// somewhere.
const (
	ansiReset   = "\x1b[0m"
	ansiPath    = "\x1b[35m"   // magenta, as grep and ripgrep colour a filename
	ansiLineNum = "\x1b[32m"   // green
	ansiMatch   = "\x1b[1;31m" // bold red
	ansiSep     = "\x1b[36m"   // cyan
)

// printer writes results. One instance, used from ONE goroutine.
type printer struct {
	out     io.Writer
	root    string
	abs     bool
	zero    bool
	numbers bool
	mask    bool
	color   bool
}

// useColor decides whether to emit ANSI escapes.
//
// "auto" means: only when stdout is a terminal. Piping into a file or another
// program must produce clean text — colour codes in a pipeline end up in the
// data, and a grep of a grep then fails to match because the word it wants has
// an escape sequence in the middle of it.
//
// NO_COLOR is honoured because it costs nothing and people who set it mean it.
func useColor(when string) bool {
	switch when {
	case "always":
		return true
	case "never":
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// display turns an absolute path into what the reader should see.
func (p *printer) display(path string) string {
	if p.abs {
		return path
	}
	if rel, err := filepath.Rel(p.root, path); err == nil {
		return rel
	}
	return path
}

// path prints one file name on its own, for a name search or -l.
func (p *printer) path(full string) {
	shown := p.display(full)
	if p.zero {
		fmt.Fprintf(p.out, "%s\x00", shown)
		return
	}
	if p.color {
		fmt.Fprintf(p.out, "%s%s%s\n", ansiPath, shown, ansiReset)
		return
	}
	fmt.Fprintln(p.out, shown)
}

// count prints "path:n", the shape grep -c uses.
func (p *printer) count(full string, n int) {
	shown := p.display(full)
	if p.color {
		fmt.Fprintf(p.out, "%s%s%s%s:%s%d\n", ansiPath, shown, ansiReset, ansiSep, ansiReset, n)
		return
	}
	fmt.Fprintf(p.out, "%s:%d\n", shown, n)
}

// block prints one file's matches, in grep's own layout.
//
// grep writes `path:line:text` for a match and `path-line-text` for a context
// line. The different separator is not decoration — it is what lets a reader,
// and a script, tell a hit from the lines printed around it.
func (p *printer) block(found *grep.FileResult) {
	shown := p.display(found.Path)

	for _, line := range found.Lines {
		separator := ":"
		if !line.Match {
			separator = "-"
		}

		var head strings.Builder
		if p.color {
			head.WriteString(ansiPath + shown + ansiReset)
			head.WriteString(ansiSep + separator + ansiReset)
			if p.numbers {
				head.WriteString(ansiLineNum + fmt.Sprint(line.Number) + ansiReset)
				head.WriteString(ansiSep + separator + ansiReset)
			}
		} else {
			head.WriteString(shown)
			head.WriteString(separator)
			if p.numbers {
				head.WriteString(fmt.Sprint(line.Number))
				head.WriteString(separator)
			}
		}

		if p.mask {
			// The key stays visible, because the key is what was being looked
			// for. What follows the separator does not.
			fmt.Fprintf(p.out, "%s%s\n", head.String(), redact.MaskLine(line.Text))
			continue
		}
		fmt.Fprintf(p.out, "%s%s\n", head.String(), p.highlight(line))
	}
}

// highlight paints the matched span, when colour is on and there is one.
//
// The bounds are checked rather than trusted: they were computed against the
// line's BYTES, and a line holding multi-byte runes still slices correctly by
// byte — but a bound past the end would panic, and a search should not be able
// to crash on somebody's file.
func (p *printer) highlight(line grep.Line) string {
	if !p.color || !line.Match || line.End <= line.Start {
		return line.Text
	}
	if line.Start < 0 || line.End > len(line.Text) {
		return line.Text
	}
	return line.Text[:line.Start] +
		ansiMatch + line.Text[line.Start:line.End] + ansiReset +
		line.Text[line.End:]
}

// keys prints one file's shape: every key path, with its value described.
func (p *printer) keys(full string, entries []redact.Entry) {
	shown := p.display(full)

	if p.color {
		fmt.Fprintf(p.out, "%s%s%s\n", ansiPath, shown, ansiReset)
	} else {
		fmt.Fprintln(p.out, shown)
	}

	// The paths are already the longest thing on the line, so the detail is
	// aligned past the widest of them rather than at a guessed column.
	width := 0
	for _, item := range entries {
		if len(item.Path) > width {
			width = len(item.Path)
		}
	}

	for _, item := range entries {
		detail := item.Detail
		if p.color && item.Redacted {
			detail = ansiMatch + detail + ansiReset
		}
		fmt.Fprintf(p.out, "  %-*s  %s\n", width, item.Path, detail)
	}
	fmt.Fprintln(p.out)
}
