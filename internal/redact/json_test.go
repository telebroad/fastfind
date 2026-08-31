package redact

import (
	"strings"
	"testing"
)

const sample = `{
  "database": {"host":"db.internal","port":5432,"password":"hunter2-real-secret"},
  "api": {"token":"sk-live-abc123def456","endpoints":["https://a","https://b"],"debug":true},
  "features": {"beta":null},
  "logging": {"sinks":[]}
}`

// paths returns just the key paths, for order-sensitive assertions.
func paths(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Path)
	}
	return out
}

// rendered joins everything Keys would print, so a leak anywhere is caught.
func rendered(entries []Entry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Path)
		b.WriteString(" ")
		b.WriteString(e.Detail)
		b.WriteString("\n")
	}
	return b.String()
}

func TestKeysNeverPrintsAValue(t *testing.T) {
	entries, err := Keys([]byte(sample))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The single assertion this package exists for.
	mustNotLeak(t, rendered(entries))
}

func TestKeysReportsEveryPath(t *testing.T) {
	entries, err := Keys([]byte(sample))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := paths(entries)
	for _, want := range []string{
		"database", "database.host", "database.port", "database.password",
		"api", "api.token", "api.endpoints", "api.endpoints[0]", "api.endpoints[1]",
		"features.beta", "logging.sinks",
	} {
		found := false
		for _, p := range got {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("path %q is missing from %v", want, got)
		}
	}
}

func TestKeysIsOrderedSoItCanBeDiffed(t *testing.T) {
	// Go randomises map iteration. A description that reorders itself between
	// runs cannot be compared against a previous one, which is half the point
	// of having it.
	first, _ := Keys([]byte(sample))
	for range 5 {
		again, _ := Keys([]byte(sample))
		if strings.Join(paths(first), ",") != strings.Join(paths(again), ",") {
			t.Fatal("the order changed between runs")
		}
	}
}

func TestKeysMarksTheCredentials(t *testing.T) {
	entries, _ := Keys([]byte(sample))

	marked := map[string]bool{}
	for _, e := range entries {
		marked[e.Path] = e.Redacted
	}

	for _, path := range []string{"database.password", "api.token"} {
		if !marked[path] {
			t.Errorf("%q should be marked as a credential", path)
		}
	}
	for _, path := range []string{"database.host", "database.port", "api.debug"} {
		if marked[path] {
			t.Errorf("%q is not a credential", path)
		}
	}
}

func TestKeysJudgesSensitivityOnTheKeyNotThePath(t *testing.T) {
	// Everything under an object called `auth` is not itself a credential —
	// `auth.url` is a hostname. Judging on the whole path would redact the lot
	// and make the output useless.
	entries, _ := Keys([]byte(`{"auth":{"url":"https://idp","token":"sk-live-abc123def456"}}`))

	marked := map[string]bool{}
	for _, e := range entries {
		marked[e.Path] = e.Redacted
	}
	if marked["auth.url"] {
		t.Error("auth.url is a hostname, not a credential")
	}
	if !marked["auth.token"] {
		t.Error("auth.token is a credential")
	}
}

func TestKeysDistinguishesEmptyFromMissing(t *testing.T) {
	entries, _ := Keys([]byte(sample))

	for _, e := range entries {
		if e.Path == "logging.sinks" {
			if !strings.Contains(e.Detail, "array(0)") {
				t.Errorf("got %q — an empty array should say so", e.Detail)
			}
			return
		}
	}
	t.Error("logging.sinks was not reported at all")
}

func TestKeysRefusesWhatIsNotJSON(t *testing.T) {
	if _, err := Keys([]byte("this is not JSON")); err == nil {
		t.Error("expected an error on invalid JSON")
	}
}

func TestGetReadsADottedPath(t *testing.T) {
	value, sensitive, err := Get([]byte(sample), "database.host")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "db.internal" {
		t.Errorf("got %v, want db.internal", value)
	}
	if sensitive {
		t.Error("a host is not a credential")
	}
}

func TestGetIndexesIntoAnArray(t *testing.T) {
	value, _, err := Get([]byte(sample), "api.endpoints[1]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "https://b" {
		t.Errorf("got %v, want https://b", value)
	}
}

func TestGetWarnsWhenTheKeyIsACredential(t *testing.T) {
	// Get exists to print the value — that is the deliberate escape hatch — but
	// the caller has to be told so it can say so before it does.
	_, sensitive, err := Get([]byte(sample), "database.password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sensitive {
		t.Error("database.password should be flagged as a credential")
	}
}

func TestGetSaysWhatIsWrongRatherThanReturningNothing(t *testing.T) {
	for _, c := range []struct{ path, wants string }{
		{"database.nope", "no key"},
		{"database.host.deeper", "not an object"},
		{"api.endpoints[9]", "outside"},
		{"database[0]", "not an array"},
	} {
		_, _, err := Get([]byte(sample), c.path)
		if err == nil {
			t.Errorf("%s: expected an error", c.path)
			continue
		}
		if !strings.Contains(err.Error(), c.wants) {
			t.Errorf("%s: error %q should mention %q", c.path, err, c.wants)
		}
	}
}
