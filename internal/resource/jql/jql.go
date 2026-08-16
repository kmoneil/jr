// Package jql is the jql resource: the commands that check a query without
// running it.
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
//
// It shares a name with internal/jql, the library it is the command surface
// over, and imports it unaliased. That is legal and unambiguous: a package's
// own name is not an identifier inside it, so `jql.Validate` here can only mean
// the library. Renaming either one to avoid a collision that does not exist
// would cost more clarity than it bought.
package jql

import (
	"context"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// Kinds and schema versions this resource emits.
const (
	KindValidate    = "jql.validate"
	VersionValidate = 1
	KindExplain     = "jql.explain"
	VersionExplain  = 1
)

func init() {
	registry.Register(validateCommand())
	registry.Register(explainCommand())

	render.RegisterSchema(KindValidate, ValidateSchema())
	render.RegisterSchema(KindExplain, ExplainSchema())
}

// ValidateSchema is the shape of a verdict.
func ValidateSchema() *render.Schema {
	return &render.Schema{
		Element: "query",
		Attrs: []render.Field{
			{Name: "valid", Type: render.TypeBool},
			{
				Name: "method", Type: render.TypeString,
				Enum: []string{MethodParse, MethodSearch, MethodLocal},
			},
		},
		Children: []render.Child{
			{Schema: render.Leaf("jql", render.TypeString)},
			// Jira reports more than one problem with a query, so these repeat.
			// A consumer that read only the first would fix one error at a time.
			{Schema: render.Leaf("error", render.TypeString), Optional: true, Repeated: true},
			{Schema: render.Leaf("warning", render.TypeString), Optional: true, Repeated: true},
		},
	}
}

// ExplainSchema is the shape of an explanation.
func ExplainSchema() *render.Schema {
	return &render.Schema{
		Element: "explanation",
		Attrs: []render.Field{
			{Name: "parenthesized", Type: render.TypeBool},
		},
		Children: []render.Child{
			{Schema: render.Leaf("fragment", render.TypeString)},
			{Schema: render.Leaf("query", render.TypeString)},
			{Schema: render.Leaf("project", render.TypeString), Optional: true},
			{Schema: render.ListSchema("fields", "field",
				render.Leaf("field", render.TypeString))},
		},
	}
}

// Doer is the part of the transport this resource needs.
type Doer interface {
	Do(ctx context.Context, r transport.Request) (*transport.Response, error)
}

// Client asks Jira whether a query parses.
type Client struct {
	Transport Doer
	Site      site.Info
}

// jqlFlag is shared by both commands, which take the same input and answer two
// different questions about it.
func jqlFlag() registry.Flag {
	return registry.Flag{
		Name: "jql", Type: registry.TypeString, Required: true,
		Usage: "the query to check",
	}
}

func validateCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"jql", "validate"},
		Summary: "Ask Jira whether a query parses, without running it",
		Description: strings.TrimSpace(`
Sends a query to Jira to be parsed and reports what it says, without returning
any issues.

The point is that the answer comes from the server. This tool has its own
checks — the query lexes, the parentheses balance — and they run first, because
a query that fails them costs no round trip. But they are not a parser, and a
query they accept can still be wrong: a field that does not exist on this site,
a function this deployment does not have, a value of the wrong type. Only Jira
knows.

The error is reported as Jira wrote it, including the position it names.
Rewriting it into this tool's own words would lose the one thing worth having.

An invalid query is a result, not a failure. This command exits 0 and reports
valid="false" with the reasons, because the reasons are the product: an agent
checking a query before it acts needs to know what is wrong with it, and an
exit code cannot carry a list. Branch on the attribute, not on the status.

A valid query can still carry warnings, and they are why the attribute is not
the whole answer. Jira warns when it will run the query and something in it
names nothing on this site, most often a value that does not exist for the
field: a user who has left, or was never here, or was typed wrong. The clause
is legal and matches nothing, so valid="true" with a warning and valid="true"
without one mean different things and lead to different next steps.

The two deployments answer this differently. Cloud has an endpoint for exactly
this question. Data Center does not, so the query is sent as a search bounded to
zero results — which parses and permission-checks it without fetching anything.
The output says which was used, because "valid" from a parse and "valid" from a
zero-row search are not quite the same claim, and "local" means this tool
answered without asking.`),
		Example: strings.Join([]string{
			buildinfo.App + ` jql validate --jql 'project = ENG AND status = Open'`,
			buildinfo.App + ` jql validate --jql 'assignee = currentUser()' --format json`,
		}, "\n"),
		Flags:     []registry.Flag{jqlFlag()},
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindValidate, Version: VersionValidate}},
		ExitCodes: []exitcode.Code{
			exitcode.Auth, exitcode.NotFound, exitcode.Permission,
			exitcode.RateLimit, exitcode.Remote,
		},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			// Only that something was passed. Whether it parses is the
			// question the command exists to answer, and answering it here
			// would refuse the input before reporting on it.
			if strings.TrimSpace(inv.Flags.String("jql")) == "" {
				return errs.Usage("EMPTY_JQL", "--jql is required and cannot be blank")
			}
			return nil
		},
		Run: runValidate,
	}
}

