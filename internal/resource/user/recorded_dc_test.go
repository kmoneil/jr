package user_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// recordedConn is replayConn plus the check that makes a recording worth
// keeping separately: a cassette that has quietly become hand-written replays
// exactly like a recorded one and would assert nothing about the API.
func recordedConn(t *testing.T, fixture string) (*transport.Client, *transport.Replayer) {
	t.Helper()

	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	if !cassette.Evidence() {
		t.Fatalf("%s is not a recording, so replaying it establishes nothing "+
			"about the API", fixture)
	}
	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return conn, replayer
}

// TestTheRecordedDataCenterAccountIsAConversationAServerHad is what
// me.datacenter.json beside it cannot be.
//
// That fixture is a belief about Data Center written by hand: it says /myself
// answers with name, key, displayName, emailAddress, and active, and it says so
// because somebody decided both halves of the exchange. Replaying it proves the
// parser handles that shape and nothing whatever about whether a real Jira
// Software Data Center answers this request, or answers it with that.
//
// This one was recorded against 10.4.0, so the endpoint, the API version in the
// path, and every field read out of the body are findings rather than
// assumptions. Two of them the constructed fixture gets wrong by omission:
//
//   - The account carries a timeZone. The hand-written Data Center fixtures
//     carry none, which is why TestTheTimezoneIsReportedWhereverTheServerSendsIt
//     expects none from them — a fact about those files that was easy to read as
//     a fact about the deployment. It is not one. Data Center discloses the zone
//     Jira evaluates every relative date in, so `user me` can answer "which day
//     does startOfDay() mean" on both deployments and not just on Cloud.
//   - The account carries both a key (JIRAUSER10000) and a name (ada), and they
//     differ. The id this tool reports has to be the username, because that is
//     what JQL and every user-valued flag take; reporting the key would be a
//     value no other command accepts.
func TestTheRecordedDataCenterAccountIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("user.me")
	if !ok {
		t.Fatal("user me is not registered")
	}
	conn, replayer := recordedConn(t, "me-recorded.datacenter.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("/myself was never read: %v", unplayed)
	}

	// The username, not JIRAUSER10000. The recording carries both, so this
	// distinguishes them rather than asserting the only string available.
	if id, _ := doc.Record.AttrValue("id"); id != "ada" {
		t.Errorf("id = %q, want the username the server named", id)
	}
	if active, _ := doc.Record.AttrValue("active"); active != "true" {
		t.Errorf("active = %q, want true", active)
	}
	display, ok := doc.Record.ChildNamed("display")
	if !ok {
		t.Fatal("the account reported no display name")
	}
	if display.Text != "Ada Lovelace" {
		t.Errorf("display = %q, want the recorded display name", display.Text)
	}
	email, ok := doc.Record.ChildNamed("email")
	if !ok {
		t.Fatal("an address the server disclosed was dropped")
	}
	if email.Text != "ada@example.invalid" {
		t.Errorf("email = %q, want the address the server disclosed", email.Text)
	}
	zone, ok := doc.Record.ChildNamed("timezone")
	if !ok {
		t.Fatal("Data Center disclosed a timezone and nothing reported it, so " +
			"on this deployment the tool cannot say which clock a relative date " +
			"is evaluated on")
	}
	if zone.Text != "Etc/UTC" {
		t.Errorf("timezone = %q, want the zone the recording carries", zone.Text)
	}
	// A zone that names no zone is worse than none: a caller would compute
	// against it.
	if _, err := time.LoadLocation(zone.Text); err != nil {
		t.Errorf("timezone %q does not name an IANA zone: %v", zone.Text, err)
	}
}

// TestTheRecordedDataCenterSearchIsAConversationAServerHad establishes the one
// thing search.datacenter.json cannot: that `username` is the parameter this
// deployment answers, and that the probe row rides along without being refused.
//
// Sending Cloud's `query` to Data Center is not an error there — it is an empty
// array, complete, exit 0, indistinguishable from a directory with nobody in
// it. A hand-written fixture asserts that split by restating it, so it stays
// green against whichever spelling its author believed in. The replayer matches
// on path and query, so a row out of this recording means the endpoint, the
// parameter name, and the maxResults this code sends are the ones a Jira
// Software Data Center 10.4.0 accepted: GET /rest/api/2/user/search with
// username=a and maxResults=51 — the caller's bound of 50 plus the extra row
// SearchUsers uses to find out whether the directory held more.
//
// It is one user, because the recorded instance has one. That is the honest
// assertion available: ordering and truncation need a populated directory and
// stay with the constructed fixtures, which say so in their source field.
func TestTheRecordedDataCenterSearchIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("user.list")
	if !ok {
		t.Fatal("user list is not registered")
	}
	conn, replayer := recordedConn(t, "users-recorded.datacenter.json")

	inv := &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter},
		Args: []string{"a"}, Flags: registry.NewFlags(),
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
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the search was never sent: %v", unplayed)
	}
	// The server sent fewer rows than the bound allowed, probe row included, so
	// the directory held no more and saying so is not a guess.
	if !result.Complete {
		t.Error("a search the server did not fill was reported incomplete")
	}

	want := "id\tdisplay\temail\tactive\n" +
		"ada\tAda Lovelace\tada@example.invalid\ttrue\n"
	if buf.String() != want {
		t.Errorf("listing =\n%s\nwant\n%s", buf.String(), want)
	}

	// The same exchange in a format that carries every element, because the
	// default projection has no timezone column and the recording's finding is
	// that a Data Center *search* discloses one. Only /myself was expected to.
	conn, replayer = recordedConn(t, "users-recorded.datacenter.json")
	var xml strings.Builder
	stream, err = render.NewStream(&xml, render.XML, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("xml stream: %v", err)
	}
	result, err = cmd.Stream(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter},
		Args: []string{"a"}, Flags: registry.NewFlags(),
		Limit:  registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}, stream)
	if err != nil {
		t.Fatalf("xml run: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("xml close: %v", err)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the search was never sent: %v", unplayed)
	}
	if !strings.Contains(xml.String(), "<timezone>Etc/UTC</timezone>") {
		t.Errorf("a zone this deployment discloses on a search was dropped:\n%s",
			xml.String())
	}
}
