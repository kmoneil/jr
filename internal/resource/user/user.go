// Package user is the user resource.
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
package user

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
type User struct {
	// ID is an accountId on Cloud and a username on Data Center. The two are
	// not interchangeable, and this is the value every other command wants
	// when it asks for a user.
	ID      string
	Display string
	// Email is often absent on Cloud, where it is a privacy setting rather than
	// a missing value. An empty string here means "not disclosed", which is not
	// the same as "has none" — nothing infers one from the other.
	Email  string
	Active bool
	// Kind distinguishes a person from an application or a customer account,
	// where the server says. Assigning an issue to an app account is a mistake
	// worth being able to see before making.
	Kind string
}

type rawUser struct {
	AccountID   string `json:"accountId"`
	AccountType string `json:"accountType"`
	Name        string `json:"name"`
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
	Email       string `json:"emailAddress"`
	Active      bool   `json:"active"`
}

func (r rawUser) convert() User {
	id := r.AccountID
	if id == "" {
		id = r.Name
	}
	if id == "" {
		id = r.Key
	}
	return User{
		ID: id, Display: r.DisplayName, Email: r.Email,
		Active: r.Active, Kind: r.AccountType,
	}
}

// Node renders one user.
func (u User) Node() *render.Node {
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
is not the same on both deployments.`),
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

// Search finds users matching a query.
//
// The parameter name differs by deployment: Cloud takes `query` and matches
// display name and email, Data Center takes `username` and matches rather more
// loosely. Sending the wrong one is not an error — it is an empty result, which
// is why the split is here rather than left to a caller.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]User, error) {
	param := "username"
	if c.Site.Kind == site.Cloud {
		param = "query"
	}
	path := c.Site.APIBase() + "/user/search"

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   path,
		Query: url.Values{
			param:        {query},
			"maxResults": {strconv.Itoa(limit)},
		},
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw []rawUser
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, errs.Remote("MALFORMED_USERS",
			"%s did not return usable users", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}

	out := make([]User, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.convert())
	}
	// Ordered so two runs agree; neither endpoint promises one.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Display != out[j].Display {
			return out[i].Display < out[j].Display
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func runList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, err := clientFor(ctx, inv, "user list")
	if err != nil {
		return registry.StreamResult{}, err
	}

	// The server bound and the caller's bound are the same thing here: there is
	// nothing to page through, so asking for more than will be printed would
	// spend somebody's rate limit on rows nobody sees.
	limit := 50
	if !inv.Limit.All {
		limit = inv.Limit.N
	}
	users, err := client.Search(ctx, inv.Args[0], limit)
	if err != nil {
		return registry.StreamResult{}, err
	}

	complete := true
	if !inv.Limit.All && len(users) > inv.Limit.N {
		users = users[:inv.Limit.N]
		complete = false
	}
	for _, u := range users {
		if err := out.Write(u.Node()); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), out.Count())
	return registry.StreamResult{Complete: complete}, nil
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
	param := "username"
	if c.Site.Kind == site.Cloud {
		param = "accountId"
	}
	path := c.Site.APIBase() + "/user"

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   path,
		Query:  url.Values{param: {id}},
	})
	if err != nil {
		return User{}, err
	}
	if err := transport.Err(resp); err != nil {
		return User{}, err
	}

	var raw rawUser
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return User{}, errs.Remote("MALFORMED_USER",
			"%s did not return a usable user", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	converted := raw.convert()
	if converted.ID == "" {
		return User{}, errs.NotFound("NO_SUCH_USER", "no user %q on this site", id).
			WithRequestID(resp.RequestID).
			WithRemedy("Cloud identifies a user by accountId and Data Center by " +
				"username; `" + buildinfo.App + " user list` reports the right one")
	}
	return converted, nil
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
	return render.Record(KindGet, VersionGet, got.Node()), nil
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
			exitcode.Auth, exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
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
	return render.Record(KindGet, VersionGet, User{
		ID: account.ID, Display: account.Display,
		Email: account.Email, Active: account.Active,
	}.Node()), nil
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
		items = append(items, u.Node())
	}
	return render.List(KindList, VersionList, &render.Collection{
		Name: "users", Items: items, Complete: complete, Columns: ListColumns(),
	})
}
