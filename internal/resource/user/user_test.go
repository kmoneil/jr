package user_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/user"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

var deployments = []site.Kind{site.Cloud, site.DataCenter}

// recordedAccountID is the Cloud id the recorded fixtures carry, after
// scrubbing. The value is a placeholder; its shape — prefix, colon, UUID — is
// the real thing, which is what the id handling is about.
const recordedAccountID = "000000:00000000-0000-0000-0000-000000000000"

// TestTheIdIsTheDeploymentsOwn is the point of this resource. Every command
// that takes a user wants an accountId on Cloud and a username on Data Center,
// they are not interchangeable, and this is where the right one comes from.
func TestTheIdIsTheDeploymentsOwn(t *testing.T) {
	for _, tc := range []struct {
		kind site.Kind
		id   string
	}{
		{site.Cloud, "000000:00000000-0000-0000-0000-000000000000"},
		{site.DataCenter, "ada"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			conn, replayer := replayConn(t, "search."+string(tc.kind)+".json")
			client := &user.Client{Transport: conn, Site: site.Info{Kind: tc.kind}}

			page, err := client.Search(t.Context(), "ada", 50)
			users := page.Users
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				// The query parameter differs by deployment, and sending the
				// wrong one is an empty result rather than an error — so a
				// fixture miss here is the bug this catches.
				t.Fatalf("the search used the wrong parameter: %v", unplayed)
			}

			if len(users) == 0 {
				t.Fatalf("no users came back")
			}
			var ada user.User
			for _, u := range users {
				if u.Display == "Ada Lovelace" {
					ada = u
				}
			}
			if ada.ID != tc.id {
				t.Errorf("id = %q, want this deployment's own %q", ada.ID, tc.id)
			}
		})
	}
}

// TestUsersAreOrderedByDisplayName needs two of them, which the recorded
// instance does not have — it is a one-person sandbox. So it runs against the
// constructed fixture, which says so in its source field.
func TestUsersAreOrderedByDisplayName(t *testing.T) {
	conn, _ := replayConn(t, "search-private.cloud.json")
	client := &user.Client{Transport: conn, Site: site.Info{Kind: site.Cloud}}

	page, err := client.Search(t.Context(), "ada", 50)
	users := page.Users
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(users) != 2 || users[0].Display != "Aaron Bot" {
		t.Fatalf("users = %+v, want them ordered by display name", users)
	}
}

