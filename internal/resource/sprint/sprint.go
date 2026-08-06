// Package sprint is the sprint resource.
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
//
// Sprints live on the agile API at site.Info.AgileBase, not on either platform
// REST version. Moving issues into a sprint spans two resources and lives in
// internal/workflow; everything here is about the sprint itself.
package sprint

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kinds and schema versions this resource emits.
const (
	KindList    = "sprint.list"
	VersionList = 1
	KindGet     = "sprint.get"
	VersionGet  = 1
)

func init() {
	registry.Register(listCommand())
	registry.Register(getCommand())

	render.RegisterSchema(KindList, Schema())
	render.RegisterSchema(KindGet, Schema())
}

// Schema is the shape of a sprint, as `jr contract` reports it and as
// render.Doc.Validate holds every emitted sprint to.
func Schema() *render.Schema {
	return &render.Schema{
		Element: "sprint",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeInt},
			{Name: "state", Type: render.TypeString, Enum: States},
			{Name: "board", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
			{Schema: render.Leaf("goal", render.TypeString), Optional: true},
			// Absent means the event has not happened: a future sprint has no
			// start, a running one has no completion. Declared as timestamps
			// so a consumer knows they are RFC 3339 in UTC without finding out
			// from a sample — and so the check fails here if a deployment's
			// own format ever reaches the output unnormalized.
			{Schema: render.Leaf("start", render.TypeTimestamp), Optional: true},
			{Schema: render.Leaf("end", render.TypeTimestamp), Optional: true},
			{Schema: render.Leaf("completed", render.TypeTimestamp), Optional: true},
		},
	}
}

// Doer is the part of the transport this resource needs.
type Doer interface {
	Do(ctx context.Context, r transport.Request) (*transport.Response, error)
}

// Client queries sprints.
type Client struct {
	Transport Doer
	Site      site.Info
}

// The states a sprint can be in. There is no fourth: a sprint is planned,
// running, or finished.
const (
	StateFuture = "future"
	StateActive = "active"
	StateClosed = "closed"
)

// States is what --state accepts.
var States = []string{StateFuture, StateActive, StateClosed}

// PageSize is how many sprints are asked for per request. The agile API clamps
// a page at 50, so asking for more would misreport how much of the set one
// request covered.
const PageSize = 50

// Sprint is one sprint, in the shape this tool reports.
type Sprint struct {
	ID   string
	Name string
	// State is future, active, or closed.
	State string
	// Start, End, and Completed are RFC 3339 in UTC, or empty.
	//
	// Empty means the event has not happened: a future sprint has no start, and
	// a sprint that is running has no completion. It is never a zero time, which
	// would sort and compare as 1 January year 1 rather than as absent.
	Start     string
	End       string
	Completed string
	// Goal is what the sprint was for, and is often unset.
	Goal string
	// Board is the id of the board the sprint was created on. A sprint can
	// appear on more than one board, so this is where it came from rather than
	// everywhere it shows up.
	Board string
}

type rawSprint struct {
	ID            json.Number `json:"id"`
	Name          string      `json:"name"`
	State         string      `json:"state"`
	StartDate     string      `json:"startDate"`
	EndDate       string      `json:"endDate"`
	CompleteDate  string      `json:"completeDate"`
	Goal          string      `json:"goal"`
	OriginBoardID json.Number `json:"originBoardId"`
}

func (r rawSprint) convert() (Sprint, error) {
	out := Sprint{
		ID: r.ID.String(), Name: r.Name,
		State: strings.ToLower(r.State), Goal: r.Goal,
		Board: r.OriginBoardID.String(),
	}
	for _, f := range []struct {
		field string
		raw   string
		into  *string
	}{
		{"startDate", r.StartDate, &out.Start},
		{"endDate", r.EndDate, &out.End},
		{"completeDate", r.CompleteDate, &out.Completed},
	} {
		normalized, err := normalizeTime(f.field, f.raw)
		if err != nil {
			return Sprint{}, err
		}
		*f.into = normalized
	}
	return out, nil
}

