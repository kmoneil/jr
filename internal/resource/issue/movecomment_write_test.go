//go:build write

package issue_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
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
// Neither test deployment has a transition whose screen accepts a comment, so
// the encoding cannot be proved end to end against a server. It can be proved
// where it is decided, which is in the request builder.
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

// TestACommentIsRefusedWhereTheTransitionCannotTakeOne is the Data Center half,
// and the more dangerous one.
//
// Jira 10.4.0 answers a transition carrying a comment its screen has no field
// for with 204 and no comment: measured, comment count 0 to 0 across two
// accepted POSTs, with a control comment landing on the same issue in the same
// session. The transition applies, the comment vanishes, and the caller is told
// it worked.
//
// A refusal, not a warning: a warning on a write that did not do what was asked
// still exits 0, and the exit is what a script reads.
func TestACommentIsRefusedWhereTheTransitionCannotTakeOne(t *testing.T) {
	bare := site.Transition{ID: "21", Name: "In Progress"}

	err := issue.CommentIsAcceptedForTest(bare, "closed in the Q3 sweep")
	if err == nil {
		t.Fatal("a comment was accepted for a transition with no comment field, " +
			"so Jira would take the transition and discard it")
	}
	e := errs.Coerce(err)
	if e.Code != "TRANSITION_TAKES_NO_COMMENT" {
		t.Errorf("code = %q, want TRANSITION_TAKES_NO_COMMENT", e.Code)
	}
	if e.Exit != exitcode.Usage {
		t.Errorf("exit = %d, want %d", e.Exit, exitcode.Usage)
	}
	if e.Remedy == "" {
		t.Error("no remedy: the caller needs to be told that `issue comment add` works")
	}

	// No comment asked for, nothing to refuse.
	if err := issue.CommentIsAcceptedForTest(bare, ""); err != nil {
		t.Errorf("a transition with no comment was refused: %v", err)
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
