// Package board is the board resource.
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
//
// Boards are the first thing in this tool that does not live on the platform
// REST API. Jira Software serves them from site.Info.AgileBase, which is the
// same path on both deployments and is not either API version — building an
// agile path out of APIBase is a 404 that reads like a board that is not there.
package board

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
	KindList    = "board.list"
	VersionList = 1
	KindGet     = "board.get"
	VersionGet  = 1
)

func init() {
	registry.Register(listCommand())
	registry.Register(getCommand())

	// Both kinds are the same element: a listing is boards, and a get is one
	// board. Declaring the shape once is the point — two copies would be two
	// things to keep in step with Node.
	render.RegisterSchema(KindList, Schema())
	render.RegisterSchema(KindGet, Schema())
}

// Schema is the shape of a board, as `jr contract` reports it and as
// render.Doc.Validate holds every emitted board to.
func Schema() *render.Schema {
	return &render.Schema{
		Element: "board",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeInt},
			{Name: "type", Type: render.TypeString, Optional: true, Enum: boardTypes},
		},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
			// Absent for a board located on a person rather than a project,
			// which Data Center allows.
			{Schema: render.Leaf("project", render.TypeString), Optional: true},
			{Schema: render.Leaf("project-name", render.TypeString), Optional: true},
		},
	}
}

// Doer is the part of the transport this resource needs.
type Doer interface {
	Do(ctx context.Context, r transport.Request) (*transport.Response, error)
}

// Client queries boards.
type Client struct {
	Transport Doer
	Site      site.Info
}

// Board is one board, in the shape this tool reports.
type Board struct {
	// ID is numeric and is what every other agile command wants. It is carried
	// as a string because that is what a URL path and a TSV cell both need,
	// and it is parsed rather than trusted on the way in.
	ID   string
	Name string
	// Type is scrum, kanban, or simple. It is not cosmetic: a sprint listing
	// exists only for a scrum board, and asking a kanban board for one is a
	// 400 rather than an empty list.
	Type string
	// Project is the key of the project the board is located on. It is empty
	// for a board located on a user rather than a project, which Data Center
	// allows — an absent key means "not on a project", not "unknown".
	Project     string
	ProjectName string
}

// PageSize is how many boards are asked for per request. The agile API caps a
// board page at 50 and silently clamps anything larger, so asking for more
// would misreport how much of the set one request covered.
const PageSize = 50

// boardTypes are the values --type accepts, which are the ones the agile API
// documents. An unknown type is refused locally rather than sent, because the
// server answers an unrecognized one with an empty page and no complaint —
// indistinguishable from a site with no boards of that type.
var boardTypes = []string{"scrum", "kanban", "simple"}

type rawBoard struct {
	ID   json.Number `json:"id"`
	Name string      `json:"name"`
	Type string      `json:"type"`
	// Location is absent on a board the credential can see but not place, so
	// it is a pointer and its absence is reported as an empty project.
	Location *struct {
		ProjectKey  string `json:"projectKey"`
		ProjectName string `json:"projectName"`
	} `json:"location"`
}

func (r rawBoard) convert() Board {
	out := Board{ID: r.ID.String(), Name: r.Name, Type: r.Type}
	if r.Location != nil {
		out.Project = r.Location.ProjectKey
		out.ProjectName = r.Location.ProjectName
	}
	return out
}

// Node renders one board.
func (b Board) Node() *render.Node {
	return render.El("board").
		Attr("id", b.ID).
		AttrIf("type", b.Type).
		Leaf("name", b.Name).
		LeafIf("project", b.Project).
		LeafIf("project-name", b.ProjectName)
}

// ListColumns is the default TSV column set for `board list`.
func ListColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "name", Path: "name"},
		{Header: "type", Path: "@type"},
		{Header: "project", Path: "project"},
	}
}

func listCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"board", "list"},
		Summary: "List the boards this credential can see",
		Description: strings.TrimSpace(`
Returns the boards visible to the credential, ordered by id.

Scoped to the resolved project when the context sets one, exactly as issue list
is. --project all lifts the scope and returns every board on the site, which is
a different and much larger question.

The type matters more than it looks. A sprint listing exists only for a scrum
board, so ` + "`" + buildinfo.App + ` sprint list` + "`" + ` against a kanban board is refused by the
server rather than answered with nothing.

Ordered by id numerically rather than by whatever order the server returned,
which the agile API does not document — so two runs of a script agree.`),
		Example: strings.Join([]string{
			buildinfo.App + " board list",
			buildinfo.App + " board list --type scrum",
			buildinfo.App + " board list --project all --limit all",
		}, "\n"),
		Flags: []registry.Flag{
			{
				Name: "type", Type: registry.TypeEnum, Enum: boardTypes,
				Usage: "only boards of this type",
			},
			{
				Name: "name", Type: registry.TypeString,
				Usage: "only boards whose name contains this text",
			},
		},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "boards",
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

func validateList(_ context.Context, inv *registry.Invocation) error {
	// Checked here rather than in the command body because a streaming command
	// has already written its header by the time the body runs, and a rejection
	// after bytes are on stdout is a result document a consumer cannot parse.
	if t := inv.Flags.String("type"); t != "" && !validType(t) {
		return errs.Usage("INVALID_BOARD_TYPE", "%q is not a board type", t).
			WithDetail("the agile API has %s", strings.Join(boardTypes, ", ")).
			WithRemedy("an unrecognized type returns no boards rather than an error")
	}
	return nil
}

func validType(t string) bool {
	for _, known := range boardTypes {
		if strings.EqualFold(t, known) {
			return true
		}
	}
	return false
}

// ListOptions bounds a board listing.
type ListOptions struct {
	// Project scopes the listing to one project by key or id. Empty means every
	// board the credential can see.
	Project string
	// Type is scrum, kanban, or simple.
	Type string
	// Name matches boards whose name contains it, case-insensitively. The
	// server does the matching, because it is the one that knows the collation.
	Name string
}

// List reads every board matching opt.
//
// The whole set is fetched and then ordered here rather than being truncated as
// it arrives, because the agile API documents no ordering for this endpoint. A
// caller asking for five boards twice would otherwise get five boards twice and
// no promise they were the same five.
func (c *Client) List(ctx context.Context, opt ListOptions) ([]Board, error) {
	path := c.Site.AgileBase() + "/board"

	var out []Board
	startAt := 0
	for {
		query := url.Values{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(PageSize)},
		}
		if opt.Project != "" {
			query.Set("projectKeyOrId", opt.Project)
		}
		if opt.Type != "" {
			query.Set("type", strings.ToLower(opt.Type))
		}
		if opt.Name != "" {
			query.Set("name", opt.Name)
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
			IsLast     bool       `json:"isLast"`
			MaxResults int        `json:"maxResults"`
			Total      int        `json:"total"`
			Values     []rawBoard `json:"values"`
		}
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, errs.Remote("MALFORMED_BOARDS",
				"%s did not return usable boards", path).
				WithRequestID(resp.RequestID).Wrap(err)
		}

		for _, raw := range page.Values {
			out = append(out, raw.convert())
		}
		// A page that added nothing ends the loop whatever the server claims,
		// so a server that never sets isLast cannot spin here. The agile API
		// omits total on some responses, so it is not part of the condition.
		if len(page.Values) == 0 || page.IsLast {
			break
		}
		startAt += len(page.Values)
	}
	return sorted(out), nil
}

// sorted orders by id numerically, so two runs against one site produce the
// same rows in the same order. A board id is a number, and ordering it as text
// would put 100 before 99 — the same failure issue keys have.
func sorted(boards []Board) []Board {
	sort.SliceStable(boards, func(i, j int) bool {
		a, aErr := strconv.Atoi(boards[i].ID)
		b, bErr := strconv.Atoi(boards[j].ID)
		if aErr != nil || bErr != nil {
			// An id that is not a number is not something to invent an order
			// for. Falling back to text keeps the sort total and stable rather
			// than leaving it undefined.
			return boards[i].ID < boards[j].ID
		}
		return a < b
	})
	return boards
}

