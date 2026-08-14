package render_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/render"
)

// TestTheEnvelopeNamesTheSiteItCameFrom is the whole point of Doc.Site.
//
// An answer stored in a file outlives the shell that produced it, and two
// answers from two Jiras were indistinguishable once both were on disk. The
// case that raised it was a command running against the wrong instance and
// returning a well-formed, complete, exit-0 document about it, with nothing
// anywhere in the output to say which instance that was.
func TestTheEnvelopeNamesTheSiteItCameFrom(t *testing.T) {
	const site = "https://one.invalid/jira"

	for _, tc := range []struct {
		name string
		doc  *render.Doc
	}{
		{"collection", sampleCollection(true)},
		{"record", sampleRecord()},
	} {
		tc.doc.Site = site
		for _, f := range []render.Format{render.XML, render.JSON, render.YAML} {
			t.Run(tc.name+"/"+string(f), func(t *testing.T) {
				var buf strings.Builder
				if err := render.Write(&buf, tc.doc, f); err != nil {
					t.Fatalf("write: %v", err)
				}
				if !strings.Contains(buf.String(), site) {
					t.Errorf("no site in the envelope:\n%s", buf.String())
				}
			})
		}
	}
}

// TestTwoSitesProduceTwoDocuments is the card's case, reduced to what a
// consumer can see.
//
// Before this, the two were byte-identical. The point is not that the attribute
// exists; it is that it differs, which is the only thing that makes a stored
// document traceable.
func TestTwoSitesProduceTwoDocuments(t *testing.T) {
	render1, render2 := sampleCollection(true), sampleCollection(true)
	render1.Site = "https://one.invalid"
	render2.Site = "https://two.invalid"

	for _, f := range []render.Format{render.XML, render.JSON, render.YAML} {
		var a, b strings.Builder
		if err := render.Write(&a, render1, f); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := render.Write(&b, render2, f); err != nil {
			t.Fatalf("write: %v", err)
		}
		if a.String() == b.String() {
			t.Errorf("%s: two sites produced identical documents:\n%s", f, a.String())
		}
	}
}

// TestAnAbsentSiteIsAbsentRatherThanEmpty keeps the attribute meaningful.
//
// A command that reached no Jira has no site to name, and `site=""` would read
// as a site that is the empty string. A field whose absence is meaningful needs
// to be absent.
func TestAnAbsentSiteIsAbsentRatherThanEmpty(t *testing.T) {
	for _, f := range []render.Format{render.XML, render.JSON, render.YAML} {
		var buf strings.Builder
		if err := render.Write(&buf, sampleRecord(), f); err != nil {
			t.Fatalf("write: %v", err)
		}
		if strings.Contains(buf.String(), "site") {
			t.Errorf("%s: a document from no site mentions one:\n%s", f, buf.String())
		}
	}
}

// TestTSVIsUnchangedByProvenance holds the format that cannot carry it.
//
// TSV has no envelope, which is why truncation there is a stderr warning plus
// exit 3 rather than `complete="false"`. Provenance has the same limit, and the
// answer is that TSV output is byte-identical with a site and without one — not
// a column, which would change a default column set and be major, and not a
// comment line, which `cut -f1` would read as data.
func TestTSVIsUnchangedByProvenance(t *testing.T) {
	with, without := sampleCollection(true), sampleCollection(true)
	with.Site = "https://one.invalid"

	var a, b strings.Builder
	if err := render.Write(&a, with, render.TSV); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := render.Write(&b, without, render.TSV); err != nil {
		t.Fatalf("write: %v", err)
	}
	if a.String() != b.String() {
		t.Errorf("TSV changed:\n with:\n%s\n without:\n%s", a.String(), b.String())
	}
}

// TestAStreamedCollectionCarriesTheSite covers the other way a collection
// reaches stdout.
//
// A buffered format goes through Stream.Close, which builds the document the
// writer sees, so a spec that carried the site and a Close that dropped it
// would leave every list command unlabelled while every record command was
// labelled — and no test of Write would notice.
func TestAStreamedCollectionCarriesTheSite(t *testing.T) {
	const site = "https://streamed.invalid"

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.XML, render.StreamSpec{
		Kind: "issue.list", Version: 1, Name: "issues",
		Columns: []render.Column{{Header: "key", Path: "@key"}},
		Site:    site,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if err := stream.Write(render.El("issue").Attr("key", "ENG-1")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := stream.Close(true, ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(buf.String(), site) {
		t.Errorf("a streamed collection does not name its site:\n%s", buf.String())
	}
}
