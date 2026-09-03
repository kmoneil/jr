//go:build write

package issue_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
)

// validateEditWith runs only the command's validation, which is where every
// refusal in this file lives. Nothing reaches Jira, so no stub is needed and a
// nil session is the honest setup: a validation that touched the network would
// fail here rather than pass quietly.
func validateEditWith(t *testing.T, args []string, flags map[string]string) error {
	t.Helper()

	cmd, ok := registry.Lookup("issue.edit")
	if !ok {
		t.Fatal("issue edit is not registered")
	}
	set := registry.NewFlags()
	for name, value := range flags {
		set.SetString(name, value)
	}
	return cmd.Validate(t.Context(), &registry.Invocation{
		Jira:  &stubSession{doer: &stubDoer{body: catalogueJSON}, project: "ENG"},
		Args:  args,
		Flags: set,
	})
}

// TestTheThreeModesRefuseEachOther covers every combination the command cannot
// honour, one row per refusal.
//
// It is a table rather than seven tests because the point is the *set*: these
// are the ways the three modes overlap, and a new flag that creates an eighth
// belongs here beside them. Each case asserts the code rather than the message,
// because the code is what a script branches on and the message is what a
// person reads.
func TestTheThreeModesRefuseEachOther(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		flags map[string]string
		want  string
	}{
		{
			// The rule that makes a plan the only path to a bulk write. Without
			// it the convenient spelling exists and nobody writes a plan again.
			name:  "several keys and no plan",
			args:  []string{"ENG-1", "ENG-2"},
			flags: map[string]string{"summary": "x"},
			want:  "BULK_NEEDS_A_PLAN",
		},
		{
			name:  "no keys at all",
			args:  nil,
			flags: map[string]string{"summary": "x"},
			want:  "NO_ISSUES",
		},
		{
			name:  "apply given keys as well",
			args:  []string{"ENG-1"},
			flags: map[string]string{"apply": "plan.xml"},
			want:  "PLAN_TAKES_NO_KEYS",
		},
		{
			// Whether the flag overrides the plan or the plan wins, the
			// document somebody reviewed stops being the change that runs.
			name:  "apply given a change as well",
			args:  nil,
			flags: map[string]string{"apply": "plan.xml", "summary": "x"},
			want:  "PLAN_CARRIES_THE_CHANGE",
		},
		{
			name:  "apply given a baseline as well",
			args:  nil,
			flags: map[string]string{"apply": "plan.xml", "if-unchanged": "eyJrIjoiRU5HLTEifQ"},
			want:  "PLAN_CARRIES_THE_BASELINE",
		},
		{
			name:  "writing and running a plan at once",
			args:  []string{"ENG-1"},
			flags: map[string]string{"apply": "a.xml", "plan-out": "b.xml"},
			want:  "CONFLICTING_PLAN_FLAGS",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEditWith(t, tc.args, tc.flags)
			if err == nil {
				t.Fatalf("accepted, want %s", tc.want)
			}
			e := errs.Coerce(err)
			if e.Code != tc.want {
				t.Errorf("code = %q, want %q", e.Code, tc.want)
			}
			if e.Exit != exitcode.Usage {
				t.Errorf("exit = %d, want %d: the caller typed this",
					e.Exit, exitcode.Usage)
			}
			if e.Remedy == "" {
				t.Error("no remedy: a refusal that does not say what to do " +
					"instead is a dead end with a helpful tone")
			}
		})
	}
}

// TestOneKeyStillEditsWithoutAPlan is the case every existing caller is in, and
// the one the refusals above must not have swallowed.
func TestOneKeyStillEditsWithoutAPlan(t *testing.T) {
	if err := validateEditWith(t, []string{"ENG-1"}, map[string]string{"summary": "x"}); err != nil {
		t.Fatalf("a single-issue edit was refused: %v", err)
	}
}

// TestAPlanRefusesMoreRowsThanAnybodyReads holds the cap at the boundary rather
// than somewhere inside it, because an off-by-one here either refuses a legal
// plan or accepts one the apply cannot honour.
func TestAPlanRefusesMoreRowsThanAnybodyReads(t *testing.T) {
	keys := make([]string, 0, issue.MaxPlanRows+1)
	for i := range issue.MaxPlanRows + 1 {
		keys = append(keys, "ENG-"+itoa(i+1))
	}

	if err := validateEditWith(t, keys[:issue.MaxPlanRows],
		map[string]string{"summary": "x", "plan-out": "p.xml"}); err != nil {
		t.Fatalf("a plan of exactly %d rows was refused: %v", issue.MaxPlanRows, err)
	}

	err := validateEditWith(t, keys, map[string]string{"summary": "x", "plan-out": "p.xml"})
	if err == nil {
		t.Fatalf("a plan of %d rows was accepted", len(keys))
	}
	if code := errs.Coerce(err).Code; code != "TOO_MANY_ISSUES" {
		t.Errorf("code = %q, want TOO_MANY_ISSUES", code)
	}
}