// jiraTimeLayouts are the timestamp formats the agile API serves.
//
// The first is what Data Center sends and the second is what Cloud sends, and
// neither is RFC 3339: Data Center writes the offset without a colon, which
// time.RFC3339 refuses. Parsing only one of them would leave every sprint date
// on the other deployment reported as an error or, worse, as empty.
//
// This is copied rather than shared with the issue resource because resources
// never import each other. Duplicating twenty bytes of layout string is the
// price of each resource compiling on its own.
var jiraTimeLayouts = []string{
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05.999-0700",
	"2006-01-02T15:04:05-0700",
	time.RFC3339Nano,
	time.RFC3339,
}

// normalizeTime converts a Jira timestamp to RFC 3339 in UTC.
//
// A timestamp that cannot be parsed is an error rather than a value passed
// through. Emitting whatever arrived would make the output format depend on the
// server, and a consumer that parsed the documented shape would break on a row
// rather than on a request — which is much harder to notice.
func normalizeTime(field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	for _, layout := range jiraTimeLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", errs.Remote("MALFORMED_TIMESTAMP",
		"Jira returned a %s this tool cannot parse", field).
		WithDetail("%q", value).
		WithRemedy("report this: the timestamp format changed")
}

// Node renders one sprint.
func (s Sprint) Node() *render.Node {
	return render.El("sprint").
		Attr("id", s.ID).
		Attr("state", s.State).
		AttrIf("board", s.Board).
		Leaf("name", s.Name).
		LeafIf("goal", s.Goal).
		LeafIf("start", s.Start).
		LeafIf("end", s.End).
		LeafIf("completed", s.Completed)
}

// ListColumns is the default TSV column set for `sprint list`.
func ListColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "name", Path: "name"},
		{Header: "state", Path: "@state"},
		{Header: "start", Path: "start"},
		{Header: "end", Path: "end"},
	}
}

func listCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"sprint", "list"},
		Summary: "List a board's sprints",
		Description: strings.TrimSpace(`
Returns the sprints on a board, ordered by id.

The board comes from --board, JIRA_BOARD, or the context, and there is no
default: a sprint listing is a question about one board, and every board has a
different answer.

Only a scrum board has sprints. A kanban board is refused by the server, and the
refusal says which board it was and how to check its type.

--state narrows to future, active, or closed, and repeats: --state active
--state future is the pair a planner wants. An unknown state is refused rather
than sent, because the server answers one with an empty page and no complaint.

A date is RFC 3339 in UTC, and is empty when the event has not happened — a
future sprint has no start. Empty is never rendered as a zero time, which would
compare as the year 1 rather than as absent.`),
		Example: strings.Join([]string{
			buildinfo.App + " sprint list --board 42",
			buildinfo.App + " sprint list --state active --state future",
			buildinfo.App + " sprint list --board 42 --limit all --format json",
		}, "\n"),
		Flags: []registry.Flag{{
			Name: "state", Type: registry.TypeEnum, Enum: States, Repeatable: true,
			Usage: "only sprints in this state; repeat for several",
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "sprints",
		Columns:        ListColumns(),
		Outputs:        []registry.Output{{Kind: KindList, Version: VersionList}},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: validateList,
		Stream:   runList,
	}
}

// validateList refuses everything it can before the stream is opened.
//
// A streaming command writes its header before its body runs, so a missing
// board or a misspelled state caught in the body would arrive after bytes were
// already on stdout. That includes resolving the board, which is why this
// reaches for the session rather than only reading flags.
func validateList(_ context.Context, inv *registry.Invocation) error {
	for _, state := range inv.Flags.StringSlice("state") {
		if !validState(state) {
			return errs.Usage("INVALID_SPRINT_STATE", "%q is not a sprint state", state).
				WithDetail("a sprint is %s", strings.Join(States, ", ")).
				WithRemedy("an unrecognized state returns no sprints rather than an error")
		}
	}
	if inv.Jira == nil {
		return errs.Runtime("NO_SESSION", "sprint list has no connection to Jira")
	}
	_, err := inv.Jira.RequireBoard()
	return err
}

