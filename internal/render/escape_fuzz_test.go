package render_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/render"
)

// The escaping rules of the output contract had no fuzzer.
//
// Twenty targets covered every parser in the tree and every function whose
// output reaches a URL path segment. These do not parse, they emit, so they
// fell outside the wording while sitting at the centre of what the wording was
// protecting: the values reaching them come from Jira, which is the source
// everything else in this tree treats as untrusted.
//
// All three drive the public API rather than reaching for the unexported
// escapers, because what has to hold is what a consumer sees.

// unescapeTSVCell is the consumer side of the TSV escaping rule, written out.
//
// It is here rather than in the package because it is the *reader's* half, and
// until now it existed only as prose in a doc comment and as whatever each
// consumer wrote for itself. A round trip needs both halves, and writing this
// one is how the rule gets checked rather than described.
func unescapeTSVCell(t *testing.T, s string) string {
	t.Helper()

	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		default:
			t.Fatalf("a TSV cell carries the escape %q, which the contract does not define", s[i-1:i+1])
		}
	}
	return b.String()
}

// FuzzTSVCellRoundTrips is the promise TSV makes: every record on one line and
// every field in one column, so a consumer splits on \t and \n with no
// defensive code.
//
// The property has two halves and both matter. A cell holds no bare separator,
// which is what makes splitting safe, and unescaping returns exactly what went
// in, which is what makes the value trustworthy after it.
func FuzzTSVCellRoundTrips(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "a\tb", "a\nb", "a\r\nb", `a\tb`, `\`, `\\`, `\n`,
		"trailing\\", "ünïcøde", "emoji 🙂", strings.Repeat("\t", 8),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		var out strings.Builder
		if err := render.Write(&out, cellDoc(value), render.TSV); err != nil {
			// Refused rather than altered, which is the other rule and is
			// FuzzRenderableIsExactlyWhatXMLAllows's business.
			return
		}

		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("one row became %d lines: %q", len(lines), out.String())
		}
		cell := lines[1]
		if strings.ContainsAny(cell, "\t\n\r") {
			t.Fatalf("a cell carries a bare separator: %q", cell)
		}
		if got := unescapeTSVCell(t, cell); got != value {
			t.Fatalf("round trip: %q became %q", value, got)
		}
	})
}

// FuzzJoinListSurvivesTheCell is the composition, which is where a rule with
// two escapers goes wrong.
//
// JoinList escapes a comma and a backslash, and then the TSV writer escapes the
// backslashes it just added. A consumer unescapes the cell first and then
// splits on a comma not preceded by one. Neither half was ever checked against
// the other, and the failure this prevents is exact: a status named "Ready,
// Set" quietly becoming two statuses.
func FuzzJoinListSurvivesTheCell(f *testing.F) {
	for _, seed := range [][3]string{
		{"To Do", "In Progress", "Done"},
		{"Ready, Set", "Go", ""},
		{`back\slash`, "plain", "a,b"},
		{`\,`, `,\`, `\\,,`},
		{"", "", ""},
	} {
		f.Add(seed[0], seed[1], seed[2])
	}

	f.Fuzz(func(t *testing.T, a, b, c string) {
		values := []string{a, b, c}

		var out strings.Builder
		if err := render.Write(&out, cellDoc(render.JoinList(values)), render.TSV); err != nil {
			return
		}

		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("one row became %d lines: %q", len(lines), out.String())
		}

		got := splitList(unescapeTSVCell(t, lines[1]))
		if len(got) != len(values) {
			t.Fatalf("%q split into %d values, want %d: %q", lines[1], len(got), len(values), got)
		}
		for i := range values {
			if got[i] != values[i] {
				t.Fatalf("value %d: %q became %q (cell %q)", i, values[i], got[i], lines[1])
			}
		}
	})
}

// splitList is the consumer side of JoinList: split on a comma that is not
// escaped, then undo the escaping within each value.
func splitList(s string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case s[i] == ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(out, cur.String())
}

// FuzzRenderableIsExactlyWhatXMLAllows holds the refusal to a definition
// written independently of the one it guards.
//
// xmlChar in the package is the production; xmlSafe below is the same rule
// written from XML 1.0 rather than from that function, on the same reasoning
// the scrubber's residue check is kept separate from the scrubber: a guard that
// shares a definition with the thing it guards cannot catch that definition
// being wrong.
//
// The interesting edge is the one the package comment already names. A range
// over a string yields U+FFFD for an invalid byte and U+FFFD is itself legal,
// so the two are told apart by how much was consumed rather than by the rune,
// and that is exactly the kind of distinction a fuzzer finds an exception to.
func FuzzRenderableIsExactlyWhatXMLAllows(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "\x00", "\x1f", "\t\n\r", "�", "￾", "￿",
		// DEL and the C1 block are legal despite looking like control
		// characters, which is the half of the production that is wider
		// than "printable" rather than narrower.
		"\x7f", "\u0085", "\U0010ffff", "ok\x07here", "\xed\xa0\x80", "\xff",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		var out strings.Builder
		err := render.Write(&out, cellDoc(value), render.TSV)

		if want := xmlSafe(value); (err == nil) != want {
			t.Fatalf("value %q: renderable=%v, XML 1.0 says %v (err %v)",
				value, err == nil, want, err)
		}
		if err != nil && out.Len() != 0 {
			t.Fatalf("a refused value produced %d bytes", out.Len())
		}
	})
}

// xmlSafe reports whether every rune of s is in XML 1.0's Char production and
// s is valid UTF-8. Written from the specification, not from xmlChar.
func xmlSafe(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == 0x9 || r == 0xA || r == 0xD:
		case r >= 0x20 && r <= 0xD7FF:
		case r >= 0xE000 && r <= 0xFFFD:
		case r >= 0x10000 && r <= 0x10FFFF:
		default:
			return false
		}
	}
	return true
}

// cellDoc is one row with one column, so a value's whole journey to a TSV cell
// is one call.
func cellDoc(value string) *render.Doc {
	return render.List("fuzz.cell", 1, &render.Collection{
		Name:     "items",
		Items:    []*render.Node{render.El("item").Attr("v", value)},
		Columns:  []render.Column{{Header: "v", Path: "@v"}},
		Complete: true,
	})
}
