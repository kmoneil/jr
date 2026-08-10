package render_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/render"
)

// sampleCollection is the canonical rectangular payload: the shape a list
// command emits.
func sampleCollection(complete bool) *render.Doc {
	items := []*render.Node{
		render.El("issue").
			Attr("key", "ENG-101").
			Leaf("summary", "Retry logic drops the last error").
			Child(render.El("status").Attr("category", "in-progress").SetText("In Progress")).
			Child(render.El("assignee").Attr("id", "712020:8f3a").Attr("display", "Ada Lovelace")).
			Leaf("updated", "2026-08-04T11:32:07Z"),
		render.El("issue").
			Attr("key", "ENG-102").
			Leaf("summary", "Tabs\tand\nnewlines in a summary").
			Child(render.El("status").Attr("category", "done").SetText("Done")).
			Child(render.El("assignee").Attr("id", "").Attr("display", "")).
			Leaf("updated", "2026-08-04T09:00:00Z"),
	}
	c := &render.Collection{
		Name:     "issues",
		Items:    items,
		Complete: complete,
		Columns: []render.Column{
			{Header: "key", Path: "@key"},
			{Header: "summary", Path: "summary"},
			{Header: "status", Path: "status"},
			{Header: "assignee", Path: "assignee@display"},
			{Header: "updated", Path: "updated"},
		},
	}
	if !complete {
		c.NextPageToken = "eyJvIjoyfQ"
	}
	return render.List("issue.list", 1, c)
}

// sampleRecord is the canonical document payload: mixed content that would be
// an escape-sequence minefield in JSON.
func sampleRecord() *render.Doc {
	body := "## Repro\n\n```go\nclient.Do(req)  // returns err == nil on 5xx\n```\n\nAlso: a < b && c > d, and a literal ]]> in the text."
	n := render.El("issue").
		Attr("key", "ENG-101").
		Leaf("summary", `Retry logic drops the last error`).
		Child(render.El("status").Attr("category", "in-progress").SetText("In Progress")).
		Child(render.El("description").Attr("format", "markdown").SetCDATA(body)).
		Child(render.ListEl(
			"labels", "label",
			render.El("label").SetText("retry"),
			render.El("label").SetText("transport"),
		)).
		Child(render.ListEl("components", "component"))
	return render.Record("issue.get", 1, n)
}

func TestGoldenOutput(t *testing.T) {
	cases := []struct {
		name string
		doc  *render.Doc
	}{
		{"collection", sampleCollection(true)},
		{"collection-truncated", sampleCollection(false)},
		{"record", sampleRecord()},
	}

	for _, tc := range cases {
		for _, f := range render.Formats() {
			t.Run(tc.name+"/"+string(f), func(t *testing.T) {
				var b strings.Builder
				if err := render.Write(&b, tc.doc, f); err != nil {
					t.Fatalf("write: %v", err)
				}
				assertGolden(t, tc.name+"."+string(f), b.String())
			})
		}
	}
}

func TestGoldenDiagnostics(t *testing.T) {
	e := errs.Usage("JQL_SYNTAX", "Unclosed quote in --jql at position 34").
		WithDetail(`project = ENG AND summary ~ "unclosed`).
		WithRemedy("Quote the whole expression in single quotes, or escape inner double quotes.").
		WithRequestID("2f1c9a4e-0b77-4f0e-9d3a-1a2b3c4d5e6f")

	for _, f := range render.Formats() {
		t.Run("error/"+string(f), func(t *testing.T) {
			var b strings.Builder
			if err := render.WriteError(&b, e, f); err != nil {
				t.Fatalf("write error: %v", err)
			}
			assertGolden(t, "error."+string(f), b.String())
		})
		t.Run("warning/"+string(f), func(t *testing.T) {
			var b strings.Builder
			if err := render.WriteTruncationWarning(&b, sampleCollection(false), f); err != nil {
				t.Fatalf("write warning: %v", err)
			}
			assertGolden(t, "warning."+string(f), b.String())
		})
	}
}

