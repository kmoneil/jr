package cli_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// TestARefusalOfANameTheCallerTypedNamesTheNearMisses is the rule this package
// followed in three places, differently, and in a fourth not at all.
//
// A field name went through internal/site's edit distance and landed in
// `detail`. A command name went through a substring match and landed in
// `remedy`, displacing the pointer to the command that lists everything. A
// subcommand went through cobra's own suggester, with cobra's distance and
// cobra's prefix rule, and also displaced the remedy. An unknown flag was
// offered nothing, which is the one with the cheapest candidate set in the
// tool: the command's own declaration, already in memory.
//
// Nothing asserted any of it. There was no test in this package or in
// internal/site containing the words "did you mean", which is how four answers
// to one question survived.
func TestARefusalOfANameTheCallerTypedNamesTheNearMisses(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
		exit exitcode.Code
	}{
		{
			name: "a mistyped flag",
			args: []string{"issue", "list", "--assignne", "ada"},
			want: "--assignee",
			exit: exitcode.Usage,
		},
		{
			name: "a mistyped verb",
			args: []string{"issue", "lst"},
			want: "list",
			exit: exitcode.Usage,
		},
		{
			name: "a mistyped command name to schema",
			args: []string{"schema", "issue.lst"},
			want: "issue.list",
			exit: exitcode.NotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, nil, tc.args...)

			if got.exit != tc.exit {
				t.Errorf("exit = %v, want %v", got.exit, tc.exit)
			}
			if got.stdout != "" {
				t.Errorf("a refusal wrote to stdout:\n%s", got.stdout)
			}

			detail := element(got.stderr, "detail")
			if !strings.Contains(detail, "did you mean") || !strings.Contains(detail, tc.want) {
				t.Errorf("detail does not name %q:\n%s", tc.want, got.stderr)
			}
			// The remedy is the pointer to whatever lists the whole set, and
			// it has to survive the suggestion rather than be replaced by it.
			// A caller offered a wrong guess is exactly the caller who needs
			// the full list.
			if element(got.stderr, "remedy") == "" {
				t.Errorf("the suggestion displaced the remedy:\n%s", got.stderr)
			}
		})
	}
}

// TestARefusalSaysNothingRatherThanGuessing is the other half, and the one that
// keeps the first honest.
//
// The rule the command path used matched on substring, so a caller who typed a
// word this tool does not have was offered every candidate containing it. Three
// unrelated suggestions read as an answer and cost a turn to rule out, which is
// worse than the bare refusal they replaced.
func TestARefusalSaysNothingRatherThanGuessing(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"a flag from another tool", []string{"issue", "list", "--porcelain"}},
		{"a verb that is not a typo of one", []string{"issue", "reticulate"}},
		{"a command name from nowhere", []string{"schema", "xyzzy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, nil, tc.args...)

			if got.exit == exitcode.OK {
				t.Fatalf("exit = 0\nstderr: %s", got.stderr)
			}
			if strings.Contains(got.stderr, "did you mean") {
				t.Errorf("a refusal guessed at what was meant:\n%s", got.stderr)
			}
			if element(got.stderr, "remedy") == "" {
				t.Errorf("a refusal with no suggestion also has no remedy:\n%s", got.stderr)
			}
		})
	}
}

// TestAnArgumentTypedAsAFlagSaysSo is the fifth refusal of the one mistake, and
// the one the other four left out.
//
// `jr sprint add --sprint 128 ENG-1` is not a spelling mistake: the word is
// declared, it is one token from where it belongs, and it is a positional
// argument. Ranked against the flags it finds nothing, and "nothing close" is
// the right answer to a typo. Here it reads as "no such thing", which is
// false.
//
// Asserted on a command in every build. `sprint add` is where this was found
// and it needs the write tag, so a test named for it would not run in the
// profile `make test` uses.
func TestAnArgumentTypedAsAFlagSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		usage string
	}{
		{
			name:  "a single required argument",
			args:  []string{"issue", "get", "--format", "json", "--key", "ENG-1"},
			usage: "jr issue get <key>",
		},
		{
			name:  "on another command entirely",
			args:  []string{"meta", "transitions", "--format", "json", "--key", "ENG-1"},
			usage: "jr meta transitions <key>",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// JSON, because the usage line carries angle brackets and XML
			// escapes them. Asserting on `&lt;key&gt;` would pin the encoding
			// rather than the sentence. It comes before the bad flag because
			// parsing stops at the bad flag, and a --format after it has not
			// been read when the refusal is rendered.
			got := run(t, nil, tc.args...)

			if got.exit != exitcode.Usage {
				t.Fatalf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.Usage, got.stderr)
			}
			if !strings.Contains(got.stderr, "is an argument, not a flag") {
				t.Errorf("the refusal did not say what the word is:\n%s", got.stderr)
			}
			// The usage line comes from the same declaration the check reads,
			// so a caller is shown where the word goes and not only that it is
			// in the wrong place.
			if !strings.Contains(got.stderr, tc.usage) {
				t.Errorf("stderr does not carry %q:\n%s", tc.usage, got.stderr)
			}
			// This is not a spelling correction and must not read as one.
			if strings.Contains(got.stderr, "did you mean") {
				t.Errorf("an exact argument match was offered as a guess:\n%s", got.stderr)
			}
			if !strings.Contains(got.stderr, "--help") {
				t.Errorf("the detail displaced the remedy:\n%s", got.stderr)
			}
		})
	}
}

// TestANearFlagStillBeatsAnArgumentThatIsNotOne keeps the new branch from
// swallowing the old one. The argument check is exact, so a typo of a real flag
// still gets the flag advice even on a command whose arguments have names.
func TestANearFlagStillBeatsAnArgumentThatIsNotOne(t *testing.T) {
	got := run(t, nil, "issue", "get", "--fild", "customfield_10042")

	if got.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.Usage, got.stderr)
	}
	detail := element(got.stderr, "detail")
	if !strings.Contains(detail, "--field") {
		t.Errorf("a near flag was not offered:\n%s", got.stderr)
	}
	if strings.Contains(detail, "is an argument") {
		t.Errorf("a typo was answered as though it named an argument:\n%s", got.stderr)
	}
}

// TestSuggestionsComeFromThisCommandsOwnFlags keeps the candidate set honest.
//
// Every flag jr has is the wrong set to rank against: `--worklog-after` is a
// real flag on `issue activity` and offering it to somebody mistyping something
// on `project list` is a second wrong turn wearing the clothes of help.
func TestSuggestionsComeFromThisCommandsOwnFlags(t *testing.T) {
	got := run(t, nil, "project", "list", "--worklog-aftr", "x")

	if got.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.Usage, got.stderr)
	}
	if strings.Contains(got.stderr, "worklog-after") {
		t.Errorf("a flag from another command was offered:\n%s", got.stderr)
	}
}

// element pulls one leaf out of an XML diagnostic. The tests above care which
// field a suggestion lands in, so reading the whole document is not enough.
func element(doc, name string) string {
	_, after, ok := strings.Cut(doc, "<"+name+">")
	if !ok {
		return ""
	}
	inner, _, ok := strings.Cut(after, "</"+name+">")
	if !ok {
		return ""
	}
	return inner
}
