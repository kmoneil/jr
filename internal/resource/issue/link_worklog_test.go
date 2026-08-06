package issue_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/issue"
	"github.com/kmoneil/jira-cli/internal/site"
)

// TestLinksAreReadFromThisIssuesSide is the subtle half of links. The same link
// is "blocks" from one end and "is blocked by" from the other, and reporting
// the type's name instead would leave a caller to work out which they had.
func TestLinksAreReadFromThisIssuesSide(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			conn, _ := replayConn(t, "links."+string(kind)+".json")
			client := &issue.Client{Transport: conn, Site: site.Info{Kind: kind}}

			links, err := client.ListLinks(t.Context(), "ENG-101")
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(links) != 2 {
				t.Fatalf("got %d links, want 2", len(links))
			}

			// The fixture has one link each way. Jira writes a link as
			// "inwardIssue <inward> outwardIssue", so when the far end is the
			// outward issue, this issue is the inward one.
			byOther := map[string]issue.Link{}
			for _, l := range links {
				byOther[l.Other] = l
			}
			if got := byOther["ENG-200"].Relationship; got != "is blocked by" {
				t.Errorf("ENG-200 reads %q, want \"is blocked by\"", got)
			}
			if got := byOther["ENG-300"].Relationship; got != "blocks" {
				t.Errorf("ENG-300 reads %q, want \"blocks\"", got)
			}
			// Both are the same type; only the reading differs.
			for key, l := range byOther {
				if l.Type != "Blocks" {
					t.Errorf("%s type = %q", key, l.Type)
				}
			}
		})
	}
}

// TestLinkDirectionIsRefusedWhenAmbiguous is the point of taking a phrase
// rather than a type name. "Blocks" does not say which way the link runs, and
// choosing would be wrong half the time.
func TestLinkDirectionIsRefusedWhenAmbiguous(t *testing.T) {
	types := []issue.LinkType{
		{ID: "10000", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"},
		{ID: "10002", Name: "Duplicate", Inward: "is duplicated by", Outward: "duplicates"},
	}

	// A type name that matches neither phrase says nothing about direction.
	_, _, err := issue.ResolveLinkType(types, "Duplicate")
	if err == nil {
		t.Fatal("a bare type name picked a direction")
	}
	e := errs.Coerce(err)
	if e.Code != "AMBIGUOUS_LINK_DIRECTION" {
		t.Errorf("code = %q, want AMBIGUOUS_LINK_DIRECTION", e.Code)
	}
	// Both readings are offered, so the caller can pick the one that is true.
	if !strings.Contains(e.Detail, "duplicates") ||
		!strings.Contains(e.Detail, "is duplicated by") {
		t.Errorf("the detail does not offer both readings: %q", e.Detail)
	}

	// "Blocks" is the one case where the guard cannot fire, because the type
	// name and its outward phrase differ only in case. That coincidence
	// resolves it the way anybody typing it would mean, so it is left alone
	// rather than special-cased into a refusal.
	if got, dir, err := issue.ResolveLinkType(types, "Blocks"); err != nil ||
		got.Name != "Blocks" || dir != issue.Outward {
		t.Errorf("ResolveLinkType(\"Blocks\") = %s/%v, %v; want Blocks/Outward",
			got.Name, dir, err)
	}

	// And each phrase resolves to its own direction.
	for phrase, want := range map[string]issue.Direction{
		"blocks":        issue.Outward,
		"BLOCKS":        issue.Outward,
		"is blocked by": issue.Inward,
	} {
		got, dir, err := issue.ResolveLinkType(types, phrase)
		if err != nil {
			t.Errorf("ResolveLinkType(%q) = %v", phrase, err)
			continue
		}
		if got.Name != "Blocks" || dir != want {
			t.Errorf("ResolveLinkType(%q) = %s/%v, want Blocks/%v",
				phrase, got.Name, dir, want)
		}
	}
}

// TestAnUnknownRelationshipListsWhatExists covers the refusal. Link wording is
// customizable per site, so showing the set beats guessing at a near match.
func TestAnUnknownRelationshipListsWhatExists(t *testing.T) {
	types := []issue.LinkType{
		{Name: "Blocks", Inward: "is blocked by", Outward: "blocks"},
		{Name: "Relates", Inward: "relates to", Outward: "relates to"},
	}

	_, _, err := issue.ResolveLinkType(types, "supersedes")
	if err == nil {
		t.Fatal("an unknown relationship was accepted")
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_LINK_TYPE" {
		t.Errorf("code = %q, want UNKNOWN_LINK_TYPE", e.Code)
	}
	for _, want := range []string{"blocks", "is blocked by", "relates to"} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("the detail does not list %q: %q", want, e.Detail)
		}
	}
	if errs.ExitOf(err) != exitcode.Usage {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
	}
}