// TestTruncationIsAlwaysVisible is the invariant the whole format exists for: a
// truncated result never claims to be complete, in any format.
func TestTruncationIsAlwaysVisible(t *testing.T) {
	doc := sampleCollection(false)
	if doc.IsComplete() {
		t.Fatal("truncated document reports itself complete")
	}
	for _, f := range render.Formats() {
		var b strings.Builder
		if err := render.Write(&b, doc, f); err != nil {
			t.Fatalf("%s: write: %v", f, err)
		}
		out := b.String()

		switch f {
		case render.TSV:
			// TSV carries no envelope, so the signal is the stderr warning and
			// exit 3 rather than anything in the payload.
			var warn strings.Builder
			if err := render.WriteTruncationWarning(&warn, doc, f); err != nil {
				t.Fatalf("%s: write warning: %v", f, err)
			}
			if !strings.Contains(warn.String(), render.TruncatedCode) {
				t.Errorf("%s: truncation warning does not carry %s", f, render.TruncatedCode)
			}
		default:
			if strings.Contains(out, "complete=\"true\"") || strings.Contains(out, `"complete": true`) ||
				strings.Contains(out, "complete: true") {
				t.Errorf("%s: truncated result claims complete=true:\n%s", f, out)
			}
			if !strings.Contains(out, "eyJvIjoyfQ") {
				t.Errorf("%s: truncated result omits the next page token:\n%s", f, out)
			}
		}
	}
}

// TestCompleteResultCarriesNoToken is the converse: an exhaustive result must
// not offer a cursor, because there is nothing to resume.
func TestCompleteResultCarriesNoToken(t *testing.T) {
	doc := sampleCollection(true)
	doc.Collection.NextPageToken = "leftover"
	if err := doc.Validate(); err == nil {
		t.Fatal("a complete result carrying a next-page token was accepted")
	}
}

func TestTSVEscaping(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"a\tb", `a\tb`},
		{"a\nb", `a\nb`},
		{"a\r\nb", `a\r\nb`},
		{`a\b`, `a\\b`},
		{"a\\tb", `a\\tb`},
	}
	for _, tc := range cases {
		doc := render.Record("t", 1, render.El("t").Leaf("v", tc.in))
		var b strings.Builder
		if err := render.Write(&b, doc, render.TSV); err != nil {
			t.Fatalf("write: %v", err)
		}
		line := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")[1]
		_, got, _ := strings.Cut(line, "\t")
		if got != tc.want {
			t.Errorf("escape(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.Count(b.String(), "\n") != 2 {
			t.Errorf("escape(%q) produced more than one data row:\n%s", tc.in, b.String())
		}
	}
}

func TestCDATATerminatorIsSplit(t *testing.T) {
	doc := render.Record("t", 1, render.El("t").
		Child(render.El("body").SetCDATA("before ]]> after")))
	var b strings.Builder
	if err := render.Write(&b, doc, render.XML); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := b.String()
	if strings.Contains(got, "before ]]> after") {
		t.Errorf("a literal ]]> survived into the CDATA section:\n%s", got)
	}
	if !strings.Contains(got, "]]]]><![CDATA[>") {
		t.Errorf("CDATA terminator was not split:\n%s", got)
	}
}

// TestACDATAValueCarriesNoFraming asserts the value a consumer parses out of a
// CDATA leaf is the value that went in, with nothing added at either end.
//
// The writer used to put a newline after `<![CDATA[` and one before `]]>`, and
// then indent the closing tag. All three are element content, so `<summary>`
// holding "before" parsed back as "\nbefore\n    " — and CDATA is the path that
// carries descriptions, comment bodies, worklog comments, and dry-run request
// bodies, the longest and least replaceable values this tool emits. An
// attribute round-tripped exactly and plain element text round-tripped exactly;
// this was the one leaf that did not.
//
// Every value here ends in whitespace on one side or the other, because that is
// what framing made unreadable: with two newlines added at each end there is no
// way for a consumer to tell a value that genuinely starts with a blank line
// from one that does not, and a documented "strip one newline at each end"
// would have destroyed the former to recover the latter. Equality after
// decoding is the only assertion that separates them.
func TestACDATAValueCarriesNoFraming(t *testing.T) {
	for name, want := range map[string]string{
		"no whitespace at all": "before",
		"leading newline":      "\nbefore",
		"trailing newline":     "before\n",
		"newline at both ends": "\nbefore\n",
		"leading spaces":       "    indented",
		"trailing spaces":      "trailed    ",
		"blank line inside":    "## Repro\n\nclient.Do(req)\n",
		"only whitespace":      "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := parseXMLSummary(t, renderSummary(t, want, true))
			if got != want {
				t.Errorf("CDATA read back as %q, want %q", got, want)
			}
		})
	}
}

