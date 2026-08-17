package render_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
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

// TestARefusalNamesTheRecordItRefused is what a field report had to bisect
// --limit to work out by hand.
//
// A validation path is built from element names, so the refusal read
// "issue.comment.list/comments/comment/body" for a thread of three hundred
// comments and named none of them. The reporter narrowed it down by halving
// --limit until the command stopped failing, and still never learned the comment
// id, because nothing in the output carried it.
//
// Both paths are asserted, because both hold the whole record and only one of
// them was ever going to be remembered.
func TestARefusalNamesTheRecordItRefused(t *testing.T) {
	bell := "a bell\x07here"

	// The whole detail, not a substring of it: where the identity lands and what
	// it displaces are both part of the answer, and an assertion that only looks
	// for the identity passes just as well when the byte offset is gone.
	for _, tc := range []struct {
		name string
		item *render.Node
		want string
	}{{
		// key is preferred over id: it is what a caller of this kind types.
		name: "an issue by key",
		item: sampleIssue("ENG-101").Leaf("note", bell),
		want: "U+0007 at byte 6; in issue key=ENG-101",
	}, {
		name: "a comment by id",
		item: render.El("issue").Attr("id", "10234").Leaf("body", bell),
		want: "U+0007 at byte 6; in issue id=10234",
	}, {
		// An empty attribute is a fact this format emits deliberately, and it
		// identifies nothing, so the next candidate answers instead.
		name: "an empty key falls through to the id",
		item: render.El("issue").Attr("key", "").Attr("id", "10234").Leaf("body", bell),
		want: "U+0007 at byte 6; in issue id=10234",
	}, {
		// An activity event carries neither: it is identified by the issue it
		// happened on together with what kind of event it was.
		name: "a record with no identifying attribute",
		item: render.El("issue").
			Attr("kind", "comment").
			Attr("issue", "ENG-101").
			Leaf("body", bell),
		want: "U+0007 at byte 6; in issue kind=comment issue=ENG-101",
	}, {
		// The same defect one layer in: a record carrying a paged subresource
		// reports every member of it under one path. Innermost first, because
		// that is the record to go and correct.
		name: "a member of a list inside a record",
		item: sampleIssue("ENG-101").Child(render.ListEl("comments", "comment",
			render.El("comment").Attr("id", "10001").Leaf("body", "fine"),
			render.El("comment").Attr("id", "10234").Leaf("body", bell),
		)),
		want: "U+0007 at byte 6; in comment id=10234; in issue key=ENG-101",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			for path, got := range map[string]*errs.Error{
				"streamed": refuseStreamed(t, tc.item),
				"buffered": refuseBuffered(t, tc.item),
			} {
				if got.Detail != tc.want {
					t.Errorf("%s: detail = %q, want %q\nmessage: %s",
						path, got.Detail, tc.want, got.Message)
				}
			}
		})
	}
}

// TestAnIdentityNoFormatCanCarryIsNotCopiedIntoTheError covers the case where
// the identifying attribute is itself what was refused.
//
// The annotation lands in a document on stderr, and that document is written by
// the same four writers with none of this validation in front of them. Copying a
// raw control character into it would produce a diagnostic the caller cannot
// parse either, which is the failure this whole check exists to close, one layer
// out. The error already names the attribute, so nothing is lost by staying
// quiet about it.
func TestAnIdentityNoFormatCanCarryIsNotCopiedIntoTheError(t *testing.T) {
	// The only attribute it has is the unrenderable one, so there is nothing
	// left to name the record with and the detail is the one the check itself
	// produced, unannotated.
	item := render.El("issue").Attr("key", "ENG-\x07101").Leaf("summary", "fine")

	for path, got := range map[string]*errs.Error{
		"streamed": refuseStreamed(t, item),
		"buffered": refuseBuffered(t, item),
	} {
		if strings.ContainsRune(got.Error(), '\x07') {
			t.Errorf("%s: a character no format can carry reached the error:\n%q",
				path, got.Error())
		}
		if !strings.Contains(got.Message, `attribute "key"`) {
			t.Errorf("%s: the refusal does not name the attribute it refused:\n%s",
				path, got.Message)
		}
		if want := "U+0007 at byte 4"; got.Detail != want {
			t.Errorf("%s: detail = %q, want %q", path, got.Detail, want)
		}
	}
}

// refuseStreamed writes one item to a TSV stream and returns the refusal.
func refuseStreamed(t *testing.T, item *render.Node) *errs.Error {
	t.Helper()
	var out strings.Builder
	s, err := render.NewStream(&out, render.TSV, streamSpec())
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	err = s.Write(item)
	if err == nil {
		t.Fatalf("the streamed path accepted it, and produced:\n%s", out.String())
	}
	return structured(t, err)
}

// refuseBuffered renders the same item as a one-row collection and returns the
// refusal.
func refuseBuffered(t *testing.T, item *render.Node) *errs.Error {
	t.Helper()
	var out strings.Builder
	doc := render.List("issue.list", 1, &render.Collection{
		Name:     "issues",
		Items:    []*render.Node{item},
		Complete: true,
		Columns:  streamSpec().Columns,
	})
	err := render.Write(&out, doc, render.XML)
	if err == nil {
		t.Fatalf("the buffered path accepted it, and produced:\n%s", out.String())
	}
	return structured(t, err)
}

func structured(t *testing.T, err error) *errs.Error {
	t.Helper()
	e, ok := errs.AsError(err)
	if !ok {
		t.Fatalf("the refusal carries no structured error: %v", err)
	}
	return e
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
