package issue_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestTheThreadArrivesInTheRequestThePageAlreadyCost is the whole argument for
// this flag, and the thing the card it came from got wrong at first.
//
// `issue get --with-comments` costs a second request because /issue/{key} and
// /issue/{key}/comment are two endpoints. A search is not that: `comment` goes
// into the same `fields` list every column comes from, so twelve issues cost
// one request rather than twelve. Both recordings hold exactly one interaction,
// so a second request would be a replay miss.
func TestTheThreadArrivesInTheRequestThePageAlreadyCost(t *testing.T) {
	for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
		t.Run(string(kind), func(t *testing.T) {
			_, _, replayer := runListWithComments(t, kind)
			if got := replayer.Unplayed(); len(got) != 0 {
				t.Errorf("recorded requests nobody made: %v", got)
			}
			if got := replayer.Unmatched(); len(got) != 0 {
				t.Errorf("asked for something outside the recording: %v", got)
			}
		})
	}
}

// TestDataCenterInlinesTheWholeThread is one half of a split that is real and
// was measured rather than assumed: Data Center returns every comment an issue
// has, from the oldest, and reports it complete.
func TestDataCenterInlinesTheWholeThread(t *testing.T) {
	out, result, _ := runListWithComments(t, site.DataCenter)

	if !result.Complete {
		t.Error("a whole thread was reported as clipped")
	}
	if result.PartialElement != "" {
		t.Errorf("nothing was clipped and the warning would name %q",
			result.PartialElement)
	}
	if !strings.Contains(out, "\t3\t3\n") {
		t.Errorf("the row with a thread does not report 3 of 3:\n%s", out)
	}
}

// TestCloudSaysWhichEndOfTheThreadItHolds is the other half, and the reason
// `start-at` exists on the container at all.
//
// Cloud caps the projection at twenty and returns the *newest* twenty, so a
// 25-comment issue arrives as comments 6 to 25. `count` and `complete` together
// cannot distinguish that from the first twenty of 25, and a caller
// reconstructing a conversation from a fragment needs to know which end it is
// holding.
func TestCloudSaysWhichEndOfTheThreadItHolds(t *testing.T) {
	out, result, _ := runListWithComments(t, site.Cloud)

	if result.Complete {
		t.Error("a clipped thread was reported complete")
	}
	if result.NextPageToken != "" {
		t.Errorf("a resume token was invented for a clipped subresource: %q",
			result.NextPageToken)
	}
	// The rows all arrived. What is missing is inside one of them, and the
	// warning has to name it rather than offer --limit all, which would fetch
	// no further comment.
	if result.PartialElement != "comments" {
		t.Errorf("PartialElement = %q, want comments", result.PartialElement)
	}
	if !strings.Contains(out, "\t20\t25\n") {
		t.Errorf("the clipped row does not report 20 of 25:\n%s", out)
	}

	xml := renderListWithComments(t, site.Cloud, render.XML)
	for _, want := range []string{`total="25"`, `complete="false"`, `start-at="5"`} {
		if !strings.Contains(xml, want) {
			t.Errorf("the container does not carry %s, so the fragment's "+
				"position in the real thread is unstated:\n%s", want, xml)
		}
	}
}

// TestAnUnclippedThreadOmitsStartAt keeps the attribute meaningful. Data Center
// and `issue get` always start at zero, and an attribute that is the same value
// on every row is noise a consumer learns to skip — which is how the one row
// where it matters gets skipped too.
func TestAnUnclippedThreadOmitsStartAt(t *testing.T) {
	xml := renderListWithComments(t, site.DataCenter, render.XML)
	if strings.Contains(xml, "start-at") {
		t.Errorf("a thread that starts at the beginning wrote start-at:\n%s", xml)
	}
	if !strings.Contains(xml, `<comments count="3" total="3" complete="true">`) {
		t.Errorf("the whole thread is not reported as whole:\n%s", xml)
	}
}

// TestWithoutTheFlagNothingAsksForComments guards the cost. The projection is
// cheap in requests and expensive in bytes — a Data Center issue with 150
// comments came to 200 KB — so a listing that did not ask must not pay.
//
// The assertion is the replay: the plain listing cassette records a request
// whose `fields` has no `comment` in it, so asking for one is an unmatched
// request rather than a bigger response.
func TestWithoutTheFlagNothingAsksForComments(t *testing.T) {
	client, _ := replayClient(t, site.DataCenter)

	result, err := client.List(t.Context(), issue.ListOptions{
		JQL: `project = "ENG"`, Limit: registry.Limit{N: 2},
		PageSize: 2, Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, i := range result.Issues {
		if i.HasThread {
			t.Errorf("%s carries a thread nobody asked for", i.Key)
		}
	}
}

// renderListWithComments is the same run in another format, for the assertions
// about the container's attributes rather than about the columns.
func renderListWithComments(t *testing.T, kind site.Kind, f render.Format) string {
	t.Helper()
	out, _, _ := runListIn(t, kind, f)
	return out
}

// runListWithComments drives the registered command against a recording made
// with the flag on.
func runListWithComments(
	t *testing.T, kind site.Kind,
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()
	return runListIn(t, kind, render.TSV)
}

func runListIn(
	t *testing.T, kind site.Kind, f render.Format,
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	conn, replayer := replayConn(t, "with-comments-recorded."+string(kind)+".json")
	flags := registry.NewFlags()
	flags.SetBool("with-comments", true)
	// The recordings were made at the default page size. The CLI resolves that
	// from the flag's absence and this harness does not, so it is set here
	// rather than the fixtures being re-made around a number nobody chose.
	flags.SetInt("page-size", 50)
	// The Cloud recording was made against a context with no project, so the
	// query it holds is the raw fragment alone. A stub that supplied one would
	// build `project = "ENG" AND (...)` and miss the recording — which is the
	// replayer doing its job.
	session := &stubSession{
		doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
		unscoped: kind == site.Cloud,
	}
	inv := &registry.Invocation{
		Jira: session, Flags: flags, Limit: registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if kind == site.Cloud {
		inv.Flags.SetString("jql", "key in (AGL-2, AGL-3)")
	} else {
		inv.Flags.SetString("project", "ENG")
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, f, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.ColumnsFor(inv),
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