// TestWorklogReportsTimeTwice covers the pair a caller needs: the wording for
// reading and the seconds for arithmetic. Deriving one from the other means
// re-implementing a site's working day.
func TestWorklogReportsTimeTwice(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			out, result, _ := runWorklogList(t, kind, registry.Limit{All: true})

			if !result.Complete {
				t.Error("an exhausted worklog list was reported incomplete")
			}
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if len(lines) != 3 {
				t.Fatalf("got %d lines, want a header and two rows:\n%s", len(lines), out)
			}
			if lines[0] != "id\tauthor\tstarted\tseconds\ttime-spent" {
				t.Errorf("header = %q", lines[0])
			}
			if !strings.Contains(lines[1], "\t10800\t3h") {
				t.Errorf("the row does not carry both readings: %q", lines[1])
			}
			// Started is when the work happened, and it is not the created
			// timestamp — summing the wrong one answers a different question.
			if !strings.Contains(lines[1], "2026-08-01T09:00:00Z") {
				t.Errorf("started is not the work's own time: %q", lines[1])
			}
		})
	}
}

// TestDurationsAreCheckedNotConverted covers the local check. A mistyped
// duration is the kind that logs 3 minutes where 3 hours was meant, and the
// server's refusal names the request rather than the field.
func TestDurationsAreCheckedNotConverted(t *testing.T) {
	for _, ok := range []string{"3h", "30m", "1d 4h", "2w 3d 4h 30m", " 3h ", "2w"} {
		if err := issue.ValidateDuration(ok); err != nil {
			t.Errorf("ValidateDuration(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "   ", "3", "3 hours", "h", "3h4", "-3h", "3s"} {
		err := issue.ValidateDuration(bad)
		if err == nil {
			t.Errorf("ValidateDuration(%q) was accepted", bad)
			continue
		}
		if code := errs.Coerce(err).Code; code != "INVALID_DURATION" {
			t.Errorf("%q code = %q, want INVALID_DURATION", bad, code)
		}
	}

	// The remedy says why no conversion happens, because "why not just accept
	// 180m" is the obvious next question.
	e := errs.Coerce(issue.ValidateDuration("3 hours"))
	if !strings.Contains(e.Remedy, "site setting") {
		t.Errorf("the remedy does not say why: %q", e.Remedy)
	}
}

// TestLinkAndWorklogListsAreInEveryBuild is why the reads are in their own
// files: a reader binary can see relationships and logged time.
func TestLinkAndWorklogListsAreInEveryBuild(t *testing.T) {
	for _, name := range []string{"issue.link.list", "issue.worklog.list"} {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if len(cmd.RequiresTags) != 0 {
			t.Errorf("%s requires %v; reading needs no tag", name, cmd.RequiresTags)
		}
		if cmd.Mutating {
			t.Errorf("%s is marked mutating", name)
		}
	}
}

func runWorklogList(
	t *testing.T, kind site.Kind, limit registry.Limit,
) (string, registry.StreamResult, *issue.Client) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.worklog.list")
	if !ok {
		t.Fatal("issue worklog list is not registered")
	}

	conn, _ := replayConn(t, "worklogs."+string(kind)+".json")
	flags := registry.NewFlags()
	flags.SetInt("page-size", 2)
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
	return buf.String(), result, &issue.Client{Transport: conn, Site: site.Info{Kind: kind}}
}

// TestLinkListRunsAsARegisteredCommand exercises the command wrapper and the
// rendered shape, which the client-level test above does not reach.
func TestLinkListRunsAsARegisteredCommand(t *testing.T) {
	cmd, ok := registry.Lookup("issue.link.list")
	if !ok {
		t.Fatal("issue link list is not registered")
	}

	conn, replayer := replayConn(t, "links.datacenter.json")
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.DataCenter,
		},
		Args: []string{"ENG-101"}, Flags: registry.NewFlags(),
		Limit:  registry.Limit{All: true},
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
	if !result.Complete {
		t.Error("a whole link list was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the issue was never read: %v", unplayed)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if lines[0] != "id\trelationship\tkey\tstatus\tsummary" {
		t.Errorf("header = %q", lines[0])
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header and two rows:\n%s", len(lines), buf.String())
	}
	// Ordered, so two runs of a script agree. "blocks" sorts before
	// "is blocked by".
	if !strings.HasPrefix(lines[1], "10002\tblocks\tENG-300\t") {
		t.Errorf("first row = %q", lines[1])
	}
}

// TestLinkColumnsResolve keeps the default projection honest: a column whose
// path finds nothing renders as an empty cell, so nothing else would catch it.
func TestLinkColumnsResolve(t *testing.T) {
	node := issue.Link{
		ID: "1", Type: "Blocks", Relationship: "blocks",
		Other: "ENG-2", OtherStatus: "Open", OtherType: "Bug", Summary: "a summary",
	}.Node()
	for _, col := range issue.LinkColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}
}