// Result is what a validation found.
type Result struct {
	Query string
	Valid bool
	// Errors are Jira's own messages, unedited. They carry the position, which
	// is the part worth having and the part a rewrite would lose.
	Errors []string
	// Warnings are what Jira flags without refusing, e.g. a field that exists
	// but is not visible in every project the query touches.
	Warnings []string
	// Method records how the answer was obtained: the parse endpoint, or a
	// search bounded to zero rows. They are not quite the same claim.
	Method string
}

// The three ways this question gets answered.
const (
	// Both come from internal/site, which is what reaches the server and
	// therefore what decides which of them is true of an answer.
	MethodParse  = site.JQLByParse
	MethodSearch = site.JQLBySearch
	// MethodLocal means this tool decided, without asking. It happens when the
	// query does not lex or its parentheses do not balance — errors worth
	// catching before spending a round trip. The verdict is reported the same
	// way whoever reached it, so a consumer parses one shape.
	MethodLocal = "local"
)

// Node renders a validation result.
func (r Result) Node() *render.Node {
	n := render.El("query").
		Attr("valid", strconv.FormatBool(r.Valid)).
		Attr("method", r.Method).
		Leaf("jql", r.Query)
	for _, e := range r.Errors {
		n.Child(render.El("error").SetText(e))
	}
	for _, w := range r.Warnings {
		n.Child(render.El("warning").SetText(w))
	}
	return n
}

// Check asks Jira whether the query parses.
//
// The asking lives in internal/site, because `issue list` needs the same answer
// before it runs and a resource may not import another resource. What stays
// here is the shape this command publishes: a Result carries the same fields
// under the names the kind has always used, so no schema moved when the
// implementation did.
//
// A malformed query is a result, not an error: the command was asked whether
// the query is valid and it found out. Only a failure to get an answer at all,
// no credential, no network, a 500, fails the command.
func (c *Client) Check(ctx context.Context, query string) (Result, error) {
	got, err := site.CheckJQL(ctx, c.Transport, c.Site, query)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Query: got.Query, Valid: got.Valid,
		Errors: got.Errors, Warnings: got.Warnings,
		Method: got.Method,
	}, nil
}

func runValidate(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	query := inv.Flags.String("jql")

	// The local checks first, so a query that cannot be right costs no round
	// trip. They are not a parser and they never claim to be — what they catch
	// is an unterminated string or an unbalanced parenthesis, which is most of
	// what a hand-written query gets wrong.
	//
	// A failure here is reported, not raised. Refusing would mean the same bad
	// query produced a document when Jira caught it and an error when this did,
	// and which one a caller got would depend on how the query was wrong.
	if err := jql.Validate(query); err != nil {
		local := errs.Coerce(err)
		return validateDoc(Result{
			Query: query, Valid: false, Method: MethodLocal,
			Errors: []string{local.Message},
		}), nil
	}

	client, err := clientFor(ctx, inv, "jql validate")
	if err != nil {
		return nil, err
	}
	result, err := client.Check(ctx, query)
	if err != nil {
		return nil, err
	}
	return validateDoc(result), nil
}

// validateDoc renders a verdict. It is one shape whoever reached it.
func validateDoc(r Result) *render.Doc {
	return render.Record(KindValidate, VersionValidate, r.Node())
}

func explainCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"jql", "explain"},
		Summary: "Show the query this tool would send",
		Description: strings.TrimSpace(`
Renders a query the way it would go out, and makes no request at all.

A raw fragment is never concatenated. It is parenthesized, and the parentheses
are the point: without them

    --jql 'status = Open OR status = Closed' --project ENG

means "in ENG and open, or closed anywhere" — an OR that escapes the project
scope and returns issues from projects the caller never mentioned. This shows
which one you are getting.

Every query carries an ORDER BY, and this shows that too. The default is the
issue key descending, because pagination has to be stable and the key is the
only field that is both unique and immutable. A --sort keeps the key as a
tiebreaker, and --order sets the direction either way — on its own it turns the
default key ordering around rather than being ignored.

The project comes from the resolved context, exactly as it does on issue list,
so this explains the query that command would actually build.

It also lists the fields the query references, tokenized rather than
pattern-matched: the text inside ` + "`summary ~ \"project = FOO\"`" + ` is a value,
not a project scope, and a regular expression cannot tell the difference.`),
		Example: strings.Join([]string{
			buildinfo.App + ` jql explain --jql 'status = Open OR status = Closed'`,
			buildinfo.App + ` jql explain --jql 'labels = retry' --sort updated --order desc`,
		}, "\n"),
		Flags: []registry.Flag{
			jqlFlag(),
			{
				Name: "sort", Type: registry.TypeString,
				Usage: "field to order by; the issue key stays as a tiebreaker",
			},
			{
				Name: "order", Type: registry.TypeEnum, Enum: []string{"asc", "desc"},
				Usage: "direction for --sort, or for the issue key ordering " +
					"when there is no --sort",
			},
		},
		// The context's project is part of the answer, so this needs the
		// resolved settings. It never connects: nothing here asks Jira
		// anything.
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindExplain, Version: VersionExplain}},
		// Asking Jira nothing is not the same as being unable to fail. The
		// settled context has to be resolved to answer at all, and `--context`
		// naming one that does not exist is UNKNOWN_CONTEXT at exit 5 before a
		// byte goes anywhere. The four exits a request can produce are the ones
		// this command genuinely cannot.
		ExitCodes: []exitcode.Code{exitcode.NotFound},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			return jql.ValidateFragment(inv.Flags.String("jql"))
		},
		Run: runExplain,
	}
}

// Explanation is what `jql explain` reports.
type Explanation struct {
	// Fragment is the caller's raw --jql, as they wrote it.
	Fragment string
	// Query is what would be sent.
	Query string
	// Project is the scope the fragment was combined with, or empty.
	Project string
	// Fields are the fields the fragment references, tokenized.
	Fields []string
	// Parenthesized reports whether the fragment was wrapped. It always is —
	// this is stated rather than assumed, because the whole reason the command
	// exists is that the consequence of not wrapping is invisible.
	Parenthesized bool
}

// Node renders an explanation.
func (e Explanation) Node() *render.Node {
	n := render.El("explanation").
		Attr("parenthesized", strconv.FormatBool(e.Parenthesized)).
		Leaf("fragment", e.Fragment).
		Leaf("query", e.Query).
		LeafIf("project", e.Project)

	fields := make([]*render.Node, 0, len(e.Fields))
	for _, f := range e.Fields {
		fields = append(fields, render.El("field").SetText(f))
	}
	return n.Child(render.ListEl("fields", "field", fields...))
}

// Explain builds the query without sending it.
//
// It goes through the same builder and the same ordering policy the issue
// commands use, so what it reports is what would be sent rather than a second
// account of it.
func Explain(fragment, project, sort, order string) (Explanation, error) {
	if err := jql.ValidateFragment(fragment); err != nil {
		return Explanation{}, err
	}

	b := jql.New()
	if project != "" {
		b.Project(project)
	}
	b.Raw(fragment)
	if err := jql.AppendOrder(b, sort, order); err != nil {
		return Explanation{}, err
	}

	query, err := b.Render()
	if err != nil {
		return Explanation{}, err
	}
	fields, err := jql.Fields(fragment)
	if err != nil {
		return Explanation{}, err
	}

	return Explanation{
		Fragment: fragment, Query: query, Project: project,
		Fields: fields, Parenthesized: true,
	}, nil
}

func runExplain(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	var project string
	if inv.Jira != nil {
		// A default, never a requirement: --project is global, and a query with
		// no project scope is a legitimate thing to explain.
		project = inv.Jira.Project()
	}

	explained, err := Explain(inv.Flags.String("jql"), project,
		inv.Flags.String("sort"), inv.Flags.String("order"))
	if err != nil {
		return nil, err
	}
	return render.Record(KindExplain, VersionExplain, explained.Node()), nil
}

// clientFor is the opening the validating command needs.
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
