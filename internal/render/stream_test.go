package render_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/render"
)

func streamSpec() render.StreamSpec {
	return render.StreamSpec{
		Kind:    "issue.list",
		Version: 1,
		Name:    "issues",
		Columns: []render.Column{
			{Header: "key", Path: "@key"},
			{Header: "summary", Path: "summary"},
			{Header: "status", Path: "status"},
			{Header: "assignee", Path: "assignee@display"},
			{Header: "updated", Path: "updated"},
		},
	}
}

// TestStreamedTSVIsByteIdenticalToBuffered is the property that makes streaming
// safe to adopt: the output contract does not change because the rows happened
// to leave the process at a different time.
func TestStreamedTSVIsByteIdenticalToBuffered(t *testing.T) {
	doc := sampleCollection(true)

	var buffered strings.Builder
	if err := render.Write(&buffered, doc, render.TSV); err != nil {
		t.Fatalf("buffered write: %v", err)
	}

	// Stream the same items, one page at a time, in a few different shapes.
	for _, pageSize := range []int{1, 2, 5} {
		var streamed strings.Builder
		s, err := render.NewStream(&streamed, render.TSV, streamSpec())
		if err != nil {
			t.Fatalf("new stream: %v", err)
		}
		items := doc.Collection.Items
		for i := 0; i < len(items); i += pageSize {
			if err := s.Write(items[i:min(i+pageSize, len(items))]...); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		if err := s.Close(true, ""); err != nil {
			t.Fatalf("close: %v", err)
		}
		if streamed.String() != buffered.String() {
			t.Errorf("page size %d differs from buffered\n--- buffered ---\n%s\n--- streamed ---\n%s",
				pageSize, buffered.String(), streamed.String())
		}
	}
}

// TestStructuredFormatsMatchBuffered asserts the formats that cannot stream
// still produce exactly what the buffered path produces.
func TestStructuredFormatsMatchBuffered(t *testing.T) {
	for _, f := range []render.Format{render.XML, render.JSON, render.YAML} {
		t.Run(string(f), func(t *testing.T) {
			doc := sampleCollection(true)

			var buffered strings.Builder
			if err := render.Write(&buffered, doc, f); err != nil {
				t.Fatalf("buffered: %v", err)
			}

			var streamed strings.Builder
			s, err := render.NewStream(&streamed, f, streamSpec())
			if err != nil {
				t.Fatalf("new stream: %v", err)
			}
			if render.Streams(f) {
				t.Fatalf("%s claims to stream, but its envelope needs the final count", f)
			}
			for _, item := range doc.Collection.Items {
				if err := s.Write(item); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if err := s.Close(true, ""); err != nil {
				t.Fatalf("close: %v", err)
			}
			if streamed.String() != buffered.String() {
				t.Errorf("streamed %s differs\n--- want ---\n%s\n--- got ---\n%s",
					f, buffered.String(), streamed.String())
			}
		})
	}
}

// TestRowsAppearBeforeClose is the whole point. A caller piping to head must see
// data without waiting for the last page.
func TestRowsAppearBeforeClose(t *testing.T) {
	var out strings.Builder
	s, err := render.NewStream(&out, render.TSV, streamSpec())
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}

	items := sampleCollection(true).Collection.Items
	if err := s.Write(items[0]); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "ENG-101") {
		t.Fatalf("the first row was buffered rather than written:\n%s", got)
	}
	if !strings.HasPrefix(got, "key\t") {
		t.Errorf("no header preceded the first row:\n%s", got)
	}
	// And nothing from a later page has appeared.
	if strings.Contains(got, "ENG-102") {
		t.Errorf("a row that was never written appeared:\n%s", got)
	}
}

