package render_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/render"
)

// TestEveryFormatRefusesTheSameCollection is the assertion that was missing
// beside TestStreamedTSVIsByteIdenticalToBuffered.
//
// That one compares what the streamed and buffered paths *render*. Nothing
// compared what they *refuse*, and they disagreed: two differently named
// elements handed to one Write call went out as TSV and were rejected as XML,
// because Stream.check read counters the same function advances afterwards, so
// every item of a first batch saw an empty stream. The same document was
// accepted or rejected by --format, which is the one thing the format is not
// allowed to decide.
//
// Every case here is a malformed *first* batch, which is the only point where
// the two paths can be held to the same standard: a streamed row is bytes on
// stdout the moment it is written, so a refusal in a later batch necessarily
// arrives after some output. That trade is documented and is not what this
// tests.
func TestEveryFormatRefusesTheSameCollection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []*render.Node
	}{{
		name: "mixed elements in one call",
		items: []*render.Node{
			sampleIssue("ENG-101"),
			render.El("sprint").Attr("key", "ENG-102"),
		},
	}, {
		name:  "a nil item",
		items: []*render.Node{nil},
	}, {
		name: "a nil item after a good one",
		items: []*render.Node{
			sampleIssue("ENG-101"),
			nil,
		},
	}, {
		name: "an attribute and a child sharing a name",
		items: []*render.Node{
			sampleIssue("ENG-101").Attr("summary", "also an attribute"),
		},
	}, {
		name: "text no format can carry",
		items: []*render.Node{
			sampleIssue("ENG-101").Leaf("note", "a bell\x07here"),
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, f := range []render.Format{render.TSV, render.XML, render.JSON, render.YAML} {
				var out strings.Builder
				s, err := render.NewStream(&out, f, streamSpec())
				if err != nil {
					t.Fatalf("%s: new stream: %v", f, err)
				}

				// Refused at either end counts. The streamed path answers at
				// Write and the buffered one at Close, and which of the two it
				// is is not the caller's business.
				writeErr := s.Write(tc.items...)
				closeErr := s.Close(true, "")

				if writeErr == nil && closeErr == nil {
					t.Errorf("%s accepted it, and produced:\n%s", f, out.String())
				}
				if out.Len() != 0 {
					t.Errorf("%s wrote %d bytes for a collection it refused:\n%s",
						f, out.Len(), out.String())
				}
			}
		})
	}
}

// TestMixedElementsAreRefusedHoweverTheyArrive covers the batching, which is
// what the defect turned on: one call and two calls have to reach the same
// verdict, because a resource choosing to write a page at a time rather than a
// row at a time is a decision about requests and not about the contract.
func TestMixedElementsAreRefusedHoweverTheyArrive(t *testing.T) {
	first := sampleIssue("ENG-101")
	second := render.El("sprint").Attr("key", "ENG-102")

	for _, f := range []render.Format{render.TSV, render.XML, render.JSON, render.YAML} {
		t.Run(string(f)+"/one call", func(t *testing.T) {
			var out strings.Builder
			s, err := render.NewStream(&out, f, streamSpec())
			if err != nil {
				t.Fatalf("new stream: %v", err)
			}
			if err := s.Write(first, second); err == nil {
				t.Error("two elements in one call were accepted")
			}
		})

		t.Run(string(f)+"/two calls", func(t *testing.T) {
			var out strings.Builder
			s, err := render.NewStream(&out, f, streamSpec())
			if err != nil {
				t.Fatalf("new stream: %v", err)
			}
			if err := s.Write(first); err != nil {
				t.Fatalf("the first item was refused: %v", err)
			}
			if err := s.Write(second); err == nil {
				t.Error("a second element was accepted")
			}
		})
	}
}

// TestAHomogeneousBatchIsStillAccepted is the control. Every assertion above is
// about a refusal, and a check that refused everything would satisfy all of
// them.
func TestAHomogeneousBatchIsStillAccepted(t *testing.T) {
	for _, f := range []render.Format{render.TSV, render.XML, render.JSON, render.YAML} {
		var out strings.Builder
		s, err := render.NewStream(&out, f, streamSpec())
		if err != nil {
			t.Fatalf("%s: new stream: %v", f, err)
		}
		if err := s.Write(sampleIssue("ENG-101"), sampleIssue("ENG-102")); err != nil {
			t.Fatalf("%s: two issues in one call were refused: %v", f, err)
		}
		if err := s.Write(sampleIssue("ENG-103")); err != nil {
			t.Fatalf("%s: a third issue in a second call was refused: %v", f, err)
		}
		if err := s.Close(true, ""); err != nil {
			t.Fatalf("%s: close: %v", f, err)
		}
		if out.Len() == 0 {
			t.Errorf("%s wrote nothing for three good rows", f)
		}
	}
}

// sampleIssue is one row of the shape streamSpec projects.
func sampleIssue(key string) *render.Node {
	return render.El("issue").
		Attr("key", key).
		Leaf("summary", "Retry logic drops the last error").
		Child(render.El("status").Attr("category", "in-progress").SetText("In Progress")).
		Child(render.El("assignee").Attr("id", "712020:8f3a").Attr("display", "Ada Lovelace")).
		Leaf("updated", "2026-08-04T11:32:07Z")
}