func TestXMLAttributeEscaping(t *testing.T) {
	doc := render.Record("t", 1, render.El("t").
		Attr("v", "a\tb\nc \"d\" & <e> 'f'"))
	var b strings.Builder
	if err := render.Write(&b, doc, render.XML); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := b.String()
	for _, forbidden := range []string{"\t", "\"d\""} {
		if strings.Contains(strings.SplitN(got, "\n", 3)[2], forbidden) {
			t.Errorf("attribute value was not escaped (%q survived):\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, "&#9;") || !strings.Contains(got, "&#10;") {
		t.Errorf("attribute whitespace was not escaped as entities:\n%s", got)
	}
}

func TestLookup(t *testing.T) {
	n := render.El("issue").
		Attr("key", "ENG-1").
		Leaf("summary", "s").
		Child(render.El("status").Attr("category", "done").SetText("Done")).
		Child(render.El("parent").Child(render.El("epic").Attr("key", "ENG-9")))

	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{"@key", "ENG-1", true},
		{"summary", "s", true},
		{"status", "Done", true},
		{"status@category", "done", true},
		{"parent/epic@key", "ENG-9", true},
		{"missing", "", false},
		{"@missing", "", false},
		{"status@missing", "", false},
		{"parent/missing@key", "", false},
	}
	for _, tc := range cases {
		got, ok := n.Lookup(tc.path)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Lookup(%q) = (%q, %v), want (%q, %v)", tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidatePath(t *testing.T) {
	valid := []string{"@key", "summary", "status@category", "parent/epic@key", "a/b/c"}
	for _, p := range valid {
		if err := render.ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", p, err)
		}
	}
	invalid := []string{"", "a//b", "a@b/c", "1bad", "a b", "a/"}
	for _, p := range invalid {
		if err := render.ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q) = nil, want an error", p)
		}
	}
}

func TestValidateRejectsMalformedDocuments(t *testing.T) {
	cases := map[string]*render.Doc{
		"no kind":    {Version: 1, Record: render.El("x")},
		"no version": {Kind: "k", Record: render.El("x")},
		"no payload": {Kind: "k", Version: 1},
		"both payloads": {
			Kind: "k", Version: 1,
			Record:     render.El("x"),
			Collection: &render.Collection{Name: "xs", Columns: []render.Column{{Header: "h", Path: "@a"}}},
		},
		"no columns": {
			Kind: "k", Version: 1,
			Collection: &render.Collection{Name: "xs", Complete: true},
		},
		"bad column path": {
			Kind: "k", Version: 1,
			Collection: &render.Collection{
				Name: "xs", Complete: true,
				Columns: []render.Column{{Header: "h", Path: "a@b/c"}},
			},
		},
		"mixed item elements": {
			Kind: "k", Version: 1,
			Collection: &render.Collection{
				Name:     "xs",
				Complete: true,
				Columns:  []render.Column{{Header: "h", Path: "@a"}},
				Items:    []*render.Node{render.El("x"), render.El("y")},
			},
		},
		"attribute and child collide": {
			Kind: "k", Version: 1,
			Record: render.El("x").Attr("dup", "1").Leaf("dup", "2"),
		},
		"repeated attribute": {
			Kind: "k", Version: 1,
			Record: render.El("x").Attr("a", "1").Attr("a", "2"),
		},
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := doc.Validate(); err == nil {
				t.Fatal("Validate accepted a malformed document")
			}
			var b strings.Builder
			if err := render.Write(&b, doc, render.XML); err == nil {
				t.Fatal("Write emitted a malformed document")
			}
			if b.Len() != 0 {
				t.Fatalf("Write emitted bytes before failing:\n%s", b.String())
			}
		})
	}
}

