package render_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/render"
)

// feedDoc is a complete collection that also names where the next answer starts,
// which is the combination a page token is refused for.
func feedDoc() *render.Doc {
	return render.List("issue.changes", 1, &render.Collection{
		Name: "changes",
		Items: []*render.Node{
			render.El("change").
				Attr("issue", "ENG-1").
				Attr("id", "101").
				Attr("field", "status").
				Leaf("created", "2026-08-17T14:00:00Z"),
		},
		Complete:       true,
		NextSinceToken: "eyJkIjoiY2xvdWQifQ",
		Columns: []render.Column{
			{Header: "created", Path: "created"},
			{Header: "issue", Path: "@issue"},
		},
	})
}

// TestACompleteResultMayCarryASinceToken is the whole reason the field is not
// NextPageToken. A page token on a complete result is refused before a byte is
// written, because it would mean "there is more" and "there is no more" at once.
// A since token means neither: it names the boundary between this answer and the
// next, which a complete answer is exactly the thing to have.
func TestACompleteResultMayCarryASinceToken(t *testing.T) {
	for _, format := range []render.Format{render.XML, render.JSON, render.YAML} {
		t.Run(string(format), func(t *testing.T) {
			var buf strings.Builder
			if err := render.Write(&buf, feedDoc(), format); err != nil {
				t.Fatalf("write: %v", err)
			}
			if !strings.Contains(buf.String(), "eyJkIjoiY2xvdWQifQ") {
				t.Errorf("%s dropped the cursor, so a poller cannot resume:\n%s",
					format, buf.String())
			}
			if !strings.Contains(buf.String(), "next-since-token") {
				t.Errorf("%s does not name the field a caller reads:\n%s",
					format, buf.String())
			}
		})
	}
}

// TestASinceTokenIsNotSpelledLikeAPageToken keeps the two apart in the bytes.
// A consumer that resumed a feed by passing next-since-token to --page-token,
// or the other way round, would be told it had a token this tool never issued.
func TestASinceTokenIsNotSpelledLikeAPageToken(t *testing.T) {
	var buf strings.Builder
	if err := render.Write(&buf, feedDoc(), render.JSON); err != nil {
		t.Fatalf("write: %v", err)
	}
	if strings.Contains(buf.String(), `"next-page-token"`) {
		t.Errorf("a feed answer carries a page token as well:\n%s", buf.String())
	}
}

// TestTSVCarriesNoSinceToken states the limit rather than working around it.
// TSV has no envelope, which is why a polling consumer asks for a structured
// format, and it is the same limit that puts `complete` on stderr there.
func TestTSVCarriesNoSinceToken(t *testing.T) {
	var buf strings.Builder
	if err := render.Write(&buf, feedDoc(), render.TSV); err != nil {
		t.Fatalf("write: %v", err)
	}
	if strings.Contains(buf.String(), "eyJkIjoiY2xvdWQifQ") {
		t.Errorf("TSV grew an envelope:\n%s", buf.String())
	}
}

// TestAStreamedFeedCarriesTheCursorItWasGiven covers the path the command
// actually takes: the token is set on the stream before it closes, because Close
// asks the same two questions for every collection in the tool and only one
// command has a next answer to name.
func TestAStreamedFeedCarriesTheCursorItWasGiven(t *testing.T) {
	var buf strings.Builder
	s, err := render.NewStream(&buf, render.JSON, render.StreamSpec{
		Kind: "issue.changes", Version: 1, Name: "changes",
		Columns: []render.Column{{Header: "created", Path: "created"}},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if err := s.Write(render.El("change").
		Attr("issue", "ENG-1").Leaf("created", "2026-08-17T14:00:00Z")); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.SetNextSinceToken("eyJkIjoiY2xvdWQifQ")
	if err := s.Close(true, ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(buf.String(), "eyJkIjoiY2xvdWQifQ") {
		t.Errorf("the stream dropped the cursor it was given:\n%s", buf.String())
	}
}
