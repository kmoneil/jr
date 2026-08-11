package issue_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestCommentListPagesAndNamesTheMarkup is the read half, on both deployments.
// The bodies are genuinely different types — a string on Data Center, a
// document on Cloud — and the format attribute is what lets a caller tell.
func TestCommentListPagesAndNamesTheMarkup(t *testing.T) {
	for _, tc := range []struct {
		kind   site.Kind
		format string
	}{
		{site.Cloud, "markdown"},
		{site.DataCenter, "wiki"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			out, result, replayer := runCommentList(t, tc.kind,
				registry.Limit{All: true}, 2)

			if !result.Complete {
				t.Error("an exhausted comment list was reported incomplete")
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the second page was never fetched: %v", unplayed)
			}

			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if len(lines) != 4 {
				t.Fatalf("got %d lines, want a header and three rows:\n%s", len(lines), out)
			}
			if lines[0] != "id\tauthor\tcreated\tbody" {
				t.Errorf("header = %q", lines[0])
			}
			// Oldest first, across the page boundary.
			var ids []string
			for _, line := range lines[1:] {
				ids = append(ids, strings.SplitN(line, "\t", 2)[0])
			}
			if strings.Join(ids, ",") != "10001,10002,10003" {
				t.Errorf("ids = %v, want them oldest first across pages", ids)
			}

			// And the markup is named rather than converted.
			doc := commentDoc(t, tc.kind)
			var xml strings.Builder
			if err := render.Write(&xml, doc, render.XML); err != nil {
				t.Fatalf("render: %v", err)
			}
			if !strings.Contains(xml.String(), `format="`+tc.format+`"`) {
				t.Errorf("the body does not name its markup as %s:\n%s",
					tc.format, xml.String())
			}
		})
	}
}

// TestARestrictedCommentSaysSo matters more than it looks. A caller quoting a
// comment they believe is public may be quoting one that is not.
func TestARestrictedCommentSaysSo(t *testing.T) {
	doc := commentDoc(t, site.DataCenter)

	var xml strings.Builder
	if err := render.Write(&xml, doc, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(xml.String(), `visibility="role:Administrators"`) {
		t.Errorf("the restricted comment does not carry its restriction:\n%s", xml.String())
	}
	// And an unrestricted one carries no attribute at all, rather than an empty
	// one that reads as a restriction nobody set.
	if strings.Contains(xml.String(), `visibility=""`) {
		t.Errorf("an unrestricted comment carries an empty restriction:\n%s", xml.String())
	}
}

// TestCommentListTruncatesAndSaysSo covers the bound. This endpoint pages by
// offset, so there is no token to resume from — one that meant nothing would be
// worse than none.
func TestCommentListTruncatesAndSaysSo(t *testing.T) {
	out, result, _ := runCommentList(t, site.DataCenter, registry.Limit{N: 2}, 2)

	if result.Complete {
		t.Error("a truncated comment list was reported complete")
	}
	if result.NextPageToken != "" {
		t.Errorf("a page token was invented for an offset-paged endpoint: %q",
			result.NextPageToken)
	}
	if rows := strings.Count(strings.TrimRight(out, "\n"), "\n"); rows != 2 {
		t.Errorf("got %d rows, want 2:\n%s", rows, out)
	}
}

// TestCommentListIsInEveryBuild is the card's reason for splitting read from
// write: reading takes no write tag, so a reader binary has it.
func TestCommentListIsInEveryBuild(t *testing.T) {
	cmd, ok := registry.Lookup("issue.comment.list")
	if !ok {
		t.Fatal("issue comment list is not registered")
	}
	if len(cmd.RequiresTags) != 0 {
		t.Errorf("comment list requires %v; reading needs no tag", cmd.RequiresTags)
	}
	if cmd.Mutating {
		t.Error("comment list is marked mutating")
	}
}

// TestABadCommentKeyIsRefusedLocally keeps a typo from costing a round trip,
// and keeps a caller's argument from reaching a URL path unchecked.
func TestABadCommentKeyIsRefusedLocally(t *testing.T) {
	cmd, _ := registry.Lookup("issue.comment.list")
	for _, bad := range []string{"", "nonsense", "ENG", "../../admin"} {
		inv := &registry.Invocation{Args: []string{bad}, Flags: registry.NewFlags()}
		if bad == "" {
			inv.Args = nil
		}
		err := cmd.Validate(t.Context(), inv)
		if err == nil {
			t.Errorf("%q was accepted", bad)
			continue
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("%q exits %v, want %v", bad, errs.ExitOf(err), exitcode.Usage)
		}
	}
}

// runCommentList drives the registered command against a recorded conversation.
func runCommentList(
	t *testing.T, kind site.Kind, limit registry.Limit, pageSize int,
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.comment.list")
	if !ok {
		t.Fatal("issue comment list is not registered")
	}

	conn, replayer := replayConn(t, "comments."+string(kind)+".json")
	flags := registry.NewFlags()
	flags.SetInt("page-size", pageSize)
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
		},
		Args: []string{"ENG-101"}, Flags: flags, Limit: limit,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), inv, stream)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.String(), result, replayer
}

// commentDoc reads one page directly, for the assertions about rendering rather
// than about paging.
func commentDoc(t *testing.T, kind site.Kind) *render.Doc {
	t.Helper()

	conn, _ := replayConn(t, "comments."+string(kind)+".json")
	client := &issue.Client{Transport: conn, Site: site.Info{Kind: kind}}
	page, err := client.ListComments(t.Context(), "ENG-101", 0, 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	doc := issue.CommentListDoc(page.Comments, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc
}
