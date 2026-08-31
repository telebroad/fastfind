package redact

import (
	"strings"
	"testing"
)

// The whole point of this package is that a secret never reaches the output. A
// bug here is not a wrong answer, it is a leaked credential — so the tests are
// written the paranoid way round: rather than checking the masker produced some
// expected string, most of them check the SECRET IS ABSENT from whatever it
// produced.

// secrets are the values that must never appear in any output.
var secrets = []string{
	"hunter2-real-secret",
	"sk-live-abc123def456",
	"AKIAIOSFODNN7EXAMPLE",
	"db.internal",
}

// mustNotLeak fails if any known secret survived into the output.
func mustNotLeak(t *testing.T, output string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Errorf("LEAKED %q in output: %s", secret, output)
		}
	}
}

func TestIsSensitiveCatchesTheUsualNames(t *testing.T) {
	for _, key := range []string{
		"password", "Password", "DB_PASSWORD", "dbPassword",
		"secret", "apiKey", "API_KEY", "access_key",
		"token", "authToken", "bearer", "sessionId",
		"privateKey", "passphrase", "DSN", "connection_string",
	} {
		if !IsSensitive(key) {
			t.Errorf("%q should be treated as a credential", key)
		}
	}
}

func TestIsSensitiveLeavesOrdinaryKeysAlone(t *testing.T) {
	for _, key := range []string{"host", "port", "timeout", "debug", "name", "url", "poolSize"} {
		if IsSensitive(key) {
			t.Errorf("%q is not a credential", key)
		}
	}
}

func TestDescribeNeverPrintsAString(t *testing.T) {
	// Not even an innocent-looking one. The key's name is a guess, and a guess
	// is not a basis for printing somebody's data.
	got := Describe("host", KindString, "db.internal", len("db.internal"))

	mustNotLeak(t, got)
	if got != "string(11)" {
		t.Errorf("got %q, want string(11)", got)
	}
}

func TestDescribePrintsWhatCannotHoldASecret(t *testing.T) {
	// A bool has two possible values, so printing one reveals nothing the key's
	// existence did not already.
	if got := Describe("debug", KindBool, true, 0); got != "bool = true" {
		t.Errorf("got %q, want bool = true", got)
	}
	if got := Describe("beta", KindNull, nil, 0); got != "null" {
		t.Errorf("got %q, want null", got)
	}
}

func TestDescribeRedactsNumbersToo(t *testing.T) {
	// Over-cautious until you remember that account numbers, PINs and customer
	// ids are numbers.
	got := Describe("accountNumber", KindNumber, 4111111111111111, 16)
	if strings.Contains(got, "4111") {
		t.Errorf("a number leaked: %s", got)
	}
}

func TestDescribeSaysWhenItRedactedForTheKeyName(t *testing.T) {
	got := Describe("password", KindString, "hunter2-real-secret", 19)

	mustNotLeak(t, got)
	if !strings.Contains(got, "redacted") {
		t.Errorf("got %q — it should say why it was withheld", got)
	}
	// The length still comes through: it separates "empty" from "set", which is
	// usually the question.
	if !strings.Contains(got, "19") {
		t.Errorf("got %q — the length is the useful part", got)
	}
}

func TestDescribeRedactsEvenABoolUnderASensitiveKey(t *testing.T) {
	// The key-name rule wins over the type rule. A bool named `secret` is
	// unlikely, but a rule with an exception is a rule nobody can rely on.
	got := Describe("secret", KindBool, true, 0)
	if strings.Contains(got, "true") {
		t.Errorf("got %q — a sensitive key should be redacted whatever its type", got)
	}
}

func TestMaskLineOnAnEnvFile(t *testing.T) {
	for _, line := range []string{
		"DB_PASSWORD=hunter2-real-secret",
		"API_TOKEN=sk-live-abc123def456",
		"DB_HOST=db.internal",
	} {
		got := MaskLine(line)
		mustNotLeak(t, got)
		// The key survives, because the key is what was searched for.
		key := strings.SplitN(line, "=", 2)[0]
		if !strings.Contains(got, key) {
			t.Errorf("MaskLine(%q) = %q — the key should still be visible", line, got)
		}
	}
}

func TestMaskLineKeepsBooleans(t *testing.T) {
	if got := MaskLine("DEBUG=true"); got != "DEBUG=true" {
		t.Errorf("got %q — there is no secret in a boolean", got)
	}
}

func TestMaskLineOnJSONMasksEachValueSeparately(t *testing.T) {
	line := `  "database": {"host":"db.internal","port":5432,"password":"hunter2-real-secret"},`
	got := MaskLine(line)

	mustNotLeak(t, got)
	// Every key survives — that is the difference from masking everything after
	// the first colon, which would take the useful part with the secret.
	for _, key := range []string{"database", "host", "port", "password"} {
		if !strings.Contains(got, key) {
			t.Errorf("key %q was lost: %s", key, got)
		}
	}
}

func TestMaskLineKeepsJSONStructure(t *testing.T) {
	got := MaskLine(`{"a":"secret1","b":[1,2],"c":true}`)

	for _, char := range []string{"{", "}", "[", "]", ","} {
		if !strings.Contains(got, char) {
			t.Errorf("structure character %q was lost: %s", char, got)
		}
	}
	// A boolean stays; there is nothing to hide in one.
	if !strings.Contains(got, "true") {
		t.Errorf("got %q — true should survive", got)
	}
}

func TestMaskLineLeavesALineWithNoValueAlone(t *testing.T) {
	for _, line := range []string{"", "# a comment", "just some prose", "{"} {
		if got := MaskLine(line); got != line {
			t.Errorf("MaskLine(%q) = %q — nothing to mask here", line, got)
		}
	}
}

func TestMaskLineHandlesAnEscapedQuoteInAValue(t *testing.T) {
	// A backslash-escaped quote does not end the string, and mis-reading it
	// would shift everything after it — turning a value into a "key" and
	// printing it.
	got := MaskLine(`{"note":"he said \"hi\"","password":"hunter2-real-secret"}`)
	mustNotLeak(t, got)
	if !strings.Contains(got, "password") {
		t.Errorf("got %q — the parse went wrong after the escape", got)
	}
}

func TestMaskLineKeepsTheLengthButNothingElse(t *testing.T) {
	got := MaskLine("SECRET=abcdefghij")

	mustNotLeak(t, got)
	if !strings.Contains(got, "10") {
		t.Errorf("got %q — the length should survive", got)
	}
	// Not a prefix, not a suffix. The first character of a password is a
	// character of a password.
	for _, fragment := range []string{"abc", "hij", "a<", ">j"} {
		if strings.Contains(got, fragment) {
			t.Errorf("got %q — %q leaked part of the value", got, fragment)
		}
	}
}
