// Package epic is the epic resource.
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
//
// Epics live on the agile API at site.Info.AgileBase. Moving issues into and
// out of an epic spans two resources and lives in internal/workflow; everything
// here is about the epic itself.
package epic

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"

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
	KindList    = "epic.list"
	VersionList = 1
	KindGet     = "epic.get"
	VersionGet  = 1
)

func init() {
	registry.Register(listCommand())
	registry.Register(getCommand())

	render.RegisterSchema(KindList, Schema())
	render.RegisterSchema(KindGet, Schema())
}

// Schema is the shape of an epic, as `jr contract` reports it and as
// render.Doc.Validate holds every emitted epic to.
func Schema() *render.Schema {
	return &render.Schema{
		Element: "epic",
		Attrs: []render.Field{
			{Name: "key", Type: render.TypeString},
			{Name: "id", Type: render.TypeInt},
			{Name: "done", Type: render.TypeBool},
		},
		Children: []render.Child{
			// name and summary are two fields and they differ. The board shows
			// the name; a JQL search shows the summary.
			{Schema: render.Leaf("name", render.TypeString)},
			{Schema: render.Leaf("summary", render.TypeString), Optional: true},
			{Schema: render.Leaf("color", render.TypeString), Optional: true},
		},
	}
}

// Doer is the part of the transport this resource needs.
type Doer interface {
	Do(ctx context.Context, r transport.Request) (*transport.Response, error)
}

// Client queries epics.
type Client struct {
	Transport Doer
	Site      site.Info
}

// PageSize is how many epics are asked for per request. The agile API clamps a
// page at 50, so asking for more would misreport how much of the set one
// request covered.
const PageSize = 50

// Epic is one epic, in the shape this tool reports.
type Epic struct {
	// ID is the numeric id, and Key is the issue key. An epic is an issue, so
	// it has both, and the agile API accepts either when addressing one.
	ID  string
	Key string
	// Name is the epic's own name — the "Epic Name" field — and Summary is the
	// issue summary. They are two fields and they frequently differ: the board
	// shows the name and a JQL search shows the summary. Reporting one as the
	// other would make the two views disagree with no way to tell why.
	Name    string
	Summary string
	// Done reports whether the epic is marked complete. It is the epic's own
	// flag, not a workflow status: an epic can be done with open issues in it.
	Done bool
	// Color is the swatch the board draws it in, e.g. color_4. It is reported
	// because it is the only thing tying a row here to a stripe on a board.
	Color string
}

type rawEpic struct {
	ID      json.Number `json:"id"`
	Key     string      `json:"key"`
	Name    string      `json:"name"`
	Summary string      `json:"summary"`
	Done    bool        `json:"done"`
	Color   *struct {
		Key string `json:"key"`
	} `json:"color"`
}

func (r rawEpic) convert() Epic {
	out := Epic{
		ID: r.ID.String(), Key: r.Key, Name: r.Name,
		Summary: r.Summary, Done: r.Done,
	}
	if r.Color != nil {
		out.Color = r.Color.Key
	}
	return out
}

// Node renders one epic.
func (e Epic) Node() *render.Node {
	return render.El("epic").
		Attr("key", e.Key).
		Attr("id", e.ID).
		Attr("done", strconv.FormatBool(e.Done)).
		Leaf("name", e.Name).
		LeafIf("summary", e.Summary).
		LeafIf("color", e.Color)
}

// ListColumns is the default TSV column set for `epic list`.
func ListColumns() []render.Column {
	return []render.Column{
		{Header: "key", Path: "@key"},
		{Header: "name", Path: "name"},
		{Header: "summary", Path: "summary"},
		{Header: "done", Path: "@done"},
	}
}

// The values --done accepts. It is an enum rather than a boolean flag because a
// boolean has two states and this filter has three: done, not done, and no
// filter at all. A bool defaulting to false would silently hide every completed
// epic from a caller who never passed the flag.
var doneValues = []string{"true", "false"}

func listCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"epic", "list"},
		Summary: "List a board's epics",
		Description: strings.TrimSpace(`
Returns the epics on a board, ordered by id.

The board comes from --board, JIRA_BOARD, or the context, and there is no
default: the epics on a board are the epics that board shows, and every board
has a different answer.

An epic has a name and a summary, and they are different fields. The board shows
the name; a JQL search shows the summary. Both are reported, because reporting
one as the other would make two views of the same epic disagree with nothing to
explain it.

--done narrows to complete or incomplete epics, and omitting it returns both. It
takes true or false rather than being a bare flag, because a bare --done would
default to false and silently hide every finished epic from a caller who never
passed it.

Ordered by id, which is the order the epics were created. Keys are deliberately
not sorted as text — ENG-999 is below ENG-1000 as an issue and above it as a
string — and id order needs no such parsing.`),
		Example: strings.Join([]string{
			buildinfo.App + " epic list --board 42",
			buildinfo.App + " epic list --done false",
			buildinfo.App + " epic list --board 42 --limit all --format json",
		}, "\n"),
		Flags: []registry.Flag{{
			Name: "done", Type: registry.TypeEnum, Enum: doneValues,
			Usage: "only epics that are done (true) or not (false); omit for both",
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "epics",
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
// board or a bad --done caught in the body would arrive after bytes were
// already on stdout. That includes resolving the board, which is why this
// reaches for the session rather than only reading flags.
func validateList(_ context.Context, inv *registry.Invocation) error {
	if done := inv.Flags.String("done"); done != "" &&
		!strings.EqualFold(done, "true") && !strings.EqualFold(done, "false") {
		return errs.Usage("INVALID_DONE", "--done takes true or false, not %q", done).
			WithRemedy("omit it to list both the finished epics and the unfinished")
	}
	if inv.Jira == nil {
		return errs.Runtime("NO_SESSION", "epic list has no connection to Jira")
	}
	_, err := inv.Jira.RequireBoard()
	return err
}

// List reads every epic on a board, optionally narrowed by whether it is done.
//
// The whole set is fetched and then ordered here rather than being truncated as
// it arrives, because the agile API documents no ordering for this endpoint. A
// caller asking for five epics twice would otherwise get five epics twice and
// no promise they were the same five.
func (c *Client) List(ctx context.Context, boardID, done string) ([]Epic, error) {
	if err := ValidateBoardID(boardID); err != nil {
		return nil, err
	}
	path := c.Site.AgileBase() + "/board/" + url.PathEscape(boardID) + "/epic"

	var out []Epic
	startAt := 0
	for {
		query := url.Values{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(PageSize)},
		}
		if done != "" {
			query.Set("done", strings.ToLower(done))
		}

		resp, err := c.Transport.Do(ctx, transport.Request{
			Method: transport.MethodGet, Path: path, Query: query,
		})
		if err != nil {
			return nil, err
		}
		if err := transport.Err(resp); err != nil {
			return nil, err
		}

		var page struct {
			IsLast bool      `json:"isLast"`
			Values []rawEpic `json:"values"`
		}
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, errs.Remote("MALFORMED_EPICS",
				"%s did not return usable epics", path).
				WithRequestID(resp.RequestID).Wrap(err)
		}

		for _, raw := range page.Values {
			out = append(out, raw.convert())
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

// sorted orders by id numerically, so two runs against one board produce the
// same rows in the same order.
//
// By id rather than by key on purpose. An epic key is an issue key, and issue
// keys do not sort as text — ENG-999 is below ENG-1000 as an issue and above it
// as a string. Ordering by key would need the parsing that lives in the issue
// resource, which this one may not import; ordering by id needs none of it and
// is creation order, which is at least a thing a reader can predict.
func sorted(epics []Epic) []Epic {
	sort.SliceStable(epics, func(i, j int) bool {
		a, aErr := strconv.Atoi(epics[i].ID)
		b, bErr := strconv.Atoi(epics[j].ID)
		if aErr != nil || bErr != nil {
			// An id that is not a number is not something to invent an order
			// for. Falling back to text keeps the sort total and stable rather
			// than leaving it undefined.
			return epics[i].ID < epics[j].ID
		}
		return a < b
	})
	return epics
}

func runList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, err := clientFor(ctx, inv, "epic list")
	if err != nil {
		return registry.StreamResult{}, err
	}
	boardID, err := inv.Jira.RequireBoard()
	if err != nil {
		return registry.StreamResult{}, err
	}

	epics, err := client.List(ctx, boardID, inv.Flags.String("done"))
	if err != nil {
		return registry.StreamResult{}, err
	}

	complete := true
	if !inv.Limit.All && len(epics) > inv.Limit.N {
		epics = epics[:inv.Limit.N]
		complete = false
	}
	for _, e := range epics {
		if err := out.Write(e.Node()); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), out.Count())
	return registry.StreamResult{Complete: complete}, nil
}

func getCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"epic", "get"},
		Summary: "Fetch one epic",
		Description: strings.TrimSpace(`
Returns a single epic by key or id.

An epic is an issue, so it has both, and either addresses it. A key is what a
person has; an id is what a listing reports.

Unlike the listing, this needs no board — an epic exists whether or not a board
shows it.`),
		Example: strings.Join([]string{
			buildinfo.App + " epic get ENG-42",
			buildinfo.App + " epic get 10101 --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "epic", Usage: "epic key or id, e.g. ENG-42", Required: true,
		}},
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindGet, Version: VersionGet}},
		ExitCodes: []exitcode.Code{
			exitcode.Auth, exitcode.NotFound, exitcode.Permission,
			exitcode.RateLimit, exitcode.Remote,
		},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			if len(inv.Args) == 0 {
				return errs.Usage("INVALID_EPIC", "an epic key or id is required")
			}
			return ValidateRef(inv.Args[0])
		},
		Run: runGet,
	}
}

