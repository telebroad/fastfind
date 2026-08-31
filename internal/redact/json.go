package redact

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Entry is one key path in a document, with its value described rather than
// reproduced.
type Entry struct {
	// Path is the dotted route to the value: `database.hosts[0].name`.
	Path string
	// Kind is what the value is.
	Kind Kind
	// Detail is the safe rendering — a type and a length, or the value itself
	// when it cannot carry a secret.
	Detail string
	// Redacted is true when the key's name marked it as a credential.
	Redacted bool
}

// Keys reads a JSON document and returns every key path in it.
//
// The values never leave this function. What comes back describes the shape of
// the document — which is what somebody asking "what does this config look
// like" actually wants — and nothing else.
func Keys(raw []byte) ([]Entry, error) {
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}

	var entries []Entry
	walk("", document, &entries)
	return entries, nil
}

// KeysOfFile reads a file and describes it.
func KeysOfFile(path string) ([]Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Keys(raw)
}

// walk descends a decoded document, appending one entry per leaf.
//
// Objects and arrays get an entry of their own as well as their children, so a
// reader can see that a key exists and is empty — `logging.sinks array(0)` says
// something a missing line would not.
func walk(path string, node any, into *[]Entry) {
	switch value := node.(type) {
	case map[string]any:
		if path != "" {
			*into = append(*into, entry(path, KindObject, nil, len(value)))
		}
		// Sorted, because Go randomises map order and a description that
		// reorders itself between runs cannot be diffed.
		names := make([]string, 0, len(value))
		for name := range value {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			walk(join(path, name), value[name], into)
		}

	case []any:
		*into = append(*into, entry(path, KindArray, nil, len(value)))
		for i, item := range value {
			walk(fmt.Sprintf("%s[%d]", path, i), item, into)
		}

	case string:
		*into = append(*into, entry(path, KindString, value, len(value)))

	case float64:
		// A number is redacted like a string. That looks over-careful until you
		// remember that account numbers, PINs and customer ids are numbers.
		*into = append(*into, entry(path, KindNumber, value, len(fmt.Sprint(value))))

	case bool:
		*into = append(*into, entry(path, KindBool, value, 0))

	case nil:
		*into = append(*into, entry(path, KindNull, nil, 0))
	}
}

// entry builds one described key.
func entry(path string, kind Kind, value any, length int) Entry {
	// Sensitivity is judged on the LAST segment — the key's own name — rather
	// than on the whole path, or every key under an object called `auth` would
	// be redacted including the ones that are only a hostname.
	name := path
	if at := strings.LastIndexAny(path, ".["); at >= 0 {
		name = strings.TrimLeft(path[at:], ".[")
	}

	return Entry{
		Path:     path,
		Kind:     kind,
		Detail:   Describe(name, kind, value, length),
		Redacted: IsSensitive(name),
	}
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// Get returns one value from a document, by dotted path.
//
// This is the deliberate escape hatch: it prints what was asked for, because
// sometimes the value really is the question. It is a separate call from
// {@link Keys} on purpose — reading the shape of a file is safe and casual,
// reading a value out of it is neither, and the two should not be one flag
// apart by accident.
//
// The caller is told whether the key looked like a credential so it can say so
// before printing.
func Get(raw []byte, path string) (value any, sensitive bool, err error) {
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, false, fmt.Errorf("not valid JSON: %w", err)
	}

	node := document
	for _, segment := range strings.Split(path, ".") {
		name, index, indexed := splitIndex(segment)

		if name != "" {
			object, ok := node.(map[string]any)
			if !ok {
				return nil, false, fmt.Errorf("%q is not an object", name)
			}
			node, ok = object[name]
			if !ok {
				return nil, false, fmt.Errorf("no key %q", name)
			}
		}

		if indexed {
			array, ok := node.([]any)
			if !ok {
				return nil, false, fmt.Errorf("%q is not an array", name)
			}
			if index < 0 || index >= len(array) {
				return nil, false, fmt.Errorf("index %d is outside %q, which has %d", index, name, len(array))
			}
			node = array[index]
		}
	}

	last := path
	if at := strings.LastIndex(path, "."); at >= 0 {
		last = path[at+1:]
	}
	return node, IsSensitive(last), nil
}

// splitIndex separates `hosts[2]` into its name and its index.
func splitIndex(segment string) (name string, index int, indexed bool) {
	open := strings.IndexByte(segment, '[')
	if open < 0 || !strings.HasSuffix(segment, "]") {
		return segment, 0, false
	}
	if _, err := fmt.Sscanf(segment[open:], "[%d]", &index); err != nil {
		return segment, 0, false
	}
	return segment[:open], index, true
}