// TestEmptyStreamStillHasAHeader covers a result with no rows: the caller asked
// for these columns, and an empty answer is still an answer.
func TestEmptyStreamStillHasAHeader(t *testing.T) {
	for _, f := range render.Formats() {
		var out strings.Builder
		s, err := render.NewStream(&out, f, streamSpec())
		if err != nil {
			t.Fatalf("new stream: %v", err)
		}
		if err := s.Close(true, ""); err != nil {
			t.Fatalf("close: %v", err)
		}
		if out.Len() == 0 {
			t.Errorf("%s: an empty result produced no output at all", f)
		}
		if f == render.TSV && !strings.HasPrefix(out.String(), "key\t") {
			t.Errorf("TSV: an empty result has no header:\n%s", out.String())
		}
	}
}

// TestTruncatedStreamCarriesItsToken checks the structured formats still report
// completeness correctly when the rows arrived incrementally.
func TestTruncatedStreamCarriesItsToken(t *testing.T) {
	var out strings.Builder
	s, err := render.NewStream(&out, render.XML, streamSpec())
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := s.Write(sampleCollection(true).Collection.Items[0]); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := s.Close(false, "eyJvIjoxfQ"); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, `complete="false"`) {
		t.Errorf("a truncated stream claims to be complete:\n%s", got)
	}
	if !strings.Contains(got, "eyJvIjoxfQ") {
		t.Errorf("the resume token was dropped:\n%s", got)
	}
}

// TestCompleteStreamRejectsAToken keeps the invariant that a complete result
// never offers a cursor, in the streaming path too.
func TestCompleteStreamRejectsAToken(t *testing.T) {
	var out strings.Builder
	s, err := render.NewStream(&out, render.TSV, streamSpec())
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := s.Close(true, "leftover"); err == nil {
		t.Fatal("a complete stream accepted a next-page token")
	}
}

func TestStreamValidatesItsSpecBeforeWriting(t *testing.T) {
	cases := map[string]render.StreamSpec{
		"no kind":         {Version: 1, Name: "issues", Columns: []render.Column{{Header: "k", Path: "@k"}}},
		"no version":      {Kind: "k", Name: "issues", Columns: []render.Column{{Header: "k", Path: "@k"}}},
		"no container":    {Kind: "k", Version: 1, Columns: []render.Column{{Header: "k", Path: "@k"}}},
		"no columns":      {Kind: "k", Version: 1, Name: "issues"},
		"bad column path": {Kind: "k", Version: 1, Name: "issues", Columns: []render.Column{{Header: "k", Path: "a@b/c"}}},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			if _, err := render.NewStream(&out, render.TSV, spec); err == nil {
				t.Fatal("a malformed spec was accepted")
			}
			if out.Len() != 0 {
				t.Errorf("bytes were written before the spec was rejected:\n%s", out.String())
			}
		})
	}
}

func TestStreamRejectsMixedItems(t *testing.T) {
	for _, f := range []render.Format{render.TSV, render.XML} {
		var out strings.Builder
		s, err := render.NewStream(&out, f, streamSpec())
		if err != nil {
			t.Fatalf("new stream: %v", err)
		}
		if err := s.Write(render.El("issue").Attr("key", "ENG-1")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := s.Write(render.El("epic").Attr("key", "ENG-2")); err == nil {
			t.Errorf("%s: a stream accepted two different item elements", f)
		}
	}
}

func TestStreamRejectsWritesAfterClose(t *testing.T) {
	var out strings.Builder
	s, err := render.NewStream(&out, render.TSV, streamSpec())
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := s.Close(true, ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Write(render.El("issue").Attr("key", "ENG-1")); err == nil {
		t.Error("a closed stream accepted a write")
	}
}

func TestStreamCounts(t *testing.T) {
	for _, f := range render.Formats() {
		var out strings.Builder
		s, err := render.NewStream(&out, f, streamSpec())
		if err != nil {
			t.Fatalf("new stream: %v", err)
		}
		for _, item := range sampleCollection(true).Collection.Items {
			if err := s.Write(item); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		if s.Count() != 2 {
			t.Errorf("%s: Count = %d, want 2", f, s.Count())
		}
		_ = s.Close(true, "")
	}
}
