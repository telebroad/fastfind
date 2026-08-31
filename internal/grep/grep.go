// Package grep searches inside files, with the flag semantics of grep(1).
//
// It exists next to the walker rather than after it: the walker already has
// every core busy discovering files, and a content search is the same shape of
// work — mostly blocked on I/O, embarrassingly parallel per file. Handing each
// matched file straight to a scanner on the goroutine that found it means the
// search finishes roughly when the walk does, rather than starting when the walk
// ends.
package grep

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// How much of a file is examined before deciding it is binary.
//
// The same rule grep uses: a NUL byte near the start means the bytes are not
// text. Reading further would not make the answer better and would mean holding
// more of every file in memory.
const sniffSize = 8192

// Lines longer than this are not searched.
//
// A minified bundle or a base64 blob is one line of several megabytes. Matching
// against it is slow, and printing it floods a terminal with something nobody
// can read — grep's own answer is to report the file rather than the line.
const maxLineLength = 1 << 20

// Options is one content search, in grep's own vocabulary.
type Options struct {
	// Pattern is the regular expression, or a literal when Fixed is set.
	Pattern string
	// Fixed matches the pattern literally rather than as a regex. grep -F.
	Fixed bool
	// IgnoreCase matches without regard to case. grep -i.
	//
	// Note this defaults to FALSE, unlike the name search, because that is what
	// grep does and muscle memory is worth more here than internal consistency.
	IgnoreCase bool
	// Word matches only where the pattern falls on word boundaries. grep -w.
	Word bool
	// Invert reports lines that do NOT match. grep -v.
	Invert bool
	// Before and After are the context lines kept either side. grep -B, -A.
	Before, After int
}

// Matcher is a compiled search, safe to use from many goroutines at once.
type Matcher struct {
	re   *regexp.Regexp
	opts Options
	// literal is set when the search is a plain case-sensitive substring, which
	// is the common case and much faster through bytes.Contains than through the
	// regexp engine.
	literal []byte
}

// Compile prepares a search.
func Compile(opts Options) (*Matcher, error) {
	if opts.Pattern == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	if opts.Before < 0 || opts.After < 0 {
		return nil, fmt.Errorf("context lines cannot be negative")
	}

	m := &Matcher{opts: opts}

	// The fast path: a literal, case-sensitive, whole-line-anywhere search needs
	// no regexp engine at all.
	if opts.Fixed && !opts.IgnoreCase && !opts.Word {
		m.literal = []byte(opts.Pattern)
		return m, nil
	}

	expr := opts.Pattern
	if opts.Fixed {
		expr = regexp.QuoteMeta(expr)
	}
	if opts.Word {
		// \b around the whole pattern, not around each alternative — grep -w
		// requires the WHOLE match to sit on word boundaries.
		expr = `\b(?:` + expr + `)\b`
	}
	if opts.IgnoreCase {
		expr = `(?i)` + expr
	}

	re, err := regexp.Compile(expr)
	if err != nil {
		// Say the fix, not just the fault. Searching for a snippet of code —
		// `computed(`, `err != nil`, `foo[0]` — is the single most common thing
		// anyone types here, and every one of those is an invalid regex. Being
		// told only that it is invalid leaves a person to work out that -F is
		// what they wanted.
		if !opts.Fixed {
			return nil, fmt.Errorf(
				"bad pattern %q: %w\n       (searching for text rather than a regex? add -F)",
				opts.Pattern, err)
		}
		return nil, fmt.Errorf("bad pattern %q: %w", opts.Pattern, err)
	}
	m.re = re
	return m, nil
}

// Line is one line of output, either a match or a line of context around one.
type Line struct {
	Number int
	Text   string
	Match  bool
	// Start and End bound the matched span within Text, for highlighting. Both
	// are zero on a context line.
	Start, End int
}

// FileResult is everything found in one file.
type FileResult struct {
	Path  string
	Lines []Line
	// Count is the number of MATCHING lines, which is not len(Lines) once
	// context is being kept.
	Count int
}

// matchAt reports whether a line matches, and where.
func (m *Matcher) matchAt(line []byte) (start, end int, ok bool) {
	if m.literal != nil {
		at := bytes.Index(line, m.literal)
		if at < 0 {
			return 0, 0, false
		}
		return at, at + len(m.literal), true
	}
	loc := m.re.FindIndex(line)
	if loc == nil {
		return 0, 0, false
	}
	return loc[0], loc[1], true
}

// Search reads one file and returns what matched.
//
// A file with no matches returns nil, so the caller can test the result rather
// than a count. A file that cannot be read, or that turns out to be binary,
// also returns nil: neither is worth interrupting a search over, which is what
// grep does too.
func (m *Matcher) Search(path string) (*FileResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	head := make([]byte, sniffSize)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	head = head[:n]

	decoded, isText := decode(head)
	if !isText {
		return nil, nil
	}

	// A UTF-16 file has to be read whole and converted; anything else can be
	// streamed. Streaming matters — a search should not need the largest file on
	// the disk to fit in memory.
	var reader io.Reader
	if decoded != nil {
		rest, err := io.ReadAll(file)
		if err != nil {
			return nil, err
		}
		whole, _ := decode(append(head, rest...))
		reader = bytes.NewReader(whole)
	} else {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		reader = file
	}

	return m.scan(path, reader)
}