func validState(s string) bool {
	for _, known := range States {
		if strings.EqualFold(s, known) {
			return true
		}
	}
	return false
}

// List reads every sprint on a board, optionally narrowed by state.
//
// The whole set is fetched and then ordered here rather than being truncated as
// it arrives, because the agile API documents no ordering for this endpoint. A
// caller asking for five sprints twice would otherwise get five sprints twice
// and no promise they were the same five.
func (c *Client) List(ctx context.Context, boardID string, states []string) ([]Sprint, error) {
	if err := ValidateBoardID(boardID); err != nil {
		return nil, err
	}
	path := c.Site.AgileBase() + "/board/" + url.PathEscape(boardID) + "/sprint"

	var out []Sprint
	startAt := 0
	for {
		query := url.Values{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(PageSize)},
		}
		if len(states) > 0 {
			// The agile API takes the set as one comma-separated value, which is
			// why repeated --state flags are joined rather than repeated here.
			lower := make([]string, 0, len(states))
			for _, s := range states {
				lower = append(lower, strings.ToLower(s))
			}
			query.Set("state", strings.Join(lower, ","))
		}

		resp, err := c.Transport.Do(ctx, transport.Request{
			Method: transport.MethodGet, Path: path, Query: query,
		})
		if err != nil {
			return nil, err
		}
		if err := c.listErr(resp, boardID); err != nil {
			return nil, err
		}

		var page struct {
			IsLast bool        `json:"isLast"`
			Values []rawSprint `json:"values"`
		}
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, errs.Remote("MALFORMED_SPRINTS",
				"%s did not return usable sprints", path).
				WithRequestID(resp.RequestID).Wrap(err)
		}

		for _, raw := range page.Values {
			converted, err := raw.convert()
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		// A page that added nothing ends the loop whatever the server claims, so
		// a server that never sets isLast cannot spin here.
		if len(page.Values) == 0 || page.IsLast {
			break
		}
		startAt += len(page.Values)
	}
	return sorted(out), nil
}

// listErr names the board in the one refusal this endpoint has that a caller
// can act on.
//
// A board that is not a scrum board has no sprints, and the server says so with
// a 400 whose generic remedy — "check the request" — sends the caller looking at
// their flags. The server's own message is kept as the detail, so this offers
// the likely cause without asserting it.
func (c *Client) listErr(resp *transport.Response, boardID string) error {
	err := transport.Err(resp)
	if err == nil || resp.Status != 400 {
		return err
	}
	out := errs.Usage("SPRINTS_REFUSED",
		"Jira refused a sprint listing for board %s", boardID).
		WithRemedy("only a scrum board has sprints; `%s board get %s` reports the type",
			buildinfo.App, boardID)
	if detail := errs.Coerce(err).Detail; detail != "" {
		out = out.WithDetail("%s", detail)
	}
	return out.WithRequestID(resp.RequestID)
}

// sorted orders by id numerically, so two runs against one board produce the
// same rows in the same order. A sprint id is a number, and ordering it as text
// would put 100 before 99 — the same failure issue keys have.
func sorted(sprints []Sprint) []Sprint {
	sort.SliceStable(sprints, func(i, j int) bool {
		a, aErr := strconv.Atoi(sprints[i].ID)
		b, bErr := strconv.Atoi(sprints[j].ID)
		if aErr != nil || bErr != nil {
			// An id that is not a number is not something to invent an order
			// for. Falling back to text keeps the sort total and stable rather
			// than leaving it undefined.
			return sprints[i].ID < sprints[j].ID
		}
		return a < b
	})
	return sprints
}

