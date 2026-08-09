// Package user is the user resource.
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
package user

import (
	"context"
	"errors"
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
	KindList    = "user.list"
	VersionList = 1
	KindGet     = "user.get"
	VersionGet  = 1
)

func init() {
	registry.Register(listCommand())
	registry.Register(getCommand())
	registry.Register(meCommand())

	render.RegisterSchema(KindList, Schema())
	render.RegisterSchema(KindGet, Schema())
}

// Schema is the shape of a user, as `jr contract` reports it and as
// render.Doc.Validate holds every emitted user to.
func Schema() *render.Schema {
	return &render.Schema{
		Element: "user",
		Attrs: []render.Field{
			// An accountId on Cloud and a username on Data Center. The two are
			// not interchangeable, which is why the type is a string rather
			// than anything more specific.
			{Name: "id", Type: render.TypeString},
			{Name: "active", Type: render.TypeBool},
			{Name: "kind", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.Leaf("display", render.TypeString)},
			// Absent means not disclosed, which Cloud does by privacy setting.
			// It is not the same as having none, and the element is omitted
			// rather than emitted empty so a consumer can tell.
			{Schema: render.Leaf("email", render.TypeString), Optional: true},
		},
	}
}

// Doer is the part of the transport this resource needs.
type Doer interface {
	Do(ctx context.Context, r transport.Request) (*transport.Response, error)
}

// Client queries users.
type Client struct {
	Transport Doer
	Site      site.Info
}

// User is one account, in the shape this tool reports.
//
// It is site.User because `--assignee` has to resolve a name to the same value
// this command reports, and two definitions of what a user is would be two
// answers to that. The deployment split — accountId against username, `query`
// against `username` — lives in internal/site with it.
type User = site.User

// Node renders one user. It is a function rather than a method because User
// is site's type: how a user is reported is this resource's business, and
// what a user is is not.
func Node(u User) *render.Node {
	return render.El("user").
		Attr("id", u.ID).
		Attr("active", strconv.FormatBool(u.Active)).
		AttrIf("kind", u.Kind).
		Leaf("display", u.Display).
		LeafIf("email", u.Email)
}

// ListColumns is the default TSV column set for `user list`.
func ListColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "display", Path: "display"},
		{Header: "email", Path: "email"},
		{Header: "active", Path: "@active"},
	}
}

func listCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"user", "list"},
		Summary: "Search for users",
		Description: strings.TrimSpace(`
Searches for users matching a query, ordered by display name.

The id is what every other command wants when it asks for a user — an
accountId on Cloud, a username on Data Center. The two are not interchangeable,
and this is where you get the right one.

An email is often absent on Cloud, where disclosure is a privacy setting rather
than a missing value. An empty column means "not disclosed", which is not the
same as "has none", and nothing here infers one from the other.

A query is required. Listing every user on an instance is not a thing this tool
does: it is slow, it is rarely what was meant, and the endpoint that allows it
is not the same on both deployments.

This command does not page, so --limit all is bounded like any other limit and
is not a way to get the whole directory. Whatever the bound, a search with more
matches than it allows is never reported as complete: it says so in the output,
warns on stderr, and exits 3. Narrow the query, or raise --limit.`),
		Example: strings.Join([]string{
			buildinfo.App + " user list ada",
			buildinfo.App + " user list ada --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "query", Usage: "part of a name or email", Required: true,
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "users",
		Columns:        ListColumns(),
		Outputs:        []registry.Output{{Kind: KindList, Version: VersionList}},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			if len(inv.Args) == 0 || strings.TrimSpace(inv.Args[0]) == "" {
				return errs.Usage("EMPTY_QUERY", "a search query is required").
					WithRemedy("pass part of a name or email")
			}
			return nil
		},
		Stream: runList,
	}
}

// Search finds users matching a query, up to limit, and reports whether the
// directory held more.
func (c *Client) Search(ctx context.Context, query string, limit int) (site.UserPage, error) {
	return site.SearchUsers(ctx, c.Transport, c.Site, query, limit)
}

