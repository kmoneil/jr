package user_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/user"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
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

			users, err := client.Search(t.Context(), "ada", 50)
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

	users, err := client.Search(t.Context(), "ada", 50)
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

	users, err := client.Search(t.Context(), "ada", 50)
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
	dcUsers, err := dc.Search(t.Context(), "ada", 50)
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

	users, err := client.Search(t.Context(), "ada", 50)
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
	node := user.User{
		ID: "ada", Display: "Ada Lovelace", Email: "ada@example.invalid", Active: true,
	}.Node()
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
	conn *transport.Client
	kind site.Kind
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
