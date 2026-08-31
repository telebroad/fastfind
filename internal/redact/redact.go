// Package redact describes files without reproducing what is in them.
//
// The problem it solves is specific. Reading a config file to find out how it is
// shaped — what keys exist, where a setting lives — normally means printing the
// file, and printing the file means printing the credentials in it. In a
// terminal that is bad. In a session with an AI assistant it is worse, because
// the transcript is stored: a secret that reaches the screen is a secret that
// has been written down somewhere it will outlive the reason it was shown.
//
// Every ordinary tool fails OPEN here. `cat`, `grep`, `jq` and friends print
// values unless you go out of your way to stop them, and going out of your way
// is exactly what nobody does at the moment they are debugging something.
//
// So this fails CLOSED. A value is shown only when it cannot carry a secret,
// and everything else is described by its type and its length. That is enough to
// answer "is the key there", "is it the right shape", "is it empty" — which is
// what the question almost always is — without answering "what is it".
package redact

import (
	"fmt"
	"strings"
)

// Sensitive names a key whose value must never be printed, whatever it is.
//
// Matched as a substring of the lower-cased key, so `dbPassword`, `API_KEY` and
// `auth.token` are all caught. The list errs towards catching too much: a
// wrongly redacted port number costs one extra command, and a wrongly printed
// credential cannot be taken back.
var Sensitive = []string{
	"password", "passwd", "pwd",
	"secret", "token", "credential", "auth",
	"apikey", "api_key", "accesskey", "access_key",
	"privatekey", "private_key", "signature", "salt",
	"session", "cookie", "bearer",
	"dsn", "connectionstring", "connection_string",
	"certificate", "passphrase", "pin",
}

// IsSensitive reports whether a key's name marks its value as a credential.
func IsSensitive(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range Sensitive {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// Kind is what a value is, without being what it is.
type Kind string

const (
	KindString Kind = "string"
	KindNumber Kind = "number"
	KindBool   Kind = "bool"
	KindNull   Kind = "null"
	KindObject Kind = "object"
	KindArray  Kind = "array"
)

// Describe renders a value safely.
//
// A value is only ever printed when it cannot carry a secret:
//
//   - bool has two possible values, so printing one reveals nothing that the
//     key's existence did not already.
//   - null is the absence of a value.
//
// Everything else — strings, numbers, and anything under a key whose name marks
// it as a credential — is reduced to its type and its length. A number is
// redacted too, which looks over-cautious until you remember that account
// numbers, PINs and customer ids are numbers.
func Describe(key string, kind Kind, value any, length int) string {
	if IsSensitive(key) {
		return fmt.Sprintf("%s(%d)  ** redacted: key looks like a credential **", kind, length)
	}

	switch kind {
	case KindBool:
		return fmt.Sprintf("bool = %v", value)
	case KindNull:
		return "null"
	case KindObject:
		if length == 1 {
			return "object(1 key)"
		}
		return fmt.Sprintf("object(%d keys)", length)
	case KindArray:
		return fmt.Sprintf("array(%d)", length)
	default:
		// The length is the useful part: it separates "the key is there but
		// empty" from "the key is there and set", which is the question being
		// asked most of the time.
		return fmt.Sprintf("%s(%d)", kind, length)
	}
}