func runList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, err := clientFor(ctx, inv, "user list")
	if err != nil {
		return registry.StreamResult{}, err
	}

	// This command does not page, so --limit all is bounded like any other
	// limit — at the same default every command applies when none is given.
	// The bound is honest rather than hidden: a directory with more in it comes
	// back complete="false" and exit 3, exactly as a numeric --limit would.
	//
	// Bounding is the point. Listing an entire user directory is slow, rarely
	// what was meant, and not the same endpoint on both deployments — which is
	// why the search query is required. What was wrong before was not the bound
	// but the silence: 50 rows reported as the whole answer.
	limit := registry.DefaultLimit
	if !inv.Limit.All {
		limit = inv.Limit.N
	}
	page, err := client.Search(ctx, inv.Args[0], limit)
	if err != nil {
		return registry.StreamResult{}, err
	}

	for _, u := range page.Users {
		if err := out.Write(Node(u)); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), out.Count())
	// The search decides this, not a comparison here. The bound reaches the
	// server as maxResults, so a check against len(page.Users) would be testing
	// a number the request already enforced and could never fail.
	return registry.StreamResult{Complete: page.Complete}, nil
}

func getCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"user", "get"},
		Summary: "Fetch one user by id",
		Description: strings.TrimSpace(`
Returns a single user.

The id is an accountId on Cloud and a username on Data Center — the value
` + "`" + buildinfo.App + ` user list` + "`" + ` reports. The two are not interchangeable, and the query
parameter differs with them, so passing one deployment's id to the other is a
404 rather than a wrong answer.`),
		Example: strings.Join([]string{
			buildinfo.App + " user get ada",
			buildinfo.App + " user get 712020:8f3a --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "id", Usage: "accountId on Cloud, username on Data Center", Required: true,
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

// Get reads one user.
func (c *Client) Get(ctx context.Context, id string) (User, error) {
	u, err := site.FetchUser(ctx, c.Transport, c.Site, id)
	if err == nil {
		return u, nil
	}
	// The refusal names what a caller has to pass instead, which is the whole
	// difficulty: the two deployments identify a user differently and the
	// wrong one is an empty answer rather than an error.
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Code == "NO_SUCH_USER" {
		return User{}, e.WithRemedy("Cloud identifies a user by accountId and " +
			"Data Center by username; `" + buildinfo.App + " user list` reports the right one")
	}
	return User{}, err
}

func runGet(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "user get")
	if err != nil {
		return nil, err
	}
	got, err := client.Get(ctx, inv.Args[0])
	if err != nil {
		return nil, err
	}
	return render.Record(KindGet, VersionGet, Node(got)), nil
}

func meCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"user", "me"},
		Summary: "Show who this credential authenticates as",
		Description: strings.TrimSpace(`
Returns the account the configured credential belongs to.

It is the cheapest way to find your own id, which is the value every command
that takes a user wants — and the one thing a token cannot tell you by looking
at it.

It is also the check that proves a credential works: the deployment probe
answers anonymously on most instances, so it establishes that a site is really
Jira and nothing about whether the token is any good.`),
		Example:   buildinfo.App + " user me",
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindGet, Version: VersionGet}},
		ExitCodes: []exitcode.Code{
			// NotFound looks wrong on a command with nothing to look up, and it
			// is the likeliest failure there is: a site URL missing its context
			// path answers /myself with a web page, which is NO_SUCH_ENDPOINT
			// at exit 5. This command is what a new caller runs first, so it is
			// the one that reports that misconfiguration.
			exitcode.Auth, exitcode.NotFound, exitcode.Permission,
			exitcode.RateLimit, exitcode.Remote,
		},
		Run: runMe,
	}
}

func runMe(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "user me has no connection to Jira")
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}

	// site.Whoami already exists for login verification, and reusing it means
	// the identity this reports and the identity `auth login` checked cannot
	// come apart.
	account, err := site.Whoami(ctx, conn, info)
	if err != nil {
		return nil, err
	}
	return render.Record(KindGet, VersionGet, Node(User{
		ID: account.ID, Display: account.Display,
		Email: account.Email, Active: account.Active,
	})), nil
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

// ListDoc renders users as a document.
func ListDoc(users []User, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(users))
	for _, u := range users {
		items = append(items, Node(u))
	}
	return render.List(KindList, VersionList, &render.Collection{
		Name: "users", Items: items, Complete: complete, Columns: ListColumns(),
	})
}
