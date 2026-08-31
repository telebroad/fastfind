package grep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write puts content in a temp file and returns its path.
func write(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// search compiles and runs one search over some content.
func search(t *testing.T, opts Options, content string) *FileResult {
	t.Helper()
	m, err := Compile(opts)
	if err != nil {
		t.Fatalf("compile %+v: %v", opts, err)
	}
	found, err := m.Search(write(t, "sample.txt", []byte(content)))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return found
}

// texts pulls just the line text out of a result.
func texts(found *FileResult) []string {
	if found == nil {
		return nil
	}
	out := make([]string, 0, len(found.Lines))
	for _, line := range found.Lines {
		out = append(out, line.Text)
	}
	return out
}

func TestFindsAMatchingLine(t *testing.T) {
	found := search(t, Options{Pattern: "needle"}, "hay\nneedle here\nhay\n")

	if found == nil {
		t.Fatal("expected a match")
	}
	if found.Count != 1 {
		t.Errorf("count %d, want 1", found.Count)
	}
	if found.Lines[0].Number != 2 {
		t.Errorf("line %d, want 2", found.Lines[0].Number)
	}
}

func TestReturnsNilWhenNothingMatches(t *testing.T) {
	// nil rather than an empty result, so a caller can test the value itself.
	if found := search(t, Options{Pattern: "needle"}, "hay\nhay\n"); found != nil {
		t.Errorf("expected nil, got %+v", found)
	}
}

func TestIsCaseSensitiveByDefault(t *testing.T) {
	// Unlike the NAME search, which ignores case. grep's default wins here
	// because that is the muscle memory people arrive with.
	if found := search(t, Options{Pattern: "NEEDLE"}, "needle\n"); found != nil {
		t.Error("expected a case-sensitive search to miss")
	}
	if found := search(t, Options{Pattern: "NEEDLE", IgnoreCase: true}, "needle\n"); found == nil {
		t.Error("expected -i to match")
	}
}

func TestWordRequiresTheWholeMatchOnBoundaries(t *testing.T) {
	if found := search(t, Options{Pattern: "cat", Word: true}, "concatenate\n"); found != nil {
		t.Error("-w should not match inside a longer word")
	}
	if found := search(t, Options{Pattern: "cat", Word: true}, "a cat sat\n"); found == nil {
		t.Error("-w should match a standalone word")
	}
}

func TestWordWrapsTheWholePatternNotEachAlternative(t *testing.T) {
	// `\bcat|dog\b` would anchor only the outer edges of each side; grep -w
	// requires the WHOLE match to sit on boundaries, which needs the group.
	found := search(t, Options{Pattern: "cat|dog", Word: true}, "hotdogs\n")
	if found != nil {
		t.Error("-w with an alternation should not match inside a longer word")
	}
	if found := search(t, Options{Pattern: "cat|dog", Word: true}, "a dog\n"); found == nil {
		t.Error("-w with an alternation should still match a standalone word")
	}
}

func TestFixedTreatsThePatternLiterally(t *testing.T) {
	// Without -F this is "any character followed by anything"; with it, a dot.
	if found := search(t, Options{Pattern: "a.c", Fixed: true}, "abc\n"); found != nil {
		t.Error("-F should not let . match any character")
	}
	if found := search(t, Options{Pattern: "a.c", Fixed: true}, "a.c\n"); found == nil {
		t.Error("-F should match the literal text")
	}
}

func TestFixedWithSpecialCharactersDoesNotFailToCompile(t *testing.T) {
	// `a(b` is not a valid regex; as a literal it is fine, and a search for a
	// snippet of code should not have to be escaped by hand.
	found := search(t, Options{Pattern: "a(b", Fixed: true}, "xxa(byy\n")
	if found == nil {
		t.Error("-F should match text that would be an invalid regex")
	}
}

func TestInvertReportsTheLinesThatDoNotMatch(t *testing.T) {
	found := search(t, Options{Pattern: "keep", Invert: true}, "keep\ndrop\nkeep\nalso drop\n")

	if got := texts(found); len(got) != 2 || got[0] != "drop" || got[1] != "also drop" {
		t.Errorf("got %v, want [drop, also drop]", got)
	}
}

func TestContextLinesAfter(t *testing.T) {
	found := search(t, Options{Pattern: "hit", After: 2}, "a\nhit\nb\nc\nd\n")

	if got := texts(found); len(got) != 3 || got[0] != "hit" || got[2] != "c" {
		t.Errorf("got %v, want [hit b c]", got)
	}
	// Context lines are not matches, and the count says so.
	if found.Count != 1 {
		t.Errorf("count %d, want 1 — context is not a match", found.Count)
	}
}

func TestContextLinesBefore(t *testing.T) {
	found := search(t, Options{Pattern: "hit", Before: 2}, "a\nb\nc\nhit\nd\n")

	if got := texts(found); len(got) != 3 || got[0] != "b" || got[2] != "hit" {
		t.Errorf("got %v, want [b c hit]", got)
	}
}

func TestContextDoesNotPrintALineTwice(t *testing.T) {
	// Two matches close together share the lines between them; printing that
	// overlap once is what grep does, and printing it twice reads as a bug.
	found := search(t, Options{Pattern: "hit", Before: 2, After: 2}, "a\nhit\nb\nhit\nc\n")

	seen := map[int]int{}
	for _, line := range found.Lines {
		seen[line.Number]++
	}
	for number, times := range seen {
		if times != 1 {
			t.Errorf("line %d printed %d times, want 1", number, times)
		}
	}
}

func TestReportsWhereTheMatchIsForHighlighting(t *testing.T) {
	found := search(t, Options{Pattern: "needle"}, "a needle here\n")

	line := found.Lines[0]
	if line.Text[line.Start:line.End] != "needle" {
		t.Errorf("span %d:%d gives %q, want needle",
			line.Start, line.End, line.Text[line.Start:line.End])
	}
}

func TestSkipsBinaryFiles(t *testing.T) {
	m, _ := Compile(Options{Pattern: "needle"})
	// A NUL near the start is grep's own test for "these bytes are not text".
	path := write(t, "blob.bin", []byte("needle\x00\x01\x02binary"))

	found, err := m.Search(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != nil {
		t.Error("expected a binary file to be skipped, not matched")
	}
}

func TestReadsUTF16LEWhichWindowsToolsWrite(t *testing.T) {
	// PowerShell redirection, Notepad and much of Windows still write UTF-16LE
	// with a BOM. Every other byte is NUL, so a byte-oriented search calls the
	// whole file binary — which is exactly what grep on Windows does.
	content := []byte{0xFF, 0xFE}
	for _, r := range "hello\nneedle\nworld\n" {
		content = append(content, byte(r), 0x00)
	}

	m, _ := Compile(Options{Pattern: "needle"})
	found, err := m.Search(write(t, "utf16.txt", content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found == nil {
		t.Fatal("expected UTF-16LE to be read, not skipped as binary")
	}
	if found.Lines[0].Text != "needle" {
		t.Errorf("got %q, want needle", found.Lines[0].Text)
	}
}

func TestReadsUTF16BE(t *testing.T) {
	content := []byte{0xFE, 0xFF}
	for _, r := range "needle\n" {
		content = append(content, 0x00, byte(r))
	}

	m, _ := Compile(Options{Pattern: "needle"})
	found, err := m.Search(write(t, "utf16be.txt", content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found == nil {
		t.Fatal("expected UTF-16BE to be read")
	}
}

func TestReadsUTF8WithABOM(t *testing.T) {
	content := append([]byte{0xEF, 0xBB, 0xBF}, []byte("needle\n")...)

	m, _ := Compile(Options{Pattern: "needle"})
	found, err := m.Search(write(t, "bom.txt", content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found == nil {
		t.Fatal("a UTF-8 BOM is still UTF-8 and should be searched")
	}
}

func TestHandlesAFileWithNoTrailingNewline(t *testing.T) {
	if found := search(t, Options{Pattern: "needle"}, "needle"); found == nil {
		t.Error("the last line counts even without a newline after it")
	}
}

func TestSurvivesAnEnormousLine(t *testing.T) {
	// A minified bundle is one line of megabytes. It must not match, and it must
	// not take the search down with it.
	huge := make([]byte, maxLineLength+1024)
	for i := range huge {
		huge[i] = 'x'
	}
	content := append([]byte("needle\n"), huge...)

	m, _ := Compile(Options{Pattern: "needle"})
	found, err := m.Search(write(t, "bundle.js", content))
	if err != nil {
		t.Fatalf("an over-long line should not fail the search: %v", err)
	}
	// What was found before the monster line still stands.
	if found == nil || found.Count != 1 {
		t.Errorf("got %+v, want the one match before the long line", found)
	}
}

func TestCompileRejectsAnEmptyPattern(t *testing.T) {
	if _, err := Compile(Options{}); err == nil {
		t.Error("expected an empty pattern to be refused")
	}
}

func TestCompileRejectsABadRegex(t *testing.T) {
	_, err := Compile(Options{Pattern: "a("})
	if err == nil {
		t.Fatal("expected an invalid regex to be refused")
	}
	// The message has to name the pattern, or the person cannot see what to fix.
	if got := err.Error(); !strings.Contains(got, "a(") {
		t.Errorf("error %q should quote the offending pattern", got)
	}
}

func TestCompileRejectsNegativeContext(t *testing.T) {
	if _, err := Compile(Options{Pattern: "x", Before: -1}); err == nil {
		t.Error("expected negative context to be refused")
	}
}

func TestSearchReportsAMissingFile(t *testing.T) {
	m, _ := Compile(Options{Pattern: "x"})
	if _, err := m.Search(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Error("expected an error for a file that is not there")
	}
}

func TestDescribeSaysWhatTheSearchWas(t *testing.T) {
	if got := (Options{}).Describe(); got != "" {
		t.Errorf("a plain search needs no note, got %q", got)
	}
	got := (Options{IgnoreCase: true, Word: true}).Describe()
	if !strings.Contains(got, "any case") || !strings.Contains(got, "whole words") {
		t.Errorf("got %q, want it to mention both options", got)
	}
}
