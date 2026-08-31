package redact

import (
	"fmt"
	"strings"
	"unicode"
)

// MaskLine hides the values in a line of configuration, keeping its shape.
//
// For grep output over a config file, an .env, a YAML or an ini: the keys stay
// visible, because the keys are what was being searched for, and the values do
// not.
//
// It handles two shapes, because a config line is one or the other:
//
//   - A JSON-ish line, which can hold SEVERAL pairs. Masking everything after
//     the first colon would be safe but useless — a line like
//     `"database": {"host": ..., "port": ..., "password": ...}` would collapse
//     to one blob and the keys inside it, which are the useful part, would be
//     lost with the secret. So every value is masked individually and the
//     structure survives.
//   - A `KEY=value` line — .env, .ini, .properties — which has exactly one.
func MaskLine(line string) string {
	if looksStructured(line) {
		return maskPairs(line)
	}
	return maskAssignment(line)
}

// looksStructured reports whether a line carries quoted keys, as JSON and YAML
// do, rather than a single bare assignment.
func looksStructured(line string) bool {
	quote := strings.IndexByte(line, '"')
	if quote < 0 {
		return false
	}
	closing := strings.IndexByte(line[quote+1:], '"')
	if closing < 0 {
		return false
	}
	// A quoted run followed by a colon is a key. That is what separates
	// `"host": "db"` from a bare sentence that happens to contain a quote.
	rest := strings.TrimLeft(line[quote+1+closing+1:], " \t")
	return strings.HasPrefix(rest, ":")
}

// maskPairs walks a structured line, keeping keys and structure, masking values.
//
// Written as a scanner rather than a regular expression because the thing being
// tracked is *state* — whether the next quoted run is a key or a value — and a
// pattern cannot see that. Anything it does not recognise is passed through
// unchanged, so an unusual line comes out looking odd rather than half-masked.
func maskPairs(line string) string {
	var out strings.Builder
	i := 0

	for i < len(line) {
		c := line[i]

		// A quoted run. Whether it is a key or a value is decided by what
		// follows it: a colon means key.
		if c == '"' {
			end := closingQuote(line, i)
			if end < 0 {
				out.WriteString(line[i:])
				break
			}
			run := line[i : end+1]
			after := strings.TrimLeft(line[end+1:], " \t")

			if strings.HasPrefix(after, ":") {
				out.WriteString(run) // a key: keep it
			} else {
				out.WriteString(hide(len(run) - 2)) // a value: hide it
			}
			i = end + 1
			continue
		}

		// A bare value: a number, or true/false/null. The literals are kept —
		// there is no secret in a boolean — and numbers are hidden, because
		// account numbers and PINs are numbers.
		if isValueStart(line, i) {
			end := i
			for end < len(line) && !strings.ContainsRune(",}]", rune(line[end])) {
				end++
			}
			token := strings.TrimRight(line[i:end], " \t")
			trimmed := strings.TrimSpace(token)

			switch trimmed {
			case "true", "false", "null":
				out.WriteString(token)
			default:
				out.WriteString(strings.Repeat(" ", len(token)-len(strings.TrimLeft(token, " \t"))))
				out.WriteString(hide(len(trimmed)))
			}
			i += len(token)
			continue
		}

		out.WriteByte(c)
		i++
	}

	return out.String()
}

// isValueStart reports whether position i begins a bare value — that is,
// whether the last meaningful character before it was a colon.
func isValueStart(line string, i int) bool {
	if !unicode.IsDigit(rune(line[i])) && !strings.ContainsRune("-+tfn", rune(line[i])) {
		return false
	}
	for back := i - 1; back >= 0; back-- {
		switch line[back] {
		case ' ', '\t':
			continue
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}

// closingQuote finds the end of a quoted run, respecting backslash escapes.
func closingQuote(line string, start int) int {
	for i := start + 1; i < len(line); i++ {
		if line[i] == '\\' {
			i++
			continue
		}
		if line[i] == '"' {
			return i
		}
	}
	return -1
}

// maskAssignment handles a single `KEY=value` or `key: value` line.
func maskAssignment(line string) string {
	for _, separator := range []string{"=", ":"} {
		at := strings.Index(line, separator)
		if at < 0 {
			continue
		}
		if strings.TrimSpace(line[:at]) == "" {
			continue
		}

		value := strings.TrimSpace(line[at+1:])
		if value == "" {
			return line
		}

		// A trailing comma or brace belongs to the format, not the value.
		tail := ""
		for len(value) > 0 && strings.ContainsRune(",;{}[]", rune(value[len(value)-1])) {
			tail = string(value[len(value)-1]) + tail
			value = strings.TrimRight(value[:len(value)-1], " \t")
		}

		switch value {
		case "true", "false", "null", "":
			return line
		}

		quoted := len(value) >= 2 &&
			((value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\''))

		if quoted {
			return fmt.Sprintf("%s%s %c%s%c%s",
				line[:at], separator, value[0], hide(len(value)-2), value[len(value)-1], tail)
		}
		return fmt.Sprintf("%s%s %s%s", line[:at], separator, hide(len(value)), tail)
	}

	return line
}

// hide renders a value as its own length and nothing else.
//
// The length stays because it separates "the setting is empty" from "the
// setting is filled in", which is usually the actual question. Nothing else
// survives — not a prefix, not a suffix. The first character of a password is a
// character of a password.
func hide(length int) string {
	return fmt.Sprintf("<%d chars>", length)
}
