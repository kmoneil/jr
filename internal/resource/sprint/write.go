//go:build write

// Opening a sprint is ordinary work, so unlike close.go this needs the write
// tag and nothing else. The asymmetry is deliberate and is the safe direction:
// an agent build can plan an iteration and begin one, and cannot end one.
// Starting a sprint is undone by closing it; closing it returns every
// unfinished issue to the backlog and Jira offers no undo.

package sprint

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kinds and schema versions the write verbs emit.
const (
	KindCreate    = "sprint.create"
	VersionCreate = 1
	KindStart     = "sprint.start"
	VersionStart  = 1
)

func init() {
	registry.Register(createCommand())
	registry.Register(startCommand())

	render.RegisterSchema(KindCreate, WriteSchema("created"))
	render.RegisterSchema(KindStart, WriteSchema("started"))
}

// WriteSchema is the shape of a sprint a write verb just changed: the sprint as
// the server now holds it, plus what was done to it.
//
// It is the read shape with an action attribute, because both endpoints answer
// with the whole sprint and reporting less than the server said would make a
// caller ask for it again. `completed` is the one leaf left out — a sprint that
// was just created or just started has no completion date, and declaring a
// child that cannot occur describes a shape no build emits.
//
// state keeps the full enum rather than the one value each verb produces. The
// document reports what the sprint now is and the action says what was asked
// for, so a server that answered with some other state renders a visible
// contradiction instead of failing to render at all. A write's echo never fails
// describing what it did.
func WriteSchema(action string) *render.Schema {
	return &render.Schema{
		Element: "sprint",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeInt},
			{Name: "state", Type: render.TypeString, Enum: States},
			{Name: "board", Type: render.TypeString, Optional: true},
			{Name: "action", Type: render.TypeString, Enum: []string{action}},
		},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
			{Schema: render.Leaf("goal", render.TypeString), Optional: true},
			{Schema: render.Leaf("start", render.TypeTimestamp), Optional: true},
			{Schema: render.Leaf("end", render.TypeTimestamp), Optional: true},
		},
	}
}

// writeExits are the statuses both verbs can end with. Blocked is what
// read-only mode produces; Conflict is a precondition that failed, such as
// starting a sprint that is already running.
func writeExits() []exitcode.Code {
	return []exitcode.Code{
		exitcode.Blocked, exitcode.Auth, exitcode.NotFound, exitcode.Permission,
		exitcode.Conflict, exitcode.RateLimit, exitcode.Remote,
	}
}

// dryRunFlag is declared rather than added implicitly, because the contract
// test reads the declaration — and a flag that exists only at runtime is one
// `jr schema` cannot describe.
func dryRunFlag() registry.Flag {
	return registry.Flag{
		Name: "dry-run", Type: registry.TypeBool,
		Usage: "print the request that would be sent, and send nothing",
	}
}

// dateFlags are the window a sprint runs for. They are named for the columns
// they end up in — `sprint list` reports `start` and `end` — rather than for
// the fields the API calls them, so one thing has one name.
func dateFlags() []registry.Flag {
	return []registry.Flag{
		{
			Name: "start", Type: registry.TypeString,
			Usage: "when the sprint begins, RFC 3339, e.g. 2026-08-11T09:00:00Z",
		},
		{
			Name: "end", Type: registry.TypeString,
			Usage: "when the sprint is due to end, RFC 3339",
		},
	}
}

func createCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"sprint", "create"},
		Summary: "Create a future sprint on a board",
		Description: strings.TrimSpace(`
Adds an unstarted sprint to a board.

The sprint is created in the future state. Nothing about the board changes for
anybody working on it until the sprint is started, so this is safe to run: an
unstarted sprint holds no issues and appears only in the backlog view.

The board comes from --board, JIRA_BOARD, or the context, exactly as for
` + "`" + buildinfo.App + ` sprint list` + "`" + `. Only a scrum board has sprints, and a kanban board is
refused by the server.

--start and --end are optional here. A sprint can be planned without dates and
given them when it is started, which is what the Jira UI does; passing them now
records the intended window up front.

--dry-run prints the exact request, body included, and sends nothing.`),
		Example: strings.Join([]string{
			buildinfo.App + " sprint create \"Sprint 14\" --board 42",
			buildinfo.App + " sprint create \"Sprint 14\" --goal \"Ship the importer\"",
			buildinfo.App + " sprint create \"Sprint 14\" --start 2026-08-11T09:00:00Z --end 2026-08-25T09:00:00Z",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "name", Usage: "what the sprint is called, e.g. \"Sprint 14\"", Required: true,
		}},
		Flags: append(
			dateFlags(),
			registry.Flag{
				Name: "goal", Type: registry.TypeString,
				Usage: "what the sprint is for",
			},
			dryRunFlag(),
		),
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindCreate, Version: VersionCreate},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateCreate,
		Run:       runCreate,
	}
}