func TestListCountCannotLie(t *testing.T) {
	bad := render.El("x").Child(
		render.El("labels").Attr("count", "5").
			Child(render.El("label").SetText("one")),
	)
	bad.Children[0].ListOf = "label"
	doc := render.Record("k", 1, bad)
	if err := doc.Validate(); err == nil {
		t.Fatal("a list container whose count disagreed with its children was accepted")
	}
}

func TestParseFormat(t *testing.T) {
	for _, f := range render.Formats() {
		got, err := render.ParseFormat(string(f))
		if err != nil || got != f {
			t.Errorf("ParseFormat(%q) = (%q, %v)", f, got, err)
		}
	}
	if _, err := render.ParseFormat("  XML "); err != nil {
		t.Errorf("ParseFormat is not case- and space-insensitive: %v", err)
	}
	_, err := render.ParseFormat("csv")
	if err == nil {
		t.Fatal("ParseFormat accepted an unsupported format")
	}
	if errs.ExitOf(err) != exitcode.Usage {
		t.Errorf("unknown format exits %v, want %v", errs.ExitOf(err), exitcode.Usage)
	}
}

func TestDefaultFormatFollowsContentShape(t *testing.T) {
	if got := render.DefaultFor(sampleCollection(true)); got != render.TSV {
		t.Errorf("collections default to %q, want tsv", got)
	}
	if got := render.DefaultFor(sampleRecord()); got != render.XML {
		t.Errorf("records default to %q, want xml", got)
	}
	if got := render.DefaultFor(nil); got != render.XML {
		t.Errorf("diagnostics default to %q, want xml", got)
	}
}

// TestAColumnOverAListFlattens covers a rule the output contract has always
// stated and nothing implemented.
//
// "TSV has one cell per column, so a column over a list flattens: values are
// joined with `,`" has been in docs/output-contract.md since it was written,
// and JoinList was written for it — with exactly one caller, a resource
// pre-joining an *attribute* by hand. No column in the tree had ever addressed
// a list, so a path naming a list container resolved to the container's own
// text, which is empty, and the cell came out blank. `--field labels` is what
// finally needed one.
func TestAColumnOverAListFlattens(t *testing.T) {
	n := render.El("issue").
		Child(render.ListEl(
			"labels", "label",
			render.El("label").SetText("transport"),
			render.El("label").SetText("retry"),
		))

	got, ok := n.Lookup("labels")
	if !ok {
		t.Fatal("a path naming a list container resolved to nothing")
	}
	if got != "transport,retry" {
		t.Errorf("labels = %q, want the values joined", got)
	}
	// The separator is meaningful, so a value containing one is escaped before
	// it is joined — otherwise a consumer splitting the cell gets three labels.
	comma := render.El("issue").
		Child(render.ListEl(
			"labels", "label",
			render.El("label").SetText("a,b"),
			render.El("label").SetText(`c\d`),
		))
	if got, _ := comma.Lookup("labels"); got != `a\,b,c\\d` {
		t.Errorf("labels = %q, want the separator and the escape escaped", got)
	}
}

// TestAContainerThatIsNotAListDoesNotFlatten keeps the rule narrow.
//
// An issue has children too. A column path naming one has to keep resolving to
// nothing rather than to a joined blob of the whole record, or a mistyped path
// starts returning something that looks like data.
func TestAContainerThatIsNotAListDoesNotFlatten(t *testing.T) {
	n := render.El("result").
		Child(render.El("issue").
			Attr("key", "ENG-1").
			Leaf("summary", "s"))

	if got, _ := n.Lookup("issue"); got != "" {
		t.Errorf("issue = %q, want nothing: its children are not a list", got)
	}
	// An empty list is empty, not absent: the path resolves and the cell is
	// blank, which is what "no labels" looks like.
	empty := render.El("issue").Child(render.ListEl("labels", "label"))
	got, ok := empty.Lookup("labels")
	if !ok || got != "" {
		t.Errorf("empty labels = %q, %v; want an empty cell that resolved", got, ok)
	}
}
