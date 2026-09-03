package main

import (
	"flag"
	"strings"
)

// Go's flag package stops parsing at the first argument that is not a flag,
// so `fastfind '*.go' -max 3` left -max and 3 sitting in flag.Args(), where
// they were joined into the name to look for. The search then hunted for a
// file called "*.go -max 3", found nothing, and said so as though nothing
// were wrong — the worst kind of failure, because the command looks right.
//
// grep permutes instead: `grep pattern -i file` works, and so does every
// other ordering. Since the whole point of matching grep's flags is that
// muscle memory carries over, matching grep's flags but not its argument
// order would hand people back the same surprise in a new place.
func permute(args []string, fs *flag.FlagSet) []string {
	var flags, operands []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Everything after `--` is an operand by definition, however much
		// it may look like a flag. This is how you search for a file whose
		// name begins with a dash.
		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}

		if len(arg) > 1 && arg[0] == '-' {
			flags = append(flags, arg)

			// `-max 3` carries its value in the next argument and has to be
			// moved along with it. `-max=3` already holds its own, and a
			// boolean like `-i` never takes one — moving the argument after
			// a boolean would steal the search pattern.
			if !strings.Contains(arg, "=") && takesValue(fs, arg) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}

		operands = append(operands, arg)
	}

	if len(operands) == 0 {
		return flags
	}

	// Re-inserted because moving an operand to the end can put a pattern
	// like -foo in a position where the parser would read it as a flag.
	return append(append(flags, "--"), operands...)
}

// takesValue reports whether a flag consumes the argument after it. Booleans
// do not; everything else does. An unknown flag is treated as though it does
// not, so that flag.Parse reports it rather than silently swallowing whatever
// came next.
func takesValue(fs *flag.FlagSet, arg string) bool {
	f := fs.Lookup(strings.TrimLeft(arg, "-"))
	if f == nil {
		return false
	}
	boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !boolean.IsBoolFlag()
}