// validateCreate refuses everything knowable without a request, including the
// board — a sprint has to be created on one, and a missing board is a usage
// error rather than something to discover from a 404.
func validateCreate(_ context.Context, inv *registry.Invocation) error {
	if len(inv.Args) == 0 || strings.TrimSpace(inv.Args[0]) == "" {
		return errs.Usage("INVALID_SPRINT_NAME", "a sprint name is required").
			WithRemedy("e.g. `%s sprint create \"Sprint 14\"`", buildinfo.App)
	}
	if _, err := sprintWindow(inv); err != nil {
		return err
	}
	if inv.Jira == nil {
		return errs.Runtime("NO_SESSION", "sprint create has no connection to Jira")
	}
	boardID, err := inv.Jira.RequireBoard()
	if err != nil {
		return err
	}
	return ValidateBoardID(boardID)
}

// CreateRequest builds the create without sending it.
func (c *Client) CreateRequest(
	boardID, name string, window Window, goal string,
) (transport.Request, error) {
	if err := ValidateBoardID(boardID); err != nil {
		return transport.Request{}, err
	}
	// json.Number rather than an int: the id is already known to be digits, and
	// converting it through strconv would put a range limit on a value the
	// server owns.
	fields := map[string]any{"name": name, "originBoardId": json.Number(boardID)}
	if goal != "" {
		fields["goal"] = goal
	}
	window.addTo(fields)

	body, err := json.Marshal(fields)
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the sprint").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPost,
		Path:   c.Site.AgileBase() + "/sprint",
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   body,
	}, nil
}

func runCreate(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "sprint create")
	if err != nil {
		return nil, err
	}
	boardID, err := inv.Jira.RequireBoard()
	if err != nil {
		return nil, err
	}
	window, err := sprintWindow(inv)
	if err != nil {
		return nil, err
	}

	req, err := client.CreateRequest(
		boardID, inv.Args[0], window, inv.Flags.String("goal"),
	)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc(KindCreate, req), nil
	}

	created, err := client.applied(ctx, req, boardID)
	if err != nil {
		return nil, err
	}
	return render.Record(KindCreate, VersionCreate,
		writeNode(created, "created")), nil
}

func startCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"sprint", "start"},
		Summary: "Start a future sprint",
		Description: strings.TrimSpace(`
Begins a sprint that has been planned but not yet started.

The sprint is read first, so a sprint that cannot be started is refused before
anything is sent. Only a future sprint can be started: an active one is already
running and a closed one cannot be reopened by any API, so both are a
precondition failure rather than a request worth making.

This is not destructive and takes no --yes. Starting a sprint is undone by
` + "`" + buildinfo.App + ` sprint close` + "`" + `, which is the half that needs both the write and admin
tags, because ending an iteration returns every unfinished issue to the backlog.

--dry-run prints the exact request, body included, and sends nothing. The read
still happens, so a dry run tells you whether the start would be allowed.`),
		Example: strings.Join([]string{
			buildinfo.App + " sprint start 128",
			buildinfo.App + " sprint start 128 --end 2026-08-25T09:00:00Z",
			buildinfo.App + " sprint start 128 --dry-run",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "id", Usage: "sprint id, from `jr sprint list`", Required: true,
		}},
		Flags:        append(dateFlags(), dryRunFlag()),
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindStart, Version: VersionStart},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateStart,
		Run:       runStart,
	}
}

func validateStart(_ context.Context, inv *registry.Invocation) error {
	if len(inv.Args) == 0 {
		return errs.Usage("INVALID_SPRINT_ID", "a sprint id is required")
	}
	if err := ValidateID(inv.Args[0]); err != nil {
		return err
	}
	_, err := sprintWindow(inv)
	return err
}

