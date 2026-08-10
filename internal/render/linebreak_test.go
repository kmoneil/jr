package render_test

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/render"
)

// lineBreakValues are the values whose bytes an XML processor rewrites on the
// way in, per §2.11: #xD#xA and any lone #xD become #xA, before parsing.
//
// A newline is here as the control: it is not altered, so it must survive
// without being escaped, and a fix that escaped it too would make every
// multi-line description unreadable while still passing a round-trip check.
var lineBreakValues = map[string]string{
	"lone CR":        "before\rafter",
	"CRLF":           "before\r\nafter",
	"trailing CR":    "trailing\r",
	"leading CR":     "\rleading",
	"CR beside ]]>":  "a\r]]>b",
	"newline":        "before\nafter",
	"CR then CR":     "a\r\rb",
	"nothing to fix": "ordinary text",
}

// TestALineBreakSurvivesTheParse asserts what a consumer reads, not what the
// writer emitted.
//
// This is the whole point of the test and the reason a golden file could not
// have caught the defect it pins: the bytes on disk were exactly what the
// writer intended, nothing was malformed, and the value only changed on the far
// side of somebody else's parser. `--format json` returned "before\rafter" and
// `--format xml` parsed back as "before\nafter" — one value meaning two things
// depending on a flag.
//
// Both XML paths are covered because they escape differently and both were
// wrong. Plain text uses a numeric reference; CDATA cannot, since a reference
// inside a section is five literal characters, so the section is closed and
// reopened around it.
func TestALineBreakSurvivesTheParse(t *testing.T) {
	for name, want := range lineBreakValues {
		t.Run(name, func(t *testing.T) {
			// Plain text and the attribute are exact: the writer emits the
			// value and nothing else around it, so anything but equality is a
			// character that did not survive.
			text, attr := parseXMLSummary(t, renderSummary(t, want, false))
			if text != want {
				t.Errorf("element text read back as %q, want %q", text, want)
			}
			if attr != want {
				t.Errorf("attribute read back as %q, want %q", attr, want)
			}

			// CDATA is containment rather than equality, and the reason is a
			// separate defect this test is deliberately not about: the writer
			// frames a section with a newline at each end and leaves the
			// closing tag's indentation in the element's content, so CDATA has
			// never round-tripped byte-for-byte. Asserting equality here would
			// make this test fail for that reason and read as if the line-break
			// fix were wrong.
			//
			// Containment is still enough to catch the defect it pins. A CR
			// that got normalised away leaves "before\rafter" absent from
			// "\nbefore\nafter\n", so the assertion fails on exactly the bug
			// and passes on the framing.
			cdata, _ := parseXMLSummary(t, renderSummary(t, want, true))
			if !strings.Contains(cdata, want) {
				t.Errorf("CDATA read back as %q, which does not carry %q", cdata, want)
			}
		})
	}
}

// renderSummary writes one record carrying the value in both an attribute and a
// leaf, as text or as CDATA.
func renderSummary(t *testing.T, value string, cdata bool) string {
	t.Helper()

	leaf := render.El("summary")
	if cdata {
		leaf.SetCDATA(value)
	} else {
		leaf.Text = value
	}
	var out strings.Builder
	doc := render.Record("probe", 1, render.El("issue").Attr("note", value).Child(leaf))
	if err := render.Write(&out, doc, render.XML); err != nil {
		t.Fatalf("write: %v", err)
	}
	return out.String()
}

// parseXMLSummary decodes the document the way a consumer would.
//
// Decoding rather than comparing bytes is the entire point. The bytes this
// writer produced were always exactly what it intended and nothing was
// malformed; the value changed inside somebody else's parser, which is why no
// golden file could have caught it.
func parseXMLSummary(t *testing.T, doc string) (text, attr string) {
	t.Helper()

	var parsed struct {
		Issue struct {
			Note    string `xml:"note,attr"`
			Summary string `xml:"summary"`
		} `xml:"issue"`
	}
	if err := xml.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("the document is not well-formed XML: %v\n%s", err, doc)
	}
	return parsed.Issue.Summary, parsed.Issue.Note
}

// TestNoFormatPutsARawCarriageReturnOnTheWire holds every format this build
// advertises to one rule, by two different arguments.
//
// The four contract formats escape it, because a consumer parses them and a raw
// CR does not survive: TSV because a record is one line, JSON and YAML through
// their own escaping, XML through `&#13;`. Only XML was ever wrong.
//
// `markdown` normalises it to a newline instead, and the reason is not
// fidelity — §3.2 puts it outside the contract and says not to parse it, so
// §2.11 never applied. It is the format meant for a terminal, and a CR returns
// the cursor to column 0, so "Closed as duplicate\rDO NOT MERGE" displays as
// the second half alone. Different argument, same requirement, so one sweep
// covers both rather than one carrying an exemption for the other.
//
// It sweeps Formats() rather than a list written here, and that is what found
// the markdown case in the first place: a hand-written list would have named
// the four formats the author was thinking about and reported clean.
func TestNoFormatPutsARawCarriageReturnOnTheWire(t *testing.T) {
	for _, f := range render.Formats() {
		t.Run(string(f), func(t *testing.T) {
			doc := render.Record("probe", 1,
				render.El("issue").
					Attr("note", "a\rb").
					Child(render.El("summary").SetCDATA("c\rd")))

			var out strings.Builder
			if err := render.Write(&out, doc, f); err != nil {
				t.Fatalf("write: %v", err)
			}
			if strings.Contains(out.String(), "\r") {
				t.Errorf("%s emitted a raw carriage return:\n%q", f, out.String())
			}
		})
	}
}

