package render_test

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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

// presentationOnly are formats that carry no parse promise, and so are not
// held to this file's claim, with the reason each one is exempt.
//
// `markdown` emits a raw carriage return today. That is not a fidelity defect,
// because §"`markdown` is presentation, and carries no promise" says in as many
// words that it is outside the contract and must not be parsed — line-end
// normalization is a rule about parsers, and nothing parses this.
//
// It is a *display* concern instead, and a real one: markdown is the format
// meant for a terminal, a CR returns the cursor to column 0, and an issue
// summary is written by whoever can file a ticket. Carded separately as
// backlog/markdown-passes-a-carriage-return-to-the-terminal.md, because it has
// a different audience and a different remedy from the escaping this file pins.
//
// Listed rather than skipped by name so the exemption is a decision somebody
// made, and so a second tagged format cannot inherit it silently.
var presentationOnly = map[render.Format]string{
	render.Format("markdown"): "presentation only; nothing parses it, so §2.11 " +
		"does not apply. The terminal-display half is its own card",
}

// TestNoFormatPutsARawCarriageReturnOnTheWire is the cheaper sibling, and it
// covers the formats whose parsers are not XML's.
//
// TSV escapes a CR because a record is one line; JSON and YAML encode it
// because their own escaping already does. None of them was broken, and the
// assertion exists so a change to any writer cannot make one of them the odd
// one out — which is exactly how XML became it.
//
// It sweeps Formats() rather than a list written here, which is what found the
// markdown case: a hand-written list would have named the four the author was
// thinking about and reported clean.
func TestNoFormatPutsARawCarriageReturnOnTheWire(t *testing.T) {
	for _, f := range render.Formats() {
		if reason, exempt := presentationOnly[f]; exempt {
			t.Logf("%s is exempt: %s", f, reason)
			continue
		}
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
