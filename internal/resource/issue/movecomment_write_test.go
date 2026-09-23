//go:build write

package issue_test

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// commentBodyOf digs the comment body out of a transition request, so a test
// can say what shape it is rather than matching a substring of JSON.
func commentBodyOf(t *testing.T, kind site.Kind, comment string) any {
	t.Helper()

	client := &issue.Client{Site: site.Info{Kind: kind}}
	req, err := client.MoveRequest("ENG-101", "21", "", comment)
	if err != nil {
		t.Fatalf("MoveRequest: %v", err)
	}
	var payload struct {
		Update struct {
			Comment []struct {
				Add struct {
					Body any `json:"body"`
				} `json:"add"`
			} `json:"comment"`
		} `json:"update"`
	}
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("the request body is not JSON: %v", err)
	}
	if len(payload.Update.Comment) != 1 {
		t.Fatalf("got %d comment operations, want 1: %s",
			len(payload.Update.Comment), req.Body)
	}
	return payload.Update.Comment[0].Add.Body
}

// TestATransitionCommentIsADocumentOnCloudAndAStringOnDataCenter is the half of
// this bug a live instance could not show.
//
// Cloud's v3 takes a document where a comment body goes and refuses a string:
// measured against a real site, every transition comment this tool ever built
// came back "Operation value must be an Atlassian Document (see the Atlassian
// Document Format)". So `--comment` on `issue move` had never worked there, and
// nothing failed here because nothing asserted the shape.
//
// Data Center's half was proved against a server on 2026-09-23: the string
// landed as a comment through a classic workflow's Resolve Issue. Cloud's
// cannot be, because the sandbox has no transition whose screen keeps one. Both
// are proved where they are decided, which is in the request builder.
func TestATransitionCommentIsADocumentOnCloudAndAStringOnDataCenter(t *testing.T) {
	cloud := commentBodyOf(t, site.Cloud, "shipped in the Q3 sweep")
	doc, ok := cloud.(map[string]any)
	if !ok {
		t.Fatalf("Cloud comment body is %T (%v), want an ADF document; a string "+
			"is what Jira refuses", cloud, cloud)
	}
	if doc["type"] != "doc" {
		t.Errorf("Cloud comment body type = %v, want doc", doc["type"])
	}
	if doc["version"] == nil {
		t.Error("Cloud comment body carries no version, so it is not ADF")
	}

	dc := commentBodyOf(t, site.DataCenter, "shipped in the Q3 sweep")
	text, ok := dc.(string)
	if !ok {
		t.Fatalf("Data Center comment body is %T, want the string it stores as "+
			"wiki markup", dc)
	}
	if text != "shipped in the Q3 sweep" {
		t.Errorf("Data Center comment body = %q, want the text as typed", text)
	}
}

// TestNoCommentMeansNoUpdateBlock keeps the fix from adding an empty operation
// to every transition that does not ask for one.
func TestNoCommentMeansNoUpdateBlock(t *testing.T) {
	client := &issue.Client{Site: site.Info{Kind: site.Cloud}}
	req, err := client.MoveRequest("ENG-101", "21", "", "")
	if err != nil {
		t.Fatalf("MoveRequest: %v", err)
	}
	if strings.Contains(string(req.Body), "update") {
		t.Errorf("a transition with no comment carries an update block: %s", req.Body)
	}
}

// TestACommentIsRefusedWhereTheTransitionCannotTakeOne is the dangerous half.
//
// Jira answers a transition with no screen, carrying a comment, with 204 and no
// comment. Measured on Data Center 10.4.0, comment count 0 to 0 across two
// accepted POSTs, with a control comment landing on the same issue in the same
// session; and on Cloud, 0 to 0 for a well-formed document. The transition
// applies, the comment vanishes, and the caller is told it worked. Data Center
// shows no screen as no fields and nothing said; Cloud says hasScreen false.
//
// A refusal, not a warning: a warning on a write that did not do what was asked
// still exits 0, and the exit is what a script reads.
func TestACommentIsRefusedWhereTheTransitionCannotTakeOne(t *testing.T) {
	noScreen := false
	for _, bare := range []site.Transition{
		{ID: "21", Name: "In Progress"},
		{ID: "11", Name: "To Do", HasScreen: &noScreen},
	} {
		err := issue.CommentIsAcceptedForTest(bare, "closed in the Q3 sweep")
		if err == nil {
			t.Fatalf("%+v accepted a comment, and Jira would take the transition "+
				"and discard it", bare)
		}
		e := errs.Coerce(err)
		if e.Code != "TRANSITION_TAKES_NO_COMMENT" {
			t.Errorf("code = %q, want TRANSITION_TAKES_NO_COMMENT", e.Code)
		}
		if e.Exit != exitcode.Usage {
			t.Errorf("exit = %d, want %d", e.Exit, exitcode.Usage)
		}
		if !strings.Contains(e.Message, "no screen") {
			t.Errorf("the refusal does not say there is no screen: %q", e.Message)
		}
		if e.Remedy == "" {
			t.Error("no remedy: the caller needs to be told that `issue comment add` works")
		}

		// No comment asked for, nothing to refuse.
		if err := issue.CommentIsAcceptedForTest(bare, ""); err != nil {
			t.Errorf("a transition with no comment was refused: %v", err)
		}
	}
}