func runList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, err := clientFor(ctx, inv, "board list")
	if err != nil {
		return registry.StreamResult{}, err
	}

	boards, err := client.List(ctx, ListOptions{
		// Project comes from the resolved context rather than a local flag:
		// --project is global, and it is a default, never a requirement.
		Project: listProject(inv),
		Type:    inv.Flags.String("type"),
		Name:    inv.Flags.String("name"),
	})
	if err != nil {
		return registry.StreamResult{}, err
	}

	complete := true
	if !inv.Limit.All && len(boards) > inv.Limit.N {
		boards = boards[:inv.Limit.N]
		complete = false
	}
	for _, b := range boards {
		if err := out.Write(b.Node()); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), out.Count())
	return registry.StreamResult{Complete: complete}, nil
}

// listProject resolves the scope of a board listing.
//
// The word "all" is how a caller says "ignore the context", which is otherwise
// impossible: an empty --project falls back to the context rather than clearing
// it, and a context with a project set would silently hide every other board.
func listProject(inv *registry.Invocation) string {
	if inv.Jira == nil {
		return ""
	}
	project := inv.Jira.Project()
	if strings.EqualFold(project, "all") {
		return ""
	}
	return project
}

func getCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"board", "get"},
		Summary: "Fetch one board",
		Description: strings.TrimSpace(`
Returns a single board by id.

The id defaults to the context's board, so a configured caller can ask without
repeating themselves. It is the same board every other agile command defaults
to.

Boards are addressed by id and never by name: two projects can have boards with
the same name, and resolving a name would pick one of them without saying so.`),
		Example: strings.Join([]string{
			buildinfo.App + " board get 42",
			buildinfo.App + " board get --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "id", Usage: "board id; defaults to the context's board",
		}},
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindGet, Version: VersionGet}},
		ExitCodes: []exitcode.Code{
			exitcode.Auth, exitcode.NotFound, exitcode.Permission,
			exitcode.RateLimit, exitcode.Remote,
		},
		Run: runGet,
	}
}

// Get reads one board.
func (c *Client) Get(ctx context.Context, id string) (Board, error) {
	if err := ValidateID(id); err != nil {
		return Board{}, err
	}
	path := c.Site.AgileBase() + "/board/" + url.PathEscape(id)

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path,
	})
	if err != nil {
		return Board{}, err
	}
	if err := transport.Err(resp); err != nil {
		return Board{}, err
	}

	var raw rawBoard
	if err := json.Unmarshal(resp.Body, &raw); err != nil || raw.ID.String() == "" {
		return Board{}, errs.Remote("MALFORMED_BOARD",
			"the response for board %s is not a usable board", id).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	return raw.convert(), nil
}

// ValidateID rejects a board id this tool cannot address.
//
// Board ids are numeric. Checking locally means a typo costs no round trip, and
// it keeps a caller's argument from reaching a URL path as anything but digits.
func ValidateID(id string) error {
	if id == "" {
		return errs.Usage("INVALID_BOARD_ID", "a board id is required")
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return errs.Usage("INVALID_BOARD_ID", "%q is not a board id", id).
				WithDetail("a board id is digits, e.g. 42").
				WithRemedy("take it from `%s board list`", buildinfo.App)
		}
	}
	return nil
}

func runGet(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "board get")
	if err != nil {
		return nil, err
	}
	id, err := boardArg(inv)
	if err != nil {
		return nil, err
	}

	got, err := client.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return render.Record(KindGet, VersionGet, got.Node()), nil
}

// boardArg resolves the board to act on: the argument, or the context's.
func boardArg(inv *registry.Invocation) (string, error) {
	if len(inv.Args) > 0 && inv.Args[0] != "" {
		return inv.Args[0], nil
	}
	if inv.Jira == nil {
		return "", errs.Usage("NO_BOARD", "a board id is required")
	}
	return inv.Jira.RequireBoard()
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

// ListDoc renders boards as a document.
func ListDoc(boards []Board, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(boards))
	for _, b := range boards {
		items = append(items, b.Node())
	}
	return render.List(KindList, VersionList, &render.Collection{
		Name: "boards", Items: items, Complete: complete, Columns: ListColumns(),
	})
}