// TestAnAbsentEmailIsNotAnAbsentAddress covers the Cloud privacy setting. An
// empty column means "not disclosed", and inferring "has none" from it would be
// wrong about most of an instance.
//
// It uses the constructed fixture deliberately. The recorded instance is one a
// single person owns, so it discloses its own owner's address — a recording
// cannot show the case this is about, and pretending otherwise would leave the
// privacy path untested.
func TestAnAbsentEmailIsNotAnAbsentAddress(t *testing.T) {
	conn, _ := replayConn(t, "search-private.cloud.json")
	client := &user.Client{Transport: conn, Site: site.Info{Kind: site.Cloud}}

	page, err := client.Search(t.Context(), "ada", 50)
	users := page.Users
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, u := range users {
		if u.Email != "" {
			t.Errorf("%s disclosed an email the fixture does not carry", u.ID)
		}
	}

	// The element is omitted rather than emitted empty, so a consumer sees
	// "not present" rather than a value that looks like an answer.
	var xml strings.Builder
	if err := render.Write(&xml, user.ListDoc(users, true), render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(xml.String(), "<email>") {
		t.Errorf("an undisclosed email was rendered:\n%s", xml.String())
	}

	// Data Center's fixture does carry them, and they come through.
	dcConn, _ := replayConn(t, "search.datacenter.json")
	dc := &user.Client{Transport: dcConn, Site: site.Info{Kind: site.DataCenter}}
	dcPage, err := dc.Search(t.Context(), "ada", 50)
	dcUsers := dcPage.Users
	if err != nil {
		t.Fatalf("data center: %v", err)
	}
	if dcUsers[0].Email == "" {
		t.Error("a disclosed email was dropped")
	}
}

// TestAppAccountsAreDistinguishable covers the kind field. Assigning an issue
// to an application account is a mistake worth seeing before making.
func TestAppAccountsAreDistinguishable(t *testing.T) {
	conn, _ := replayConn(t, "search.cloud.json")
	client := &user.Client{Transport: conn, Site: site.Info{Kind: site.Cloud}}

	page, err := client.Search(t.Context(), "ada", 50)
	users := page.Users
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, u := range users {
		if u.Display == "Aaron Bot" && u.Kind != "app" {
			t.Errorf("an app account reports kind %q", u.Kind)
		}
	}
}

// TestGetUsesTheRightParameter covers the other half of the identity split: the
// query parameter differs with the id, so one deployment's id sent to the other
// is a 404 rather than a wrong answer.
func TestGetUsesTheRightParameter(t *testing.T) {
	for _, tc := range []struct {
		kind site.Kind
		id   string
	}{
		{site.Cloud, recordedAccountID},
		{site.DataCenter, "ada"},
	} {
		conn, replayer := replayConn(t, "user."+string(tc.kind)+".json")
		client := &user.Client{Transport: conn, Site: site.Info{Kind: tc.kind}}

		got, err := client.Get(t.Context(), tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		if got.ID != tc.id {
			t.Errorf("%s id = %q, want %q", tc.kind, got.ID, tc.id)
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("%s used the wrong parameter: %v", tc.kind, unplayed)
		}
	}
}

// TestMeReusesWhoami keeps the identity this reports and the one `auth login`
// verified from coming apart.
func TestMeReusesWhoami(t *testing.T) {
	for _, kind := range deployments {
		cmd, ok := registry.Lookup("user.me")
		if !ok {
			t.Fatal("user me is not registered")
		}

		conn, replayer := replayConn(t, "me."+string(kind)+".json")
		inv := &registry.Invocation{
			Jira: &stubSession{conn: conn, kind: kind}, Flags: registry.NewFlags(),
			Stderr: io.Discard, Progress: registry.NoProgress,
		}
		doc, err := cmd.Run(t.Context(), inv)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if id, _ := doc.Record.AttrValue("id"); id == "" {
			t.Errorf("%s: no id reported", kind)
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("%s: /myself was never read: %v", kind, unplayed)
		}
		// It emits the same kind as `user get`, because it is the same shape.
		if !cmd.Emits(doc.Kind, doc.Version) {
			t.Errorf("%s: emitted %s v%d, undeclared", kind, doc.Kind, doc.Version)
		}
	}
}

// TestAnEmptyQueryIsRefused covers the deliberate absence of "list everyone":
// it is slow, rarely meant, and the endpoint that allows it differs by
// deployment.
func TestAnEmptyQueryIsRefused(t *testing.T) {
	cmd, _ := registry.Lookup("user.list")
	for _, args := range [][]string{nil, {""}, {"   "}} {
		inv := &registry.Invocation{Args: args, Flags: registry.NewFlags()}
		err := cmd.Validate(t.Context(), inv)
		if err == nil {
			t.Errorf("args=%v was accepted", args)
			continue
		}
		if code := errs.Coerce(err).Code; code != "EMPTY_QUERY" {
			t.Errorf("args=%v code = %q, want EMPTY_QUERY", args, code)
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("args=%v exit = %v", args, errs.ExitOf(err))
		}
	}
}

// TestAMissingUserSaysWhichIdItWanted covers the refusal that would otherwise
// be a bare 404: the two deployments identify a user differently, and that is
// the most likely reason a lookup came back empty.
func TestAMissingUserSaysWhichIdItWanted(t *testing.T) {
	conn, _ := replayConn(t, "empty-user.datacenter.json")
	client := &user.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	_, err := client.Get(t.Context(), "nobody")
	if err == nil {
		t.Fatal("an empty response was reported as a user")
	}
	e := errs.Coerce(err)
	if e.Code != "NO_SUCH_USER" {
		t.Errorf("code = %q, want NO_SUCH_USER", e.Code)
	}
	if !strings.Contains(e.Remedy, "accountId") || !strings.Contains(e.Remedy, "username") {
		t.Errorf("the remedy does not explain the two identities: %q", e.Remedy)
	}
}

// TestUserColumnsResolve keeps the default projection honest.
func TestUserColumnsResolve(t *testing.T) {
	node := user.Node(user.User{
		ID: "ada", Display: "Ada Lovelace", Email: "ada@example.invalid", Active: true,
	})
	for _, col := range user.ListColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}
}

// TestEveryUserCommandIsAReadInEveryBuild keeps the resource out of the write
// tag.
func TestEveryUserCommandIsAReadInEveryBuild(t *testing.T) {
	for _, name := range []string{"user.list", "user.get", "user.me"} {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if cmd.Mutating || cmd.Destructive {
			t.Errorf("%s is a read but declares otherwise", name)
		}
		if len(cmd.RequiresTags) != 0 {
			t.Errorf("%s requires %v; reading needs no tag", name, cmd.RequiresTags)
		}
	}
}

func replayConn(t *testing.T, fixture string) (*transport.Client, *transport.Replayer) {
	t.Helper()
	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
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

type stubSession struct {
	// site overrides what Site reports, for a test about provenance.
	site   string
	fields []string
	conn   *transport.Client
	kind   site.Kind
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return s.conn, site.Info{Kind: s.kind}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	return &site.Metadata{Info: site.Info{Kind: s.kind}}, nil
}

func (s *stubSession) Idempotency() *idem.Ledger       { return nil }
func (s *stubSession) Project() string                 { return "ENG" }
func (s *stubSession) RequireProject() (string, error) { return "ENG", nil }
func (s *stubSession) Board() string                   { return "" }

// RequireBoard is what an agile command calls. None of these fixtures set a
// board, so it fails the way the real session does rather than returning one.
// Fields is the context default field set. Empty here: a stub that
// invented one would make every resource test assert a request nobody made.
func (s *stubSession) Fields() []string { return s.fields }

func (s *stubSession) RequireBoard() (string, error) {
	return "", errs.Usage("NO_BOARD", "this command needs a board and none is set")
}
func (s *stubSession) CheckWritable(string) error { return nil }

// TestSearchAndGetRunAsCommands exercises the wrappers on both deployments.
func TestSearchAndGetRunAsCommands(t *testing.T) {
	for _, kind := range deployments {
		t.Run("list/"+string(kind), func(t *testing.T) {
			cmd, ok := registry.Lookup("user.list")
			if !ok {
				t.Fatal("user list is not registered")
			}

			conn, replayer := replayConn(t, "search."+string(kind)+".json")
			inv := &registry.Invocation{
				Jira: &stubSession{conn: conn, kind: kind},
				Args: []string{"ada"}, Flags: registry.NewFlags(),
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
				t.Error("a whole result was reported incomplete")
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the search was never sent: %v", unplayed)
			}
			lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
			if lines[0] != "id\tdisplay\temail\tactive" {
				t.Errorf("header = %q", lines[0])
			}
			// One on Cloud because the recorded sandbox has one user, two on
			// Data Center because that fixture is constructed.
			wantRows := 2
			if kind == site.Cloud {
				wantRows = 1
			}
			if len(lines) != wantRows+1 {
				t.Errorf("got %d rows, want %d:\n%s",
					len(lines)-1, wantRows, buf.String())
			}
		})

		t.Run("get/"+string(kind), func(t *testing.T) {
			cmd, _ := registry.Lookup("user.get")
			conn, _ := replayConn(t, "user."+string(kind)+".json")
			id := "ada"
			if kind == site.Cloud {
				id = recordedAccountID
			}
			inv := &registry.Invocation{
				Jira: &stubSession{conn: conn, kind: kind},
				Args: []string{id}, Flags: registry.NewFlags(),
				Stderr: io.Discard, Progress: registry.NoProgress,
			}
			doc, err := cmd.Run(t.Context(), inv)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if got, _ := doc.Record.AttrValue("id"); got != id {
				t.Errorf("id = %q, want %q", got, id)
			}
		})
	}
}

// TestUserCommandsWithoutASessionFailLoudly covers the guard each one carries.
func TestUserCommandsWithoutASessionFailLoudly(t *testing.T) {
	get, _ := registry.Lookup("user.get")
	if _, err := get.Run(t.Context(), &registry.Invocation{
		Args: []string{"ada"}, Flags: registry.NewFlags(),
	}); err == nil {
		t.Error("user get ran without a session")
	}

	me, _ := registry.Lookup("user.me")
	if _, err := me.Run(t.Context(), &registry.Invocation{
		Flags: registry.NewFlags(),
	}); err == nil {
		t.Error("user me ran without a session")
	}
}

// TestTheCloudFixturesAreRecordings guards what the other tests here rest on.
//
// TestGetUsesTheRightParameter and TestTheIdIsTheDeploymentsOwn are only worth
// anything because the conversation they replay is one a real Jira had: the
// replayer matches on path and query, so a mismatch means this code builds a
// request the server never answered. Replace the fixture with a hand-written
// one and those tests still pass while proving nothing, which is exactly the
// state this resource was in before.
func TestTheCloudFixturesAreRecordings(t *testing.T) {
	for _, name := range []string{
		"search.cloud.json", "me.cloud.json", "user.cloud.json",
	} {
		cassette, err := transport.LoadCassette(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if !cassette.Evidence() {
			t.Errorf("%s is no longer a recording, so the tests that replay it "+
				"assert nothing about the API", name)
		}
	}
}

// TestAFullPageIsNeverReportedComplete covers the one listing where the server,
// not this client, decides how much comes back.
//
// `/user/search` answers with a bare array — no total, no isLast — so a page as
// large as the bound it was given is the only evidence there is that more
// exist. Every other listing pages to exhaustion first and can compare what it
// holds against what was asked for; this one cannot, and a result that cannot
// prove it is exhaustive must not claim to be.
//
// Both halves matter. A bounded --limit truncates against a directory that has
// more, and --limit all is bounded too — this command does not page — so it has
// the same duty to say so.
func TestAFullPageIsNeverReportedComplete(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    registry.Limit
		wantRows int
	}{
		{"limit all", registry.Limit{All: true}, registry.DefaultLimit},
		{"bounded", registry.Limit{N: 5}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok := registry.Lookup("user.list")
			if !ok {
				t.Fatal("user list is not registered")
			}
			conn, _ := replayConn(t, "search-full.cloud.json")
			inv := &registry.Invocation{
				Jira: &stubSession{conn: conn, kind: site.Cloud},
				Args: []string{"ada"}, Flags: registry.NewFlags(),
				Limit:  tc.limit,
				Stderr: io.Discard, Progress: registry.NoProgress,
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

			if result.Complete {
				t.Error("a page as large as the bound it was given was reported " +
					"complete; nothing in the response says the directory held no more")
			}
			// The bound is the caller's and is honoured exactly: a probe row
			// fetched to detect truncation is not a row the caller asked for.
			if rows := strings.Count(strings.TrimRight(buf.String(), "\n"), "\n"); rows != tc.wantRows {
				t.Errorf("emitted %d rows, want %d:\n%s", rows, tc.wantRows, buf.String())
			}
		})
	}
}

// TestMeReportsTheClockJiraAnswersOn is the whole reason user.get went to v2.
//
// Jira evaluates every relative date and every startOf/endOf function in the
// timezone on the *account's* profile — not UTC, and not the clock of the
// machine running this tool. Measured against the sandbox on 2026-08-10 from a
// host running UTC: an issue created at 14:02:37Z is matched by
// `--created-after "2026-08-10 09:02"` and not by `"09:03"`, and by
// `startOfDay("+9h")` and not by `startOfDay("+10h")`. Both put the day
// boundary at 05:00Z, five hours after the caller's midnight, and the account
// says America/Chicago, which in August is UTC-5.
//
// So `--created-after startOfDay()` means "since 05:00Z" and reports itself
// complete at exit 0, and until this field was published there was nothing in
// the tool that could tell a caller which day they had asked for.
//
// The evidence needed no new recording. me.cloud.json has carried the field
// since it was recorded and nothing parsed it, which is its own lesson about
// what a fixture is worth: the answer was committed before the question.
func TestMeReportsTheClockJiraAnswersOn(t *testing.T) {
	cmd, ok := registry.Lookup("user.me")
	if !ok {
		t.Fatal("user me is not registered")
	}
	conn, _ := replayConn(t, "me.cloud.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.Cloud}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("user me: %v", err)
	}
	zone, ok := doc.Record.ChildNamed("timezone")
	if !ok {
		t.Fatal("user me reports no timezone, so nothing in this tool says " +
			"which clock a relative date is evaluated on")
	}
	if zone.Text != "America/Chicago" {
		t.Errorf("timezone = %q, want the zone the recording carries", zone.Text)
	}
	// A zone that names no zone is worse than none: a caller would compute
	// against it. It has to be loadable.
	if _, err := time.LoadLocation(zone.Text); err != nil {
		t.Errorf("timezone %q does not name an IANA zone: %v", zone.Text, err)
	}
}

// TestTheTimezoneIsReportedWhereverTheServerSendsIt is the other half, and it
// corrected the belief that produced it.
//
// The element was made optional on the reasoning that a search does not
// disclose a timezone and only /myself does. The recordings say otherwise: both
// search.cloud.json and user.cloud.json carry timeZone, so a search on this
// deployment discloses it too. Optional is still right, for the reason email is
// optional — Data Center's fixtures carry none and a Cloud privacy setting can
// withhold it — but "absent because the endpoint never sends it" was wrong, and
// a comment saying so would have outlived the check.
//
// It renders XML rather than TSV, and the first version of it did not. TSV
// emits the declared column set, which has never contained a timezone, so the
// assertion held whatever the code did: fabricating a zone in rawUser.convert
// left it green. A test whose subject is an element the default projection
// drops has to read a format that carries every element.
func TestTheTimezoneIsReportedWhereverTheServerSendsIt(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		kind    site.Kind
		want    bool
	}{
		// Recorded: this Cloud site discloses the zone on a search.
		{"search.cloud.json", site.Cloud, true},
		// Constructed, and the shape a privacy setting produces: no email and
		// no zone. Absent is absent, never an empty element.
		{"search-private.cloud.json", site.Cloud, false},
		// Data Center, hand-written, and it sends neither.
		{"search.datacenter.json", site.DataCenter, false},
	} {
		cmd, ok := registry.Lookup("user.list")
		if !ok {
			t.Fatal("user list is not registered")
		}
		conn, _ := replayConn(t, tc.fixture)

		var buf strings.Builder
		stream, err := render.NewStream(&buf, render.XML, render.StreamSpec{
			Kind: cmd.Kind(), Version: cmd.KindVersion(),
			Name: cmd.CollectionName, Columns: cmd.Columns,
		})
		if err != nil {
			t.Fatalf("%s: stream: %v", tc.fixture, err)
		}
		result, err := cmd.Stream(t.Context(), &registry.Invocation{
			Jira: &stubSession{conn: conn, kind: tc.kind}, Args: []string{"ada"},
			Flags: registry.NewFlags(), Limit: registry.Limit{N: registry.DefaultLimit},
			Stderr: io.Discard, Progress: registry.NoProgress,
		}, stream)
		if err != nil {
			t.Fatalf("%s: user list: %v", tc.fixture, err)
		}
		if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
			t.Fatalf("%s: close: %v", tc.fixture, err)
		}

		// The display name proves the rows reached the buffer, so a missing
		// timezone is a fact about the output and not about an empty document.
		if !strings.Contains(buf.String(), "<display>") {
			t.Fatalf("%s: rendered no users: %s", tc.fixture, buf.String())
		}
		if got := strings.Contains(buf.String(), "<timezone>"); got != tc.want {
			t.Errorf("%s: timezone present = %v, want %v:\n%s",
				tc.fixture, got, tc.want, buf.String())
		}
	}
}

// Site implements registry.Session. A fixed value, because provenance is a
// property of the answer and these tests assert documents.
func (s *stubSession) Site() string {
	if s.site != "" {
		return s.site
	}
	return "https://recorded.invalid"
}
