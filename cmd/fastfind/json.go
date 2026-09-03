package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/telebroad/fastfind/internal/redact"
	"github.com/telebroad/fastfind/internal/walk"
)

// describeJSON walks for JSON files and prints the shape of each.
//
// The values are never read out. What this answers is "what keys are in here,
// and are they set" — which is what somebody looking at a config file almost
// always wants — without answering "and what are they", which is the part that
// ends up in a terminal, in a scrollback, and in a stored transcript.
func describeJSON(query walk.Query, show *printer) (matched int, scanned int64) {
	type described struct {
		path    string
		entries []redact.Entry
	}

	results := make(chan described, 64)
	done := make(chan int)

	go func() {
		count := 0
		for found := range results {
			show.keys(found.path, found.entries)
			count += len(found.entries)
		}
		done <- count
	}()

	scanned = walk.Walk(query, func(hit walk.Result) {
		if hit.IsDir {
			return
		}
		entries, err := redact.KeysOfFile(hit.Path)
		if err != nil {
			// A file that is not JSON, or cannot be read, is simply not one of
			// the files being asked about.
			return
		}
		results <- described{path: hit.Path, entries: entries}
	})

	close(results)
	return <-done, scanned
}

// showValue prints one value from one JSON file, by dotted path.
//
// Deliberately separate from every other mode, and deliberately requiring the
// file to be named. Reading the shape of a config is casual and safe; reading a
// value out of one is neither, and the two should not end up one keystroke
// apart.
func showValue(file, path string, out io.Writer) int {
	if file == "" {
		fmt.Fprintln(os.Stderr, "fastfind: -get needs a file: fastfind -get database.password config.json")
		return exitUsage
	}

	full, err := filepath.Abs(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fastfind:", err)
		return exitUsage
	}

	raw, err := os.ReadFile(full)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fastfind:", err)
		return exitUsage
	}

	value, sensitive, err := redact.Get(raw, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fastfind: %s: %v\n", path, err)
		return exitNotFound
	}

	if sensitive {
		// Printed, because it was explicitly asked for — but said out loud, on
		// stderr so it does not contaminate a pipe, because the person may not
		// have noticed where this output is going.
		fmt.Fprintf(os.Stderr,
			"fastfind: %s looks like a credential — this is now in your terminal history\n", path)
	}

	switch typed := value.(type) {
	case string:
		fmt.Fprintln(out, typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			fmt.Fprintln(out, typed)
			return exitFound
		}
		fmt.Fprintln(out, string(encoded))
	}
	return exitFound
}