// TestMarkdownTurnsACarriageReturnIntoANewline says what markdown does, not
// merely what it stops doing.
//
// "No raw CR on the wire" is satisfied by dropping the character, and dropping
// it would join two lines into one — which is the same class of quiet
// alteration the whole sweep is about, just less visible. The value has to come
// out as the line break it meant.
//
// CRLF is the case worth having: collapsing it to two newlines would turn every
// line of a Windows-authored description into a paragraph break.
func TestMarkdownTurnsACarriageReturnIntoANewline(t *testing.T) {
	markdown, ok := formatNamed("markdown")
	if !ok {
		t.Skip("this build has no markdown format")
	}

	for name, tc := range map[string]struct{ in, want string }{
		"lone CR": {"before\rafter", "before\nafter"},
		"CRLF":    {"before\r\nafter", "before\nafter"},
		"CR runs": {"a\r\r\nb", "a\n\nb"},
	} {
		t.Run(name, func(t *testing.T) {
			doc := render.Record("probe", 1,
				render.El("issue").Child(render.El("summary").SetCDATA(tc.in)))

			var out strings.Builder
			if err := render.Write(&out, doc, markdown); err != nil {
				t.Fatalf("write: %v", err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("markdown does not carry %q:\n%q", tc.want, out.String())
			}
		})
	}
}

// TestNoFormatEmitsAC0ControlBeyondTabAndNewline is the backstop under the
// carriage-return rule.
//
// `renderable` already refuses every C0 character except tab, newline, and CR,
// so today this can only fail on CR — which is the point of writing it as the
// general claim rather than as a third CR test. If `xmlChar` ever widens, or a
// writer starts synthesising one, the format that reaches a terminal is covered
// without anybody remembering to come back here.
func TestNoFormatEmitsAC0ControlBeyondTabAndNewline(t *testing.T) {
	const hostile = "before\rafter\ttabbed\nnewline"

	for _, f := range render.Formats() {
		t.Run(string(f), func(t *testing.T) {
			doc := render.Record("probe", 1,
				render.El("issue").
					Attr("note", hostile).
					Child(render.El("summary").SetCDATA(hostile)))

			var out strings.Builder
			if err := render.Write(&out, doc, f); err != nil {
				t.Fatalf("write: %v", err)
			}
			for i, r := range out.String() {
				if r < 0x20 && r != '\t' && r != '\n' {
					t.Errorf("%s emitted U+%04X at byte %d", f, r, i)
				}
			}
		})
	}
}

// TestADiagnosticCarriesNoRawCarriageReturn covers the writer a document test
// cannot reach.
//
// Every format has two entry points — a result and a diagnostic — and markdown
// sets its normaliser in each, because a diagnostic does not go through
// writeMarkdown. Reverting the diagnostic line alone broke nothing until this
// existed, which is the whole reason it does: a line no test can fail is a line
// somebody will delete as dead.
//
// An error's detail is the natural home for a hostile value. `renderable`
// reports the offending text, `INVALID_ENCODING` quotes what the caller
// supplied, and `UNSAFE_FILENAME` prints the name the *server* chose — so the
// diagnostic path carries exactly the strings the result path is being guarded
// against.
func TestADiagnosticCarriesNoRawCarriageReturn(t *testing.T) {
	err := errs.NotFound("NO_THING", "no such thing").
		WithDetail("%s", "server said\rOVERWRITTEN").
		WithRemedy("check the key\rand try again")

	for _, f := range render.Formats() {
		t.Run(string(f), func(t *testing.T) {
			var out strings.Builder
			if werr := render.WriteError(&out, err, f); werr != nil {
				t.Fatalf("write: %v", werr)
			}
			if strings.Contains(out.String(), "\r") {
				t.Errorf("%s put a raw carriage return in a diagnostic:\n%q",
					f, out.String())
			}
		})
	}
}

// formatNamed finds a format this build advertises, so a test for a tagged one
// skips rather than failing in a build that does not carry it.
func formatNamed(name string) (render.Format, bool) {
	for _, f := range render.Formats() {
		if string(f) == name {
			return f, true
		}
	}
	return "", false
}

// TestJSONAndYAMLStillCarryACarriageReturn is the counterpart to the XML fix:
// the two formats that were already correct have to stay correct, or "every
// format agrees" would be satisfied by all four losing the value.
func TestJSONAndYAMLStillCarryACarriageReturn(t *testing.T) {
	const want = "before\rafter"
	doc := render.Record("probe", 1, render.El("issue").Leaf("summary", want))

	for _, tc := range []struct {
		format render.Format
		decode func([]byte, any) error
	}{
		{render.JSON, json.Unmarshal},
		{render.YAML, yaml.Unmarshal},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			var out strings.Builder
			if err := render.Write(&out, doc, tc.format); err != nil {
				t.Fatalf("write: %v", err)
			}
			var v any
			if err := tc.decode([]byte(out.String()), &v); err != nil {
				t.Fatalf("decode: %v\n%s", err, out.String())
			}
			if got := findSummary(v); got != want {
				t.Errorf("summary read back as %q, want %q", got, want)
			}
		})
	}
}

// findSummary walks a decoded document for the summary value.
func findSummary(v any) string {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "summary" {
				if s, ok := val.(string); ok {
					return s
				}
			}
			if s := findSummary(val); s != "" {
				return s
			}
		}
	case []any:
		for _, val := range x {
			if s := findSummary(val); s != "" {
				return s
			}
		}
	}
	return ""
}