// scan walks the lines of an already-decoded reader.
func (m *Matcher) scan(path string, reader io.Reader) (*FileResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineLength)

	result := &FileResult{Path: path}

	// A ring of the last -B lines, and a countdown of how many -A lines are
	// still owed to the most recent match.
	var before []Line
	afterOwed := 0
	lastEmitted := 0

	number := 0
	for scanner.Scan() {
		number++
		raw := scanner.Bytes()

		start, end, hit := m.matchAt(raw)
		if m.opts.Invert {
			// Inverting reports the whole line; there is no span to highlight in
			// a line that matched by NOT matching.
			hit, start, end = !hit, 0, 0
		}

		text := string(raw)

		switch {
		case hit:
			for _, kept := range before {
				if kept.Number > lastEmitted {
					result.Lines = append(result.Lines, kept)
					lastEmitted = kept.Number
				}
			}
			before = before[:0]

			result.Lines = append(result.Lines, Line{
				Number: number, Text: text, Match: true, Start: start, End: end,
			})
			lastEmitted = number
			result.Count++
			afterOwed = m.opts.After

		case afterOwed > 0:
			result.Lines = append(result.Lines, Line{Number: number, Text: text})
			lastEmitted = number
			afterOwed--

		case m.opts.Before > 0:
			before = append(before, Line{Number: number, Text: text})
			if len(before) > m.opts.Before {
				before = before[1:]
			}
		}
	}

	if err := scanner.Err(); err != nil {
		// A line past the buffer is a minified bundle or a blob, not a failure
		// worth stopping the whole search for. Whatever was found before it
		// still stands.
		if err != bufio.ErrTooLong {
			return nil, err
		}
	}

	if result.Count == 0 {
		return nil, nil
	}
	return result, nil
}

// decode decides whether bytes are text, converting UTF-16 where it finds it.
//
// Returns (nil, true) for ordinary bytes that need no conversion, (converted,
// true) for UTF-16, and (nil, false) for binary.
//
// UTF-16 is worth the trouble on Windows specifically: PowerShell's redirection,
// Notepad, and a good deal of Windows tooling still write UTF-16LE with a BOM,
// and to a byte-oriented search every one of those files looks like binary
// because every other byte is NUL. grep on Windows famously reports them as
// "binary file matches" and stops. This reads them.
func decode(raw []byte) ([]byte, bool) {
	switch {
	case len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE:
		return fromUTF16(raw[2:], false), true
	case len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF:
		return fromUTF16(raw[2:], true), true
	case len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF:
		// UTF-8 with a BOM is still UTF-8; nothing to convert.
		return nil, true
	}

	// grep's own test: a NUL in the sniffed head means these bytes are not text.
	if bytes.IndexByte(raw, 0) >= 0 {
		return nil, false
	}
	// And bytes that are not valid UTF-8 at all are treated as binary rather
	// than mangled into replacement characters.
	if !utf8.Valid(raw) && len(raw) > 0 {
		// Trailing bytes of a multi-byte rune can be cut by the sniff boundary,
		// so a failure only counts if it is not right at the end.
		if trimmed := trimPartialRune(raw); !utf8.Valid(trimmed) {
			return nil, false
		}
	}
	return nil, true
}

// trimPartialRune drops an incomplete rune left at the end of a sniffed chunk.
func trimPartialRune(raw []byte) []byte {
	for i := 0; i < utf8.UTFMax && i < len(raw); i++ {
		trimmed := raw[:len(raw)-i]
		if r, size := utf8.DecodeLastRune(trimmed); r != utf8.RuneError || size > 1 {
			return trimmed
		}
	}
	return raw
}

// fromUTF16 converts UTF-16 bytes to UTF-8.
func fromUTF16(raw []byte, bigEndian bool) []byte {
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		if bigEndian {
			units = append(units, uint16(raw[i])<<8|uint16(raw[i+1]))
		} else {
			units = append(units, uint16(raw[i+1])<<8|uint16(raw[i]))
		}
	}
	return []byte(string(utf16.Decode(units)))
}

// Describe renders the search back as a person would say it, for the summary.
func (o Options) Describe() string {
	var notes []string
	if o.Fixed {
		notes = append(notes, "literal")
	}
	if o.IgnoreCase {
		notes = append(notes, "any case")
	}
	if o.Word {
		notes = append(notes, "whole words")
	}
	if o.Invert {
		notes = append(notes, "inverted")
	}
	if len(notes) == 0 {
		return ""
	}
	return " (" + strings.Join(notes, ", ") + ")"
}