// TestACommentIsAcceptedWhereTheScreenTakesOne is the other direction, and the
// reason the check reads the field list rather than refusing --comment outright.
//
// An instance whose transition has a comment field on its screen is one where
// this works, and neither deployment available to test has one. Refusing there
// too would be trading a silent failure for a wrong refusal.
func TestACommentIsAcceptedWhereTheScreenTakesOne(t *testing.T) {
	for _, f := range []site.MetaField{
		{ID: "comment", Name: "Comment"},
		{ID: "customfield_1", Name: "comment"},
	} {
		withScreen := site.Transition{
			ID: "31", Name: "Done", Fields: []site.MetaField{f},
		}
		if err := issue.CommentIsAcceptedForTest(withScreen, "why"); err != nil {
			t.Errorf("a transition whose screen takes %+v refused a comment: %v", f, err)
		}
	}
}

// resolveIssueOnDataCenter is the system workflow's Resolve Issue transition
// as Data Center 10.4.0 reports it: no hasScreen, which it never sends, and
// the four fields its screen shows, none of them a comment.
func resolveIssueOnDataCenter() site.Transition {
	return site.Transition{ID: "5", Name: "Resolve Issue", Fields: []site.MetaField{
		{ID: "resolution", Name: "Resolution", Required: true},
		{ID: "assignee", Name: "Assignee"},
		{ID: "fixVersions", Name: "Fix Version/s"},
		{ID: "worklog", Name: "Log Work"},
	}}
}

// TestADataCenterScreenKeepsACommentItDoesNotList is the false refusal the
// comment check shipped with.
//
// Data Center never lists `comment` among a transition's fields, screen or no
// screen, so the check refused `--comment` on every transition there. Measured
// on 10.4.0: Resolve Issue, whose screen lists four fields and no comment, took
// a transition carrying one with 204, and the issue's comment count went 0 to
// 1. A transition without a screen lists no fields at all, and that is the one
// that drops the comment, so the field list is how a screen is read there.
func TestADataCenterScreenKeepsACommentItDoesNotList(t *testing.T) {
	if err := issue.CommentIsAcceptedForTest(resolveIssueOnDataCenter(),
		"closed after review"); err != nil {
		t.Errorf("a comment was refused on a transition whose screen keeps it: %v", err)
	}
}

// TestACloudScreenStillHasToListTheComment keeps the change to what was
// measured. Cloud says whether a transition has a screen, and nobody has
// measured whether a screen that does not list a comment keeps one, because
// the sandbox has no screened transition. Until somebody does, Cloud keeps the
// rule it had.
func TestACloudScreenStillHasToListTheComment(t *testing.T) {
	screened := true
	resolve := resolveIssueOnDataCenter()
	resolve.HasScreen = &screened
	err := issue.CommentIsAcceptedForTest(resolve, "closed after review")
	if code := errs.Coerce(err).Code; code != "TRANSITION_TAKES_NO_COMMENT" {
		t.Errorf("code = %q, want TRANSITION_TAKES_NO_COMMENT: a server that "+
			"says there is a screen and lists no comment has not been measured", code)
	}
}

// TestAMoveCarriesACommentThroughADataCenterScreen is the same thing through
// the registered command, against the request Jira answered with 204 and a
// comment kept: resolution and comment together, as a person closing through
// that form would send them.
func TestAMoveCarriesACommentThroughADataCenterScreen(t *testing.T) {
	cmd, _ := registry.Lookup("issue.move")
	conn, replayer := replayConn(t, "move-comment.datacenter.json")
	flags := registry.NewFlags()
	flags.SetString("resolution", "Done")
	flags.SetString("comment", "probe comment on a screened transition")
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn,
			kind: site.DataCenter, metaClient: conn,
		},
		Args: []string{"ENG-101", "Resolve Issue"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	doc, err := cmd.Run(t.Context(), inv)
	if err != nil {
		t.Fatalf("a comment Jira keeps was refused: %v", err)
	}
	if id, _ := doc.Record.AttrValue("transition"); id != "5" {
		t.Errorf("transition = %q, want 5", id)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the transition was never sent: %v", unplayed)
	}
}
