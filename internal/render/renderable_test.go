package render_test

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/render"
)

// TestEveryWriterEmitsWhatItsOwnParserWillRead is the assertion none of the
// four had.
//
// Each writer was tested against a golden file, which pins what it produces and
// says nothing about whether a consumer can read it. A C0 control character in
// an issue summary made XML and TSV emit the raw byte while JSON and YAML
// encoded it — the same value rendering four ways and parsing two, with `jr`
// exiting 0 and reporting the result complete.
//
// Round-tripping through each format's real parser is what makes that visible,
// so it is asserted here for every format rather than for the one that broke.
func TestEveryWriterEmitsWhatItsOwnParserWillRead(t *testing.T) {
	for _, text := range []string{
		"ordinary",
		"tabs\tand\nnewlines\rare legal",
		"markup < & > \" ' and ]]> too",
		"unicode: é 日本語 🙂",
		"U+FFFD is a real character: �",
	} {
		t.Run(text[:min(len(text), 24)], func(t *testing.T) {
			for _, f := range render.Formats() {
				doc := render.Record("issue.get", 1,
					render.El("issue").Attr("key", text).Leaf("summary", text))

				var out strings.Builder
				if err := render.Write(&out, doc, f); err != nil {
					t.Fatalf("%s: write: %v", f, err)
				}
				if err := reparse(f, out.String()); err != nil {
					t.Errorf("%s emitted something its own parser rejects: %v\n%s",
						f, err, out.String())
				}
			}
		})
	}
}

// TestAValueNoFormatCanCarryIsRefused covers the other side. The characters
// below are the ones XML 1.0 forbids outright — not escapable, so refusing is
// the only option that does not quietly change the data.
func TestAValueNoFormatCanCarryIsRefused(t *testing.T) {
	for name, text := range map[string]string{
		"NUL":               "before\x00after",
		"SOH":               "before\x01after",
		"vertical tab":      "before\x0bafter",
		"form feed":         "before\x0cafter",
		"escape":            "before\x1bafter",
		"unit separator":    "before\x1fafter",
		"not a character":   "before￾after",
		"invalid UTF-8":     "before\xffafter",
		"lone continuation": "before\x80after",
	} {
		t.Run(name, func(t *testing.T) {
			// Once as element text, once as an attribute value: the XML writer
			// escapes those through different tables, and only one of them was
			// ever going to be remembered.
			for _, doc := range []*render.Doc{
				render.Record("issue.get", 1,
					render.El("issue").Attr("key", "ENG-1").Leaf("summary", text)),
				render.Record("issue.get", 1, render.El("issue").Attr("key", text)),
			} {
				var out strings.Builder
				err := render.Write(&out, doc, render.XML)
				if err == nil {
					t.Fatalf("accepted %q and emitted:\n%s", text, out.String())
				}
				e := errs.Coerce(err)
				if e.Code != "UNRENDERABLE_VALUE" {
					t.Errorf("code = %q, want UNRENDERABLE_VALUE", e.Code)
				}
				if e.Exit != exitcode.Error {
					t.Errorf("exit = %d, want %d", e.Exit, exitcode.Error)
				}
				if out.Len() != 0 {
					t.Errorf("bytes reached the writer before the refusal: %q", out.String())
				}
			}
		})
	}
}

// TestTheRefusalDoesNotDependOnTheFormat is the reason the check lives in
// validation and not in a writer.
//
// JSON and YAML can encode a control character, so leaving it to each writer
// would mean `--format` decided what this tool is willing to say. A value is
// representable or it is not, and the flag chooses an encoding.
func TestTheRefusalDoesNotDependOnTheFormat(t *testing.T) {
	for _, f := range render.Formats() {
		doc := render.Record("issue.get", 1,
			render.El("issue").Attr("key", "ENG-1").Leaf("summary", "x\x01y"))

		var out strings.Builder
		if err := render.Write(&out, doc, f); err == nil {
			t.Errorf("%s accepted a value XML cannot carry:\n%s", f, out.String())
		}
	}
}

// TestAStreamedRowIsHeldToTheSameRule covers the path that does not go through
// Write at all. A collection streams, so its rows are checked one at a time as
// they are produced; a rule enforced only on the buffered path would be a rule
// that lapsed for every list command.
func TestAStreamedRowIsHeldToTheSameRule(t *testing.T) {
	var out strings.Builder
	stream, err := render.NewStream(&out, render.TSV, render.StreamSpec{
		Kind: "issue.list", Version: 1, Name: "issues",
		Columns: []render.Column{{Header: "key", Path: "@key"}},
	})
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}

	if err := stream.Write(render.El("issue").Attr("key", "ENG-1")); err != nil {
		t.Fatalf("a clean row was refused: %v", err)
	}
	err = stream.Write(render.El("issue").Attr("key", "ENG-\x012"))
	if err == nil {
		t.Fatal("a streamed row carried a character no format can render")
	}
	if code := errs.Coerce(err).Code; code != "UNRENDERABLE_VALUE" {
		t.Errorf("code = %q, want UNRENDERABLE_VALUE", code)
	}
}

// reparse feeds a writer's output back to the parser a consumer would use.
func reparse(f render.Format, s string) error {
	switch f {
	case render.XML:
		d := xml.NewDecoder(strings.NewReader(s))
		for {
			_, err := d.Token()
			if err != nil {
				if err.Error() == "EOF" {
					return nil
				}
				return err
			}
		}
	case render.JSON:
		var v any
		return json.Unmarshal([]byte(s), &v)
	case render.YAML:
		var v any
		return yaml.Unmarshal([]byte(s), &v)
	case render.TSV:
		// TSV has no parser to borrow, so the contract it publishes is the
		// assertion: split on \n, split on \t, and every record is one line
		// with the same number of fields.
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		want := strings.Count(lines[0], "\t")
		for i, line := range lines {
			if got := strings.Count(line, "\t"); got != want {
				return errs.Runtime("TSV_SHAPE",
					"row %d has %d separators, header has %d", i, got, want)
			}
			for _, r := range line {
				if r < 0x20 && r != '\t' {
					return errs.Runtime("TSV_SHAPE",
						"row %d carries a raw control character U+%04X", i, r)
				}
			}
		}
	}
	return nil
}