func runList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, err := clientFor(ctx, inv, "sprint list")
	if err != nil {
		return registry.StreamResult{}, err
	}
	boardID, err := inv.Jira.RequireBoard()
	if err != nil {
		return registry.StreamResult{}, err
	}

	sprints, err := client.List(ctx, boardID, inv.Flags.StringSlice("state"))
	if err != nil {
		return registry.StreamResult{}, err
	}

	complete := true
	if !inv.Limit.All && len(sprints) > inv.Limit.N {
		sprints = sprints[:inv.Limit.N]
		complete = false
	}
	for _, s := range sprints {
		if err := out.Write(s.Node()); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), out.Count())
	return registry.StreamResult{Complete: complete}, nil
}

func getCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"sprint", "get"},
		Summary: "Fetch one sprint",
		Description: strings.TrimSpace(`
Returns a single sprint by id.

A sprint is addressed by id and never by name, because a name is not unique: two
boards can both have a "Sprint 1", and resolving a name would pick one of them
without saying so. ` + "`" + buildinfo.App + ` sprint list` + "`" + ` reports the ids.

Unlike the listing, this needs no board — a sprint id addresses a sprint on its
own, whichever board it came from.`),
		Example: strings.Join([]string{
			buildinfo.App + " sprint get 128",
			buildinfo.App + " sprint get 128 --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "id", Usage: "sprint id, from `jr sprint list`", Required: true,
		}},
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindGet, Version: VersionGet}},
		ExitCodes: []exitcode.Code{
			exitcode.Auth, exitcode.NotFound, exitcode.Permission,
			exitcode.RateLimit, exitcode.Remote,
		},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			if len(inv.Args) == 0 {
				return errs.Usage("INVALID_SPRINT_ID", "a sprint id is required")
			}
			return ValidateID(inv.Args[0])
		},
		Run: runGet,
	}
}

// Get reads one sprint.
func (c *Client) Get(ctx context.Context, id string) (Sprint, error) {
	if err := ValidateID(id); err != nil {
		return Sprint{}, err
	}
	path := c.Site.AgileBase() + "/sprint/" + url.PathEscape(id)

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path,
	})
	if err != nil {
		return Sprint{}, err
	}
	if err := transport.Err(resp); err != nil {
		return Sprint{}, err
	}

	var raw rawSprint
	if err := json.Unmarshal(resp.Body, &raw); err != nil || raw.ID.String() == "" {
		return Sprint{}, errs.Remote("MALFORMED_SPRINT",
			"the response for sprint %s is not a usable sprint", id).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	return raw.convert()
}

func runGet(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "sprint get")
	if err != nil {
		return nil, err
	}
	got, err := client.Get(ctx, inv.Args[0])
	if err != nil {
		return nil, err
	}
	return render.Record(KindGet, VersionGet, got.Node()), nil
}

// ValidateID rejects a sprint id this tool cannot address.
//
// Sprint ids are numeric. Checking locally means a typo costs no round trip, and
// it keeps a caller's argument from reaching a URL path as anything but digits.
func ValidateID(id string) error { return validNumericID("sprint", id) }

// ValidateBoardID rejects a board id this tool cannot address. The sprint
// resource needs its own copy because resources never import each other.
func ValidateBoardID(id string) error { return validNumericID("board", id) }

func validNumericID(what, id string) error {
	code := "INVALID_" + strings.ToUpper(what) + "_ID"
	if id == "" {
		return errs.Usage(code, "a %s id is required", what)
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return errs.Usage(code, "%q is not a %s id", id, what).
				WithDetail("a %s id is digits, e.g. 42", what).
				WithRemedy("take it from `%s %s list`", buildinfo.App, what)
		}
	}
	return nil
}

// clientFor is the opening every command here shares.
func clientFor(
	ctx context.Context, inv *registry.Invocation, command string,
) (*Client, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "%s has no connection to Jira", command)
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{Transport: conn, Site: info}, nil
}

// ListDoc renders sprints as a document.
func ListDoc(sprints []Sprint, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(sprints))
	for _, s := range sprints {
		items = append(items, s.Node())
	}
	return render.List(KindList, VersionList, &render.Collection{
		Name: "sprints", Items: items, Complete: complete, Columns: ListColumns(),
	})
}
