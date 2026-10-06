package cli_test

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/exitcode"
)

// TestBodyCharsCutsALongBodyAndSaysSo is issue 219. --no-body was the only
// bound, and it dropped the two-line comments that answered the question along
// with the pasted logs that made the feed expensive. A body cut to its first N
// characters says it was cut and how long it was, so the cut is never silent.
func TestBodyCharsCutsALongBodyAndSaysSo(t *testing.T) {
	url := activityJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "activity", "--since", fixtureSince,
		"--body-chars", "10", "--format", "xml")

	want := `<body format="wiki" truncated="true" length="` +
		strconv.Itoa(utf8.RuneCountInString(bulkyComment)) + `"><![CDATA[Here is th]]></body>`
	if !strings.Contains(got.stdout, want) {
		t.Errorf("the body was not cut to ten characters and marked:\nwant %s\n%s", want, got.stdout)
	}
	if strings.Contains(got.stdout, "whole investigation") {
		t.Errorf("the body past the bound was emitted:\n%s", got.stdout)
	}
	// Every event is in the result, so the result is complete. The bound is
	// one the caller set, and each cut body carries it.
	if !strings.Contains(got.stdout, `complete="true"`) || !strings.Contains(got.stdout, `kind="transition"`) {
		t.Errorf("the feed lost an event or its completeness:\n%s", got.stdout)
	}
}

// TestBodyCharsCountsCharacters counts code points, not bytes. A byte bound
// would cut inside the é and emit a body that is not UTF-8.
func TestBodyCharsCountsCharacters(t *testing.T) {
	const comment = "café ☕ étude, and then a great deal more"
	url := activityJiraWith(t, comment)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "activity", "--since", fixtureSince,
		"--body-chars", "6", "--format", "xml")
	want := `truncated="true" length="` + strconv.Itoa(utf8.RuneCountInString(comment)) +
		`"><![CDATA[café ☕]]></body>`
	if !strings.Contains(got.stdout, want) {
		t.Errorf("not cut at six characters:\nwant %s\n%s", want, got.stdout)
	}
}

// TestBodyCharsLeavesAShortBodyWhole: a body within the bound is the body, and
// carries nothing that says otherwise.
func TestBodyCharsLeavesAShortBodyWhole(t *testing.T) {
	url := activityJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	n := strconv.Itoa(utf8.RuneCountInString(bulkyComment))
	got := mustRun(t, env, "issue", "activity", "--since", fixtureSince,
		"--body-chars", n, "--format", "xml")
	if !strings.Contains(got.stdout, `<body format="wiki"><![CDATA[`+bulkyComment+`]]></body>`) {
		t.Errorf("a body exactly at the bound was changed or marked:\n%s", got.stdout)
	}
}

// TestBodyCharsAddsALengthColumnInTSV is the half a caller sees by default. TSV
// has no attributes, so the whole length is a column of its own, after the
// body, and filled only where the body was cut: an empty cell is a whole body,
// the way an empty cell is "no such part" everywhere else in this row.
func TestBodyCharsAddsALengthColumnInTSV(t *testing.T) {
	url := activityJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "activity", "--since", fixtureSince,
		"--body-chars", "10", "--format", "tsv")
	lines := strings.Split(strings.TrimRight(got.stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a header and the fixture's two events, got %d lines:\n%s", len(lines), got.stdout)
	}
	want := []string{"at", "issue", "kind", "author", "field", "time-spent", "from", "to", "body", "body-length"}
	if strings.Join(strings.Split(lines[0], "\t"), ",") != strings.Join(want, ",") {
		t.Fatalf("columns = %q, want %v", lines[0], want)
	}
	length := strconv.Itoa(utf8.RuneCountInString(bulkyComment))
	var cut, whole int
	for _, line := range lines[1:] {
		cells := strings.Split(line, "\t")
		if len(cells) != len(want) {
			t.Fatalf("row has %d cells, header has %d: %q", len(cells), len(want), line)
		}
		switch cells[2] {
		case "comment":
			cut++
			if cells[8] != "Here is th" || cells[9] != length {
				t.Errorf("comment row body %q length %q, want %q %q", cells[8], cells[9], "Here is th", length)
			}
		case "transition":
			whole++
			if cells[8] != "" || cells[9] != "" {
				t.Errorf("a row with no body has a body or a length: %q", line)
			}
		}
	}
	if cut != 1 || whole != 1 {
		t.Errorf("rows: %d comment, %d transition; want one each:\n%s", cut, whole, got.stdout)
	}
}

// TestBodyCharsRefusesWhatItCannotHonor refuses, before any request, the three
// invocations it could only answer by picking for the caller. --no-body and a
// bound are two answers to one question. A cut ADF document is not a
// document, and its format attribute would still say adf.
func TestBodyCharsRefusesWhatItCannotHonor(t *testing.T) {
	url := activityJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	for _, tc := range []struct {
		code string
		args []string
	}{
		{"INVALID_BODY_CHARS", []string{"--body-chars", "0"}},
		{"INVALID_BODY_CHARS", []string{"--body-chars", "-3"}},
		{"BODY_CHARS_AND_NO_BODY", []string{"--body-chars", "10", "--no-body"}},
		{"BODY_CHARS_AND_RAW_BODY", []string{"--body-chars", "10", "--raw-body"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			got := run(t, env, append([]string{"issue", "activity", "--since", fixtureSince}, tc.args...)...)
			if got.exit != exitcode.Usage || !strings.Contains(got.stderr, tc.code) {
				t.Errorf("exit = %v, want %v with %s:\n%s", got.exit, exitcode.Usage, tc.code, got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("a refused invocation wrote a result:\n%s", got.stdout)
			}
		})
	}
}
