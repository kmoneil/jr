//go:build write

package sprint_test

import (
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/sprint"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestCreateReportsTheSprintTheServerMade covers the whole create path against
// a response carrying everything the endpoint accepts.
//
// The document is built from the response and not from the request, which is
// what makes the id in it usable: a caller creating a sprint has no other way
// to learn what it was numbered.
func TestCreateReportsTheSprintTheServerMade(t *testing.T) {
	flags := registry.NewFlags()
	flags.SetString("start", "2026-07-15T09:00:00Z")
	flags.SetString("end", "2026-07-29T09:00:00Z")
	flags.SetString("goal", "Keyset pagination")

	doc, replayer := runWrite(t, "sprint.create", "create.datacenter.json",
		[]string{"ENG Sprint 6"}, flags)

	if id, _ := doc.Record.AttrValue("id"); id != "102" {
		t.Errorf("id = %q, want the id the server assigned", id)
	}
	if state, _ := doc.Record.AttrValue("state"); state != "future" {
		t.Errorf("state = %q, want future: a created sprint has not started", state)
	}
	if action, _ := doc.Record.AttrValue("action"); action != "created" {
		t.Errorf("action = %q, want created", action)
	}
	if board, _ := doc.Record.AttrValue("board"); board != "99" {
		t.Errorf("board = %q, want the board it was created on", board)
	}
	// Data Center writes an offset with no colon, which time.RFC3339 refuses.
	// Reaching the document as RFC 3339 in UTC is normalizeTime doing its job on
	// a value that arrived through the write path rather than through a read.
	start, ok := doc.Record.ChildNamed("start")
	if !ok || start.Text != "2026-07-15T09:00:00Z" {
		t.Errorf("start = %+v, want the Data Center date normalized", start)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the create did not make its request: %v", unplayed)
	}
}

// TestCreateSendsOnlyWhatItWasGiven pins the body against the flags.
//
// An absent flag has to be absent from the body rather than present and empty:
// the API reads the two differently, and a sprint created with "goal": "" is
// not a sprint created without a goal.
func TestCreateSendsOnlyWhatItWasGiven(t *testing.T) {
	client := &sprint.Client{Site: site.Info{Kind: site.Cloud}}

	req, err := client.CreateRequest("99", "ENG Sprint 6", sprint.Window{}, "")
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if got, want := string(req.Body), `{"name":"ENG Sprint 6","originBoardId":99}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	// The board is a JSON number and not a string. Jira refuses a quoted
	// originBoardId, and the id reaches here as digits either way, so the
	// difference is invisible anywhere but on the wire.
	if strings.Contains(string(req.Body), `"99"`) {
		t.Error("originBoardId was quoted; the endpoint wants a number")
	}
	if req.Method != transport.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.Path != "/rest/agile/1.0/sprint" {
		t.Errorf("path = %q, want the agile collection", req.Path)
	}
}

// TestStartReadsBeforeItWrites is the flag half of the start path: the sprint
// carries no dates and --start and --end supply them.
func TestStartReadsBeforeItWrites(t *testing.T) {
	flags := registry.NewFlags()
	flags.SetString("start", "2026-07-15T09:00:00Z")
	flags.SetString("end", "2026-07-29T09:00:00Z")

	doc, replayer := runWrite(t, "sprint.start", "start.datacenter.json",
		[]string{"99"}, flags)

	if state, _ := doc.Record.AttrValue("state"); state != "active" {
		t.Errorf("state = %q, want active", state)
	}
	if action, _ := doc.Record.AttrValue("action"); action != "started" {
		t.Errorf("action = %q, want started", action)
	}
	end, ok := doc.Record.ChildNamed("end")
	if !ok || end.Text != "2026-07-29T09:00:00Z" {
		t.Errorf("end = %+v, want the window the flags asked for", end)
	}
	// Both interactions: a start that skipped the precondition read would leave
	// the GET unplayed.
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the start did not make both calls: %v", unplayed)
	}
}

// TestARunningSprintIsNotStartedAgain and TestAClosedSprintIsNotStarted cover
// the precondition. Neither cassette carries a POST, so a start that went ahead
// anyway fails to match rather than passing quietly.
func TestARunningSprintIsNotStartedAgain(t *testing.T) {
	replayer, err := runWriteErr(t, "sprint.start", "startactive.datacenter.json",
		[]string{"100"}, registry.NewFlags())

	e := errs.Coerce(err)
	if e.Code != "SPRINT_NOT_FUTURE" {
		t.Fatalf("code = %q, want SPRINT_NOT_FUTURE", e.Code)
	}
	if !strings.Contains(e.Remedy, "already running") {
		t.Errorf("remedy = %q, want it to say the sprint is already running", e.Remedy)
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request was made past the refusal: %v", unmatched)
	}
}

func TestAClosedSprintIsNotStarted(t *testing.T) {
	replayer, err := runWriteErr(t, "sprint.start", "startclosed.datacenter.json",
		[]string{"101"}, registry.NewFlags())

	e := errs.Coerce(err)
	if e.Code != "SPRINT_NOT_FUTURE" {
		t.Fatalf("code = %q, want SPRINT_NOT_FUTURE", e.Code)
	}
	// The two wrong states need different remedies. One is running and can be
	// closed; the other is over, and no API reopens it.
	if !strings.Contains(e.Remedy, "reopened") {
		t.Errorf("remedy = %q, want it to say a closed sprint cannot be reopened",
			e.Remedy)
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request was made past the refusal: %v", unmatched)
	}
}

// TestTheWindowBelongsToTheSprintAndNotToTheRequest is the finding this verb
// was designed around, held against bytes a server actually sent.
//
// Jira will not run a sprint that has no dates — an empty body comes back as
// "startDate: You must specify a start date for the sprint". It does not follow
// that the request must carry them: a sprint created with its window starts
// from a body holding nothing but the state, which is what the recording shows.
// Requiring --start and --end unconditionally would have refused a request the
// server accepts, and a hand-written fixture would have agreed with whichever
// rule its author believed, because its author writes both halves.
func TestTheWindowBelongsToTheSprintAndNotToTheRequest(t *testing.T) {
	cmd, replayer := recordedCommand(t, "sprint.start", "start-recorded.cloud.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: replayer.conn, kind: site.Cloud},
		Args: []string{"4"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the requests this code builds are not the ones the server "+
			"answered: %v", err)
	}
	if state, _ := doc.Record.AttrValue("state"); state != "active" {
		t.Errorf("state = %q, want active", state)
	}
	// The dates in the document were never sent. They are the sprint's own,
	// echoed back by the server, which is why a start with no flags can report a
	// window at all.
	start, ok := doc.Record.ChildNamed("start")
	if !ok || start.Text != "2026-08-17T09:00:00Z" {
		t.Errorf("start = %+v, want the sprint's own start date", start)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the start did not make both calls: %v", unplayed)
	}

	// The recorded body is the assertion. A start that resent the sprint's dates
	// would work against this server and would still be a different request from
	// the one it answered.
	write := replayer.cassette.Interactions[1]
	if got := write.Request.Body; got != `{"state":"active"}` {
		t.Errorf("recorded body = %s, want only the field that changes", got)
	}
}

// TestASprintWithNoDatesIsRefusedBeforeTheWrite is the same rule from the other
// side: when the sprint supplies nothing, the flags are required.
//
// The cassette holds one interaction and that is the point of it. Asserting the
// exit code alone would pass while the POST still went out, so the forbidden
// request is left out of the fixture and the replayer is asked whether anything
// went looking for it.
func TestASprintWithNoDatesIsRefusedBeforeTheWrite(t *testing.T) {
	cmd, replayer := recordedCommand(t, "sprint.start",
		"start-undated-recorded.cloud.json")

	_, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: replayer.conn, kind: site.Cloud},
		Args: []string{"5"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("a sprint with no dates was started")
	}
	e := errs.Coerce(err)
	if e.Code != "SPRINT_HAS_NO_DATES" {
		t.Fatalf("code = %q, want SPRINT_HAS_NO_DATES", e.Code)
	}
	// Both flags are named, because both are missing, and a remedy that named
	// one of them would send the caller back for a second refusal.
	for _, flag := range []string{"--start", "--end"} {
		if !strings.Contains(e.Remedy, flag) {
			t.Errorf("remedy = %q, want it to name %s", e.Remedy, flag)
		}
	}
	if len(replayer.cassette.Interactions) != 1 {
		t.Fatalf("the fixture holds %d interactions; the whole assertion is that "+
			"it holds only the read", len(replayer.cassette.Interactions))
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request was made past the refusal: %v", unmatched)
	}
}

// TestOnlyTheMissingHalfIsDemanded covers the case between the two above: the
// sprint has a start and no end, so --end alone is what is asked for, and
// supplying it is enough.
func TestOnlyTheMissingHalfIsDemanded(t *testing.T) {
	replayer, err := runWriteErr(t, "sprint.start", "starthalfdated.datacenter.json",
		[]string{"103"}, registry.NewFlags())

	e := errs.Coerce(err)
	if e.Code != "SPRINT_HAS_NO_DATES" {
		t.Fatalf("code = %q, want SPRINT_HAS_NO_DATES", e.Code)
	}
	// The half the sprint already holds is not asked for. A refusal naming both
	// would send the caller to re-supply a date the sprint has.
	if strings.Contains(e.Message, "start date") {
		t.Errorf("message = %q, want it to ask only for the end date", e.Message)
	}
	if !strings.Contains(e.Message, "end date") {
		t.Errorf("message = %q, want it to name the end date", e.Message)
	}
	if strings.Contains(e.Remedy, "--start") {
		t.Errorf("remedy = %q, want it to ask only for --end", e.Remedy)
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request was made past the refusal: %v", unmatched)
	}

	// And the same sprint starts once the flag supplies the missing half. The
	// body carries only the end date, because the start is the sprint's own.
	flags := registry.NewFlags()
	flags.SetString("end", "2026-07-29T09:00:00Z")
	doc, replayer := runWrite(t, "sprint.start", "starthalfdated.datacenter.json",
		[]string{"103"}, flags)

	if state, _ := doc.Record.AttrValue("state"); state != "active" {
		t.Errorf("state = %q, want active", state)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the start did not make both calls: %v", unplayed)
	}
}

// TestABackwardsWindowIsRefusedWithoutARequest covers a rule Jira has too:
// "the start date of a sprint must be before the end date". Refusing locally is
// the same verdict without the round trip.
func TestABackwardsWindowIsRefusedWithoutARequest(t *testing.T) {
	flags := registry.NewFlags()
	flags.SetString("start", "2026-09-30T09:00:00Z")
	flags.SetString("end", "2026-09-01T09:00:00Z")

	cmd, ok := registry.Lookup("sprint.create")
	if !ok {
		t.Fatal("sprint create is not registered")
	}
	inv := &registry.Invocation{
		Jira: &stubSession{board: "99"}, Args: []string{"ENG Sprint 6"},
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	}
	// Validate, not Run: a window this tool will not build is refused before
	// anything reaches the session at all.
	err := cmd.Validate(t.Context(), inv)
	if err == nil {
		t.Fatal("a sprint that ends before it starts was accepted")
	}
	if code := errs.Coerce(err).Code; code != "INVALID_SPRINT_WINDOW" {
		t.Errorf("code = %q, want INVALID_SPRINT_WINDOW", code)
	}

	// The same rule against a sprint's own start date, which is the form a
	// caller reaches by passing --end alone. Nothing typed on this command line
	// is backwards; the window it would produce is.
	endOnly := registry.NewFlags()
	endOnly.SetString("end", "2026-07-01T09:00:00Z")
	replayer, err := runWriteErr(t, "sprint.start", "starthalfdated.datacenter.json",
		[]string{"103"}, endOnly)

	if code := errs.Coerce(err).Code; code != "INVALID_SPRINT_WINDOW" {
		t.Errorf("code = %q, want the sprint's own start to be what --end is "+
			"checked against", code)
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request was made past the refusal: %v", unmatched)
	}
}

// TestADateIsAWholeInstantOrItIsRefused covers the input format.
//
// A bare date names no time and no zone. Accepting one would mean choosing when
// somebody's iteration begins on their behalf, which is the guess §5.2 exists
// to refuse; an offset is a different matter, being a spelling of an instant
// rather than a different instant.
func TestADateIsAWholeInstantOrItIsRefused(t *testing.T) {
	for _, bad := range []string{
		"2026-08-11",
		"2026-08-11 09:00:00",
		"2026-08-11T09:00:00",
		"tomorrow",
		"-7d",
	} {
		flags := registry.NewFlags()
		flags.SetString("start", bad)

		cmd, _ := registry.Lookup("sprint.start")
		err := cmd.Validate(t.Context(), &registry.Invocation{
			Args: []string{"99"}, Flags: flags,
			Stderr: io.Discard, Progress: registry.NoProgress,
		})
		if err == nil {
			t.Errorf("--start %q was accepted", bad)
			continue
		}
		if code := errs.Coerce(err).Code; code != "INVALID_SPRINT_DATE" {
			t.Errorf("--start %q: code = %q, want INVALID_SPRINT_DATE", bad, code)
		}
	}
}

// TestAnOffsetIsNormalizedRatherThanRefused is the other half of that rule. The
// value sent is the value the read path would report, so a caller comparing the
// two is comparing like with like.
func TestAnOffsetIsNormalizedRatherThanRefused(t *testing.T) {
	flags := registry.NewFlags()
	flags.SetString("start", "2026-08-11T11:00:00+02:00")
	flags.SetString("end", "2026-08-25T11:00:00+02:00")
	flags.SetBool("dry-run", true)

	doc, _ := runWrite(t, "sprint.create", "create.datacenter.json",
		[]string{"ENG Sprint 6"}, flags)

	body, ok := doc.Record.Children[0].ChildNamed("body")
	if !ok {
		t.Fatal("the dry run printed no body")
	}
	// Same instant, one spelling. Two hours ahead of UTC at eleven is nine
	// o'clock Zulu, and the value sent is now the value the read path reports.
	want := `{"endDate":"2026-08-25T09:00:00Z","name":"ENG Sprint 6",` +
		`"originBoardId":99,"startDate":"2026-08-11T09:00:00Z"}`
	if body.Text != want {
		t.Errorf("body = %q, want the offsets normalized to UTC", body.Text)
	}
}

// TestStartDryRunPrintsTheRequestAndSendsNothing covers the flag every mutating
// verb carries. The read still happens, so a dry run also tells you whether the
// start would be allowed.
func TestStartDryRunPrintsTheRequestAndSendsNothing(t *testing.T) {
	flags := registry.NewFlags()
	flags.SetString("start", "2026-07-15T09:00:00Z")
	flags.SetString("end", "2026-07-29T09:00:00Z")
	flags.SetBool("dry-run", true)

	doc, replayer := runWrite(t, "sprint.start", "start.datacenter.json",
		[]string{"99"}, flags)

	if doc.Kind != registry.DryRunOutput().Kind {
		t.Errorf("kind = %q, want the dry-run kind", doc.Kind)
	}
	// The POST is still in the cassette and was never played, which is the
	// assertion: nothing was sent.
	unplayed := replayer.Unplayed()
	if len(unplayed) != 1 || !strings.HasPrefix(unplayed[0], "POST ") {
		t.Errorf("unplayed = %v, want exactly the POST", unplayed)
	}
	body, ok := doc.Record.Children[0].ChildNamed("body")
	if !ok {
		t.Fatal("the dry run printed no body")
	}
	want := `{"endDate":"2026-07-29T09:00:00Z","startDate":"2026-07-15T09:00:00Z",` +
		`"state":"active"}`
	if body.Text != want {
		t.Errorf("body = %q, want the bytes that would have gone out", body.Text)
	}
}

// TestTheWriteVerbsAreDeclaredForWhatTheyDo is the declaration the CLI enforces
// from: read-only mode, --dry-run, and the tag gate all come from here.
func TestTheWriteVerbsAreDeclaredForWhatTheyDo(t *testing.T) {
	for _, name := range []string{"sprint.create", "sprint.start"} {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if !cmd.Mutating {
			t.Errorf("%s is not marked mutating, so read-only would not refuse it", name)
		}
		// Neither is destructive, and that is the asymmetry with sprint close.
		// Starting a sprint is undone by closing it; closing one returns every
		// unfinished issue to the backlog and no API reopens it.
		if cmd.Destructive {
			t.Errorf("%s is marked destructive, which would demand --yes for "+
				"something a later command undoes", name)
		}
		if !slices.Contains(cmd.RequiresTags, "write") {
			t.Errorf("%s does not require the write tag: %v", name, cmd.RequiresTags)
		}
		if slices.Contains(cmd.RequiresTags, "admin") {
			t.Errorf("%s requires admin; only ending an iteration does", name)
		}
		if !slices.ContainsFunc(cmd.Flags, func(f registry.Flag) bool {
			return f.Name == "dry-run"
		}) {
			t.Errorf("%s declares no --dry-run", name)
		}
	}
}

// TestTheRecordedCreateIsAConversationAServerHad establishes that the endpoint
// and the body this code builds are the ones a Cloud instance answered with a
// 201. The constructed fixture beside it decides both halves of the exchange
// and so can establish only that the request is unchanged.
func TestTheRecordedCreateIsAConversationAServerHad(t *testing.T) {
	cmd, replayer := recordedCommand(t, "sprint.create", "create-recorded.cloud.json")

	flags := registry.NewFlags()
	flags.SetString("start", "2026-08-17T09:00:00Z")
	flags.SetString("end", "2026-08-31T09:00:00Z")
	flags.SetString("goal", "Ship the importer")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: replayer.conn, kind: site.Cloud, board: "3"},
		Args: []string{"AGL Sprint 4"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if id, _ := doc.Record.AttrValue("id"); id != "4" {
		t.Errorf("id = %q, want the id the server assigned", id)
	}
	if state, _ := doc.Record.AttrValue("state"); state != "future" {
		t.Errorf("state = %q, want future", state)
	}
	// Jira answers with milliseconds and a Z where the request carried plain
	// RFC 3339 seconds. Both directions have to agree, and this is the only
	// place that agreement is checked against bytes a server sent.
	end, ok := doc.Record.ChildNamed("end")
	if !ok || end.Text != "2026-08-31T09:00:00Z" {
		t.Errorf("end = %+v, want the date sent, as the server reported it", end)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the create did not make its request: %v", unplayed)
	}
}

// TestTheRecordedBareCreateIsAConversationAServerHad is the minimum the
// endpoint takes, against a server rather than against our own builder.
//
// TestCreateSendsOnlyWhatItWasGiven asserts the same body and can only say that
// this tool still builds what it used to. This one says Jira accepted it: a
// name and an originBoardId, with no dates and no goal, and the sprint that
// comes back carries no dates either — which is the state the start verb then
// has to refuse.
func TestTheRecordedBareCreateIsAConversationAServerHad(t *testing.T) {
	cmd, replayer := recordedCommand(t, "sprint.create",
		"create-undated-recorded.cloud.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: replayer.conn, kind: site.Cloud, board: "3"},
		Args: []string{"AGL Sprint 5"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if state, _ := doc.Record.AttrValue("state"); state != "future" {
		t.Errorf("state = %q, want future", state)
	}
	// Absent, not empty and not a zero time. A sprint with no dates is the
	// normal result of planning one, and the document has to be able to say so.
	for _, name := range []string{"start", "end"} {
		if child, ok := doc.Record.ChildNamed(name); ok {
			t.Errorf("%s = %+v, want no element at all", name, child)
		}
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the create did not make its request: %v", unplayed)
	}
}

// TestTheCreateAnswersTwoHundredAndOne pins the divergence a caller would
// otherwise meet at runtime: the other agile writes in this tree answer 204,
// close answers 200, and this one answers 201 with the sprint it made.
func TestTheCreateAnswersTwoHundredAndOne(t *testing.T) {
	cassette, err := transport.LoadCassette(
		filepath.Join("testdata", "create-recorded.cloud.json"),
	)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cassette.Interactions) != 1 {
		t.Fatalf("got %d interactions, want the one write", len(cassette.Interactions))
	}
	made := cassette.Interactions[0]
	if made.Response.Status != 201 {
		t.Errorf("status = %d, want 201", made.Response.Status)
	}
	// Without an id in the body a caller has no way to address what they just
	// made, so the response being a sprint is load-bearing rather than incidental.
	if !strings.Contains(made.Response.Body, `"id":4`) {
		t.Errorf("the response carries no id: %s", made.Response.Body)
	}
}

// runWrite runs a write verb against a cassette and requires it to succeed.
func runWrite(
	t *testing.T, command, fixture string, args []string, flags registry.Flags,
) (*render.Doc, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup(command)
	if !ok {
		t.Fatalf("%s is not registered", command)
	}
	conn, replayer := replayConn(t, fixture)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.DataCenter, board: "99"},
		Args:  args,
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	return doc, replayer
}

// runWriteErr is runWrite for the paths that must not succeed.
func runWriteErr(
	t *testing.T, command, fixture string, args []string, flags registry.Flags,
) (*transport.Replayer, error) {
	t.Helper()

	cmd, ok := registry.Lookup(command)
	if !ok {
		t.Fatalf("%s is not registered", command)
	}
	conn, replayer := replayConn(t, fixture)

	_, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.DataCenter, board: "99"},
		Args:  args,
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatalf("%s succeeded against %s", command, fixture)
	}
	return replayer, err
}

// recordedReplayer is a replayer plus the two things a recorded test asks of
// the cassette itself: the connection it backs, and the interactions, so a test
// can assert the bytes a server actually answered.
type recordedReplayer struct {
	*transport.Replayer
	conn     *transport.Client
	cassette *transport.Cassette
}

// recordedCommand loads a recording and refuses a cassette that has quietly
// become hand-written, which would replay identically and assert nothing about
// the API.
func recordedCommand(
	t *testing.T, command, fixture string,
) (*registry.Command, *recordedReplayer) {
	t.Helper()

	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	if !cassette.Evidence() {
		t.Fatalf("%s is not a recording, so replaying it establishes nothing "+
			"about the API", fixture)
	}
	cmd, ok := registry.Lookup(command)
	if !ok {
		t.Fatalf("%s is not registered", command)
	}
	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return cmd, &recordedReplayer{Replayer: replayer, conn: conn, cassette: cassette}
}