func TestWorklogColumnsResolve(t *testing.T) {
	node := issue.Worklog{
		ID: "1", TimeSpent: "3h", Seconds: 10800,
		Started: "2026-08-01T09:00:00Z", Created: "2026-08-02T11:00:00Z",
	}.Node()
	for _, col := range issue.WorklogColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}
}

// TestLinkAndWorklogDocsAreWellFormed covers the document renderers, which the
// streaming path does not use.
func TestLinkAndWorklogDocsAreWellFormed(t *testing.T) {
	links := issue.LinkListDoc([]issue.Link{{
		ID: "1", Type: "Blocks", Relationship: "blocks",
		Other: "ENG-2", OtherStatus: "Open", Summary: "a summary",
	}}, true)
	if err := links.Validate(); err != nil {
		t.Fatalf("links: %v", err)
	}

	worklogs := issue.WorklogListDoc([]issue.Worklog{{
		ID: "1", TimeSpent: "3h", Seconds: 10800,
		Started: "2026-08-01T09:00:00Z", Comment: "note", BodyFormat: "wiki",
	}}, true)
	if err := worklogs.Validate(); err != nil {
		t.Fatalf("worklogs: %v", err)
	}

	var xml strings.Builder
	if err := render.Write(&xml, worklogs, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`<time-spent seconds="10800">3h</time-spent>`,
		`<comment format="wiki">`,
	} {
		if !strings.Contains(xml.String(), want) {
			t.Errorf("the output does not contain %s:\n%s", want, xml.String())
		}
	}
}

// TestWorklogListTruncatesAndSaysSo covers the bound, including that no token
// is invented for an offset-paged endpoint.
func TestWorklogListTruncatesAndSaysSo(t *testing.T) {
	_, result, _ := runWorklogList(t, site.DataCenter, registry.Limit{N: 1})

	if result.Complete {
		t.Error("a truncated worklog list was reported complete")
	}
	if result.NextPageToken != "" {
		t.Errorf("a page token was invented: %q", result.NextPageToken)
	}
}

// TestLinkListTruncatesAndSaysSo is the same for links, which arrive whole.
func TestLinkListTruncatesAndSaysSo(t *testing.T) {
	cmd, _ := registry.Lookup("issue.link.list")
	conn, _ := replayConn(t, "links.datacenter.json")
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.DataCenter,
		},
		Args: []string{"ENG-101"}, Flags: registry.NewFlags(),
		Limit:  registry.Limit{N: 1},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	stream, err := render.NewStream(io.Discard, render.TSV, render.StreamSpec{
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
	if result.Complete {
		t.Error("a truncated link list was reported complete")
	}
}

// TestLinkAndWorklogReadsFailLoudlyWithoutASession covers the guard both share.
func TestLinkAndWorklogReadsFailLoudlyWithoutASession(t *testing.T) {
	for _, name := range []string{"issue.link.list", "issue.worklog.list"} {
		cmd, _ := registry.Lookup(name)
		inv := &registry.Invocation{
			Args: []string{"ENG-1"}, Flags: registry.NewFlags(),
			Limit: registry.Limit{All: true}, Progress: registry.NoProgress,
		}
		stream, err := render.NewStream(io.Discard, render.TSV, render.StreamSpec{
			Kind: cmd.Kind(), Version: cmd.KindVersion(),
			Name: cmd.CollectionName, Columns: cmd.Columns,
		})
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if _, err := cmd.Stream(t.Context(), inv, stream); err == nil {
			t.Errorf("%s ran without a session", name)
		} else if code := errs.Coerce(err).Code; code != "NO_SESSION" {
			t.Errorf("%s code = %q, want NO_SESSION", name, code)
		}
	}
}