// Get reads one epic, by key or id.
func (c *Client) Get(ctx context.Context, ref string) (Epic, error) {
	if err := ValidateRef(ref); err != nil {
		return Epic{}, err
	}
	path := c.Site.AgileBase() + "/epic/" + url.PathEscape(ref)

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path,
	})
	if err != nil {
		return Epic{}, err
	}
	if err := transport.Err(resp); err != nil {
		return Epic{}, err
	}

	var raw rawEpic
	if err := json.Unmarshal(resp.Body, &raw); err != nil || raw.Key == "" {
		return Epic{}, errs.Remote("MALFORMED_EPIC",
			"the response for epic %s is not a usable epic", ref).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	return raw.convert(), nil
}

func runGet(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "epic get")
	if err != nil {
		return nil, err
	}
	got, err := client.Get(ctx, inv.Args[0])
	if err != nil {
		return nil, err
	}
	return render.Record(KindGet, VersionGet, got.Node()), nil
}

// ValidateRef rejects a reference that is neither an issue key nor an id.
//
// This is a shape check, not a comparison: it keeps a caller's argument out of
// a URL path as anything else, and makes a typo cost no round trip. Ordering
// keys is a different problem, and this resource deliberately does not do it —
// see sorted.
//
// Both halves are checked against a character set. "../..-1" has a non-empty
// project and a numeric part, and is not a key — accepting it here would leave
// the safety of the request resting on whether the caller downstream remembered
// to escape it.
func ValidateRef(ref string) error {
	if ref == "" {
		return errs.Usage("INVALID_EPIC", "an epic key or id is required")
	}
	if digits(ref) {
		return nil
	}
	project, number, found := strings.Cut(ref, "-")
	if found && validProject(project) && digits(number) {
		return nil
	}
	return errs.Usage("INVALID_EPIC", "%q is not an epic key or id", ref).
		WithDetail("an epic is addressed as ENG-42 or as 10101").
		WithRemedy("take it from `%s epic list`", buildinfo.App)
}

// validProject reports whether s can be a Jira project key: a leading letter,
// then letters, digits, or underscores.
//
// This duplicates issue.ParseKey's rule, because resources never import each
// other — the same trade the timestamp layouts in the sprint resource make. The
// duplication is a charset, which is the kind of thing that does not drift.
func validProject(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r == '_' || (r >= '0' && r <= '9')):
		default:
			return false
		}
	}
	return true
}

// ValidateBoardID rejects a board id this tool cannot address. The epic
// resource needs its own copy because resources never import each other.
func ValidateBoardID(id string) error {
	if !digits(id) {
		return errs.Usage("INVALID_BOARD_ID", "%q is not a board id", id).
			WithDetail("a board id is digits, e.g. 42").
			WithRemedy("take it from `%s board list`", buildinfo.App)
	}
	return nil
}

// digits reports whether s is one or more decimal digits and nothing else.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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

// ListDoc renders epics as a document.
func ListDoc(epics []Epic, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(epics))
	for _, e := range epics {
		items = append(items, e.Node())
	}
	return render.List(KindList, VersionList, &render.Collection{
		Name: "epics", Items: items, Complete: complete, Columns: ListColumns(),
	})
}