// TestDryRunAndPlanOutAreDifferentDocuments pins the one refusal that is about
// the output contract rather than about the request: a command emits one
// document, and these two flags each name a different one.
func TestDryRunAndPlanOutAreDifferentDocuments(t *testing.T) {
	cmd, _ := registry.Lookup("issue.edit")
	set := registry.NewFlags()
	set.SetString("summary", "x")
	set.SetString("plan-out", "p.xml")
	set.SetBool("dry-run", true)

	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{doer: &stubDoer{body: catalogueJSON}, project: "ENG"},
		Args: []string{"ENG-1"}, Flags: set,
	})
	if err == nil {
		t.Fatal("--dry-run and --plan-out were accepted together")
	}
	if code := errs.Coerce(err).Code; code != "CONFLICTING_PLAN_FLAGS" {
		t.Errorf("code = %q, want CONFLICTING_PLAN_FLAGS", code)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// The reader.
// ---------------------------------------------------------------------------

// TestAPlanThisToolDidNotWriteIsRefused walks the reader's refusals.
//
// This is the first parser in the tree, and what it accepts is what a bad file
// can make the tool do. Every case here is a document that is well-formed XML
// and wrong in exactly one way, because a reader that fails on garbage and
// succeeds on plausible-but-wrong input is the dangerous kind.
func TestAPlanThisToolDidNotWriteIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"not XML at all", `this is not a document`},
		{
			"another kind entirely",
			`<result kind="issue.get" v="1"><plan verb="issue.edit" count="1">` +
				`<change/><rows count="1"><row key="ENG-1" idempotency-key="auto-a"/>` +
				`</rows></plan></result>`,
		},
		{
			// A version this build does not know is refused rather than read
			// leniently: the version is how a consumer is told the shape moved.
			"a version this build does not know",
			`<result kind="issue.plan" v="99"><plan verb="issue.edit" count="1">` +
				`<change/><rows count="1"><row key="ENG-1" idempotency-key="auto-a"/>` +
				`</rows></plan></result>`,
		},
		{
			"a plan for a different verb",
			`<result kind="issue.plan" v="1"><plan verb="issue.move" count="1">` +
				`<change/><rows count="1"><row key="ENG-1" idempotency-key="auto-a"/>` +
				`</rows></plan></result>`,
		},
		{
			"no rows",
			`<result kind="issue.plan" v="1"><plan verb="issue.edit" count="0">` +
				`<change/><rows count="0"></rows></plan></result>`,
		},
		{
			"one issue twice",
			`<result kind="issue.plan" v="1"><plan verb="issue.edit" count="2">` +
				`<change/><rows count="2">` +
				`<row key="ENG-1" idempotency-key="auto-a"/>` +
				`<row key="eng-1" idempotency-key="auto-b"/>` +
				`</rows></plan></result>`,
		},
		{
			"an idempotency key the ledger cannot record",
			`<result kind="issue.plan" v="1"><plan verb="issue.edit" count="1">` +
				`<change/><rows count="1">` +
				`<row key="ENG-1" idempotency-key="../../etc/passwd"/>` +
				`</rows></plan></result>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := issue.ParsePlan(strings.NewReader(tc.doc)); err == nil {
				t.Fatal("accepted")
			} else if got := errs.Coerce(err).Exit; got != exitcode.Usage {
				t.Errorf("exit = %d, want %d: the caller named this file",
					got, exitcode.Usage)
			}
		})
	}
}

// TestAPlanCannotNameSomethingThatIsNotAnIssue is the security property, and
// the reason the plan carries intent rather than requests.
//
// A plan of raw requests would put a path in the file and send it. This reader
// takes a key, and every key goes through ParseKey, which
// FuzzParseKeyProducesASafePathSegment holds to producing something safe in a
// URL. A traversal attempt is refused at the reader and never reaches a path.
func TestAPlanCannotNameSomethingThatIsNotAnIssue(t *testing.T) {
	for _, key := range []string{
		"../../admin-1",
		"ENG-1/../../admin",
		"http://elsewhere.invalid/x-1",
		"",
		"ENG-1 OR 1=1",
	} {
		doc := `<result kind="issue.plan" v="1"><plan verb="issue.edit" count="1">` +
			`<change><summary>x</summary></change><rows count="1">` +
			`<row key="` + key + `" idempotency-key="auto-a"/></rows></plan></result>`

		plan, err := issue.ParsePlan(strings.NewReader(doc))
		if err == nil {
			t.Errorf("accepted %q as an issue key, and it reached a request as %q",
				key, plan.Rows[0].Key)
			continue
		}
		if code := errs.Coerce(err).Code; code != "INVALID_PLAN" {
			t.Errorf("%q refused with %q, want INVALID_PLAN", key, code)
		}
	}
}

// FuzzParsePlanAcceptsOnlyWhatIsSafeToApply is the postcondition on the first
// document reader this tool has ever had.
//
// Everything else in the tree writes its contract; this reads one back, and
// what it accepts becomes an edit sent with the caller's credential. So the
// property worth fuzzing is not "does not panic" but "anything accepted is
// something apply can safely execute": every row names an issue ParseKey
// accepts, which is what bounds the request path; every row carries an
// idempotency key the ledger can record, which is what bounds the file the
// ledger writes; no issue appears twice, because two rows for one issue claim
// one ledger entry and the second is reported skipped without anything having
// decided that; and the row count is inside the cap apply is willing to run.
//
// The seeds are the shapes that reach each check: a plan this tool wrote, and
// the plausible-but-wrong documents that get one step further than garbage.
func FuzzParsePlanAcceptsOnlyWhatIsSafeToApply(f *testing.F) {
	f.Add(renderPlan(&testing.T{}, planFixture()))
	f.Add("")
	f.Add("<result/>")
	f.Add(`<result kind="issue.plan" v="1"><plan verb="issue.edit" count="1">` +
		`<change><summary>x</summary></change><rows count="1">` +
		`<row key="ENG-1" idempotency-key="auto-a"/></rows></plan></result>`)
	f.Add(`<result kind="issue.plan" v="1"><plan verb="issue.edit" count="1">` +
		`<change/><rows count="1"><row key="../../admin-1" idempotency-key="auto-a"/>` +
		`</rows></plan></result>`)
	f.Add(`<result kind="issue.plan" v="1"><plan verb="issue.edit" count="2">` +
		`<change/><rows count="2"><row key="ENG-1" idempotency-key="auto-a"/>` +
		`<row key="ENG-1" idempotency-key="auto-b"/></rows></plan></result>`)

	f.Fuzz(func(t *testing.T, doc string) {
		plan, err := issue.ParsePlan(strings.NewReader(doc))
		if err != nil {
			// A refusal is the caller's mistake, always. One that reached a
			// conflict or a remote exit would be blaming the server for a file.
			if got := errs.Coerce(err).Exit; got != exitcode.Usage {
				t.Fatalf("refused at exit %d, want %d", got, exitcode.Usage)
			}
			return
		}

		if plan.Verb != "issue.edit" {
			t.Fatalf("accepted a plan for verb %q", plan.Verb)
		}
		if n := len(plan.Rows); n == 0 || n > issue.MaxPlanRows {
			t.Fatalf("accepted a plan of %d rows", n)
		}

		seen := map[string]bool{}
		for _, row := range plan.Rows {
			key, ok := issue.ParseKey(row.Key)
			if !ok {
				t.Fatalf("accepted row key %q, which is not an issue key", row.Key)
			}
			// The bound that matters: the key becomes a URL path segment, and
			// anything that could leave /issue/ would send the credential
			// somewhere nobody chose.
			if key.String() != row.Key {
				t.Fatalf("row key %q is not canonical, and %q is what reaches the path",
					row.Key, key)
			}
			if strings.ContainsAny(row.Key, "/?#%\\") {
				t.Fatalf("accepted row key %q, which is not safe in a path", row.Key)
			}
			if seen[row.Key] {
				t.Fatalf("accepted %s twice, so one ledger entry serves two rows", row.Key)
			}
			seen[row.Key] = true

			if err := idem.ValidateKey(row.IdempotencyKey); err != nil {
				t.Fatalf("accepted idempotency key %q: %v", row.IdempotencyKey, err)
			}
		}
	})
}