// StartRequest builds the start without sending it.
//
// The agile API spells a partial update as a POST to the sprint itself, with
// only the fields that change. A PUT would be a full replacement, and every
// field left out of it — the name, the goal, the dates — would be cleared.
func (c *Client) StartRequest(id string, window Window) (transport.Request, error) {
	if err := ValidateID(id); err != nil {
		return transport.Request{}, err
	}
	fields := map[string]any{"state": StateActive}
	window.addTo(fields)

	body, err := json.Marshal(fields)
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the start request").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPost,
		// Escaped even though ValidateID has already restricted it to digits.
		// Every other path in this package is built the same way, and a guard
		// that is only present where somebody judged it necessary is one that
		// gets left out the next time somebody judges.
		Path:   c.Site.AgileBase() + "/sprint/" + url.PathEscape(id),
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   body,
	}, nil
}

func runStart(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "sprint start")
	if err != nil {
		return nil, err
	}
	window, err := sprintWindow(inv)
	if err != nil {
		return nil, err
	}

	// Read before write, so a sprint that cannot be started costs one read and
	// no mutation.
	current, err := client.Get(ctx, inv.Args[0])
	if err != nil {
		return nil, err
	}
	if current.State != StateFuture {
		return nil, errs.New(exitcode.Conflict, "SPRINT_NOT_FUTURE",
			"sprint %s is %s, and only a future sprint can be started",
			current.ID, current.State).
			WithDetail("%q", current.Name).
			WithRemedy("%s", startRemedyFor(current.State))
	}
	if err := startable(current, window); err != nil {
		return nil, err
	}

	req, err := client.StartRequest(current.ID, window)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc(KindStart, req), nil
	}

	started, err := client.applied(ctx, req, current.Board)
	if err != nil {
		return nil, err
	}
	return render.Record(KindStart, VersionStart,
		writeNode(started, "started")), nil
}

// startable refuses a start Jira would refuse, without spending the round trip
// on finding out.
//
// Jira will not run a sprint that has no dates: the empty body comes back as
// "startDate: You must specify a start date for the sprint", and then the same
// again for the end. But the window it validates belongs to the sprint and not
// to the request — a sprint created with both dates starts from a body carrying
// nothing but the state, which is how the UI does it. So the flags are required
// exactly when the sprint has nothing for them to supply, and requiring them
// unconditionally would refuse a request the server accepts.
//
// The order check is against the same effective pair, because `--end` alone can
// land before a start date the sprint already holds.
func startable(current Sprint, window Window) error {
	start := firstNonEmpty(window.Start, current.Start)
	end := firstNonEmpty(window.End, current.End)

	var missing, flags []string
	if start == "" {
		missing, flags = append(missing, "start date"), append(flags, "--start")
	}
	if end == "" {
		missing, flags = append(missing, "end date"), append(flags, "--end")
	}
	if len(missing) > 0 {
		what, without := strings.Join(missing, " and "), "one"
		if len(missing) > 1 {
			what, without = "dates", "them"
		}
		return errs.Usage("SPRINT_HAS_NO_DATES",
			"sprint %s has no %s, and Jira will not run a sprint without %s",
			current.ID, what, without).
			WithDetail("%q", current.Name).
			WithRemedy("pass %s, or give the sprint its window when you create it",
				strings.Join(flags, " and "))
	}
	return ordered(start, end)
}

// firstNonEmpty is the flag if the caller gave one and the sprint's own value
// otherwise. A flag supplies a date the sprint does not have and replaces one
// it does; an absent flag leaves what is there, which is what makes the start
// request a partial update rather than a rewrite.
func firstNonEmpty(flag, current string) string {
	if flag != "" {
		return flag
	}
	return current
}

// startRemedyFor says what to do about a sprint in the wrong state, which
// differs: one is already running and the other is over for good.
func startRemedyFor(state string) string {
	if state == StateActive {
		return "it is already running; nothing was changed"
	}
	return "a closed sprint cannot be reopened by any API; create a new one"
}

// applied sends a write and reads the sprint back out of its own response.
//
// Both endpoints answer with the sprint as it now stands, which is worth more
// than an acknowledgement this tool assembled: it is the server's account of
// what the request did, including any date it filled in. A second GET would be
// another request whose answer could differ for reasons unrelated to the write.
//
// fallbackBoard is used when the response omits originBoardId. The board is not
// something the write changed, so reporting the one the sprint was addressed
// through is a fact rather than a guess.
func (c *Client) applied(
	ctx context.Context, req transport.Request, fallbackBoard string,
) (Sprint, error) {
	resp, err := c.Transport.Do(ctx, req)
	if err != nil {
		return Sprint{}, err
	}
	if err := transport.Err(resp); err != nil {
		return Sprint{}, err
	}

	var raw rawSprint
	if err := json.Unmarshal(resp.Body, &raw); err != nil || raw.ID.String() == "" {
		return Sprint{}, errs.Remote("MALFORMED_SPRINT",
			"the write succeeded and its response is not a usable sprint").
			WithDetail("%s %s", req.Method, req.Path).
			WithRemedy("`%s sprint list` reports the sprint as the server holds it",
				buildinfo.App).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	out, err := raw.convert()
	if err != nil {
		return Sprint{}, err
	}
	if out.Board == "" {
		out.Board = fallbackBoard
	}
	return out, nil
}

// writeNode renders a sprint a write verb just changed.
//
// It is Sprint.Node plus the action and minus the completion date, which is the
// shape WriteSchema declares. Built here rather than by adding an attribute to
// Node() so that a completed date arriving from some future server cannot reach
// a document whose schema has no room for it.
func writeNode(s Sprint, action string) *render.Node {
	return render.El("sprint").
		Attr("id", s.ID).
		Attr("state", s.State).
		AttrIf("board", s.Board).
		Attr("action", action).
		Leaf("name", s.Name).
		LeafIf("goal", s.Goal).
		LeafIf("start", s.Start).
		LeafIf("end", s.End)
}

// Window is the pair of dates a sprint runs between, as they will be sent.
// Either half may be empty, meaning the caller did not say.
type Window struct {
	Start string
	End   string
}

// addTo writes the window into a request body, omitting what was not given.
// An empty date is left out rather than sent as an empty string, because the
// API reads the two differently: absent means unchanged, and empty is a value.
func (w Window) addTo(fields map[string]any) {
	if w.Start != "" {
		fields["startDate"] = w.Start
	}
	if w.End != "" {
		fields["endDate"] = w.End
	}
}

// sprintWindow reads --start and --end.
//
// It is called from Validate and again from the command body, so a malformed
// date is refused before anything is sent and the body never has to trust its
// flags. Running it twice costs nothing and means neither path can be the one
// that forgot.
func sprintWindow(inv *registry.Invocation) (Window, error) {
	start, err := parseTimestamp("start", inv.Flags.String("start"))
	if err != nil {
		return Window{}, err
	}
	end, err := parseTimestamp("end", inv.Flags.String("end"))
	if err != nil {
		return Window{}, err
	}
	if err := ordered(start, end); err != nil {
		return Window{}, err
	}
	return Window{Start: start, End: end}, nil
}

// ordered refuses a window that runs backwards.
//
// Jira refuses one too — "startDate: The start date of a sprint must be before
// the end date" — so this is the same verdict without the round trip, rather
// than a rule this tool invented. A half-empty pair is not this function's
// question: on create it means the caller planned only one end of the window,
// and on start it has already been resolved against the sprint's own dates.
//
// Both values are RFC 3339 in UTC, either from parseTimestamp or from
// normalizeTime, so a parse failure here is not something a caller can cause
// and not something to report as their mistake.
func ordered(start, end string) error {
	if start == "" || end == "" {
		return nil
	}
	from, haveFrom := instant(start)
	until, haveUntil := instant(end)
	if !haveFrom || !haveUntil {
		return nil
	}
	if until.After(from) {
		return nil
	}
	return errs.Usage("INVALID_SPRINT_WINDOW",
		"a sprint has to end after it starts").
		WithDetail("%s to %s", start, end).
		WithRemedy("check which way round the two dates go")
}

// instant reads a value this package produced, and says so with a bool rather
// than an error. Both callers hold a string that came from parseTimestamp or
// from normalizeTime, so a failure here is not a caller's mistake and there is
// nothing to report to them — which is a different thing from swallowing an
// error, and reads as one if it is written that way.
func instant(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, value)
	return t, err == nil
}

// parseTimestamp reads one date flag and returns what will be sent.
//
// RFC 3339 only. A bare date has no time and no zone, and choosing one for the
// caller would decide when their iteration begins on their behalf — the exact
// guess §5.2 exists to refuse. Normalizing to UTC is not the same thing: an
// offset is a spelling of an instant, not a different instant, and it makes the
// value sent match the value the read path reports.
func parseTimestamp(flag, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", errs.Usage("INVALID_SPRINT_DATE",
			"--%s is not an RFC 3339 timestamp", flag).
			WithDetail("got %q", value).
			WithRemedy("give the whole instant, e.g. 2026-08-11T09:00:00Z; " +
				"a bare date names no time and no zone, and this tool will not pick one")
	}
	return t.UTC().Format(time.RFC3339), nil
}
