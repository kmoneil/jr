package issue

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/transport"
)

// Kinds the comment commands emit.
const (
	KindCommentList    = "issue.comment.list"
	VersionCommentList = 2
)

func init() {
	registry.Register(commentListCommand())

	render.RegisterSchema(KindCommentList, CommentSchema())
}

// CommentSchema is the shape of one comment.
func CommentSchema() *render.Schema {
	return &render.Schema{
		Element: "comment",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeString},
			// Set only when the comment is restricted to a role or group,
			// which is exactly when a reader needs to know.
			{Name: "visibility", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			{Schema: authorSchema()},
			{Schema: render.Leaf("created", render.TypeTimestamp)},
			{Schema: render.Leaf("updated", render.TypeTimestamp)},
			{Schema: bodySchema("body"), Optional: true},
		},
	}
}

// authorSchema is the shape shared by a comment's author and a worklog's.
func authorSchema() *render.Schema {
	return &render.Schema{
		Element: "author",
		Attrs: []render.Field{
			// Absent where the deployment does not disclose one; the display
			// name is always there.
			{Name: "id", Type: render.TypeString, Optional: true},
			{Name: "display", Type: render.TypeString},
		},
	}
}

// bodySchema is the shape of a block of Jira markup, named rather than guessed
// at and carried as mixed content so newlines and fenced code survive.
func bodySchema(element string) *render.Schema {
	return &render.Schema{
		Element: element,
		Attrs: []render.Field{{
			Name: "format", Type: render.TypeString,
			Enum: []string{BodyWiki, BodyADF, BodyMarkdown},
		}},
		Text: &render.Field{Type: render.TypeString},
	}
}

// rawBodyFlag is declared by every read that can carry a Cloud body.
//
// It is one flag rather than one per command because the question is the same
// everywhere: a description, a comment, and a worklog note are all documents,
// and a caller who wants one unconverted wants all of them unconverted.
func rawBodyFlag() registry.Flag {
	return registry.Flag{
		Name: "raw-body", Type: registry.TypeBool,
		Usage: "emit a Cloud body as the Atlassian Document Format document " +
			"Jira sent it as, rather than converting it to markdown",
	}
}

// bodyMode reads --raw-body. A command that does not declare it gets the
// converting mode, which is the flag's default anyway.
func bodyMode(inv *registry.Invocation) BodyMode {
	if inv.Flags.Bool("raw-body") {
		return ModeRaw
	}
	return ModeMarkdown
}

// Comment is one comment, in the shape this tool reports.
type Comment struct {
	ID     string
	Author User
	// Body is the comment's text and BodyFormat says what markup it is in:
	// wiki on Data Center, markdown converted from Cloud's document, or the
	// document itself when the caller asked for it with --raw-body.
	Body       string
	BodyFormat string
	Created    string
	Updated    string
	// Visibility names the role or group a restricted comment is limited to,
	// and is empty for one everybody can see. It matters more than it looks:
	// a caller reading a comment they think is public may be reading one that
	// is not.
	Visibility string
}

// rawComment is the JSON both deployments return.
type rawComment struct {
	ID         string          `json:"id"`
	Author     *rawUser        `json:"author"`
	Body       json.RawMessage `json:"body"`
	Created    string          `json:"created"`
	Updated    string          `json:"updated"`
	Visibility *struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"visibility"`
}

func (r rawComment) convert(mode BodyMode) (Comment, error) {
	created, err := normalizeTime("created", r.Created)
	if err != nil {
		return Comment{}, err
	}
	updated, err := normalizeTime("updated", r.Updated)
	if err != nil {
		return Comment{}, err
	}

	body, format, err := decodeDescription(r.Body, mode)
	if err != nil {
		return Comment{}, err
	}
	out := Comment{
		ID: r.ID, Author: r.Author.convert(),
		Body: body, BodyFormat: format,
		Created: created, Updated: updated,
	}
	if r.Visibility != nil && r.Visibility.Value != "" {
		out.Visibility = r.Visibility.Type + ":" + r.Visibility.Value
	}
	return out, nil
}

// Node renders one comment.
func (c Comment) Node() *render.Node {
	n := render.El("comment").
		Attr("id", c.ID).
		AttrIf("visibility", c.Visibility)

	n.Child(render.El("author").
		AttrIf("id", c.Author.ID).
		Attr("display", c.Author.Display))
	n.Leaf("created", c.Created)
	n.Leaf("updated", c.Updated)

	if c.Body != "" {
		// The same treatment a description gets: mixed content in a child
		// element, with the markup named rather than guessed at.
		n.Child(render.El("body").
			Attr("format", c.BodyFormat).
			SetCDATA(c.Body))
	}
	return n
}

// CommentColumns is the default TSV column set for `issue comment list`.
//
// The body is last because it is the only unbounded field, so a truncated
// terminal still shows every other column.
func CommentColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "author", Path: "author@display"},
		{Header: "created", Path: "created"},
		{Header: "body", Path: "body"},
	}
}

func commentListCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "comment", "list"},
		Summary: "List an issue's comments",
		Description: strings.TrimSpace(`
Returns the comments on an issue, oldest first.

Data Center serves wiki markup, which is carried through unchanged. Cloud
serves an Atlassian Document Format document, which is converted to markdown —
losslessly, or not at all: a body holding something markdown cannot represent
is an error naming it rather than an approximation. --raw-body emits the
document itself. The format attribute says which you have.

A restricted comment carries the role or group it is limited to. That is worth
reading before quoting one: a comment that looks public in a list may not be.

Reading takes no write tag, so this is in every build.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue comment list ENG-101",
			buildinfo.App + " issue comment list ENG-101 --format json --limit all",
			buildinfo.App + " issue comment list ENG-101 --raw-body",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Flags: []registry.Flag{{
			Name: "page-size", Type: registry.TypeInt,
			Usage: "results per HTTP request, 1 to 100; transport tuning only",
		}, rawBodyFlag()},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "comments",
		Columns:        CommentColumns(),
		Outputs: []registry.Output{
			{Kind: KindCommentList, Version: VersionCommentList},
		},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Usage, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			if err := requireIssueKey(inv); err != nil {
				return err
			}
			return requirePageSize(inv)
		},
		Stream: runCommentList,
	}
}

// requireIssueKey is the local key check, shared by every comment verb.
//
// It is here rather than in the write file because `comment list` needs it too
// and that file is not compiled into a reader build.
func requireIssueKey(inv *registry.Invocation) error {
	if len(inv.Args) == 0 {
		return errs.Usage("INVALID_KEY", "an issue key is required").
			WithDetail("an issue key looks like ENG-123")
	}
	if _, ok := ParseKey(inv.Args[0]); !ok {
		return errs.Usage("INVALID_KEY", "%q is not an issue key", inv.Args[0]).
			WithDetail("an issue key looks like ENG-123").
			WithRemedy("pass a key, not an id or a summary")
	}
	return nil
}

// CommentPage is one page of comments plus what the server said about the rest.
type CommentPage struct {
	Comments []Comment
	StartAt  int
	// Total is the server's count of the whole thread, and nil when it did not
	// report one. A pointer rather than an int because absent and zero are
	// different facts and only one of them means "there is nothing more": see
	// exhausted in client.go for what reading them as the same one cost.
	Total *int
}

// ListComments reads one page.
//
// This endpoint pages by offset on both deployments — there is no cursor here,
// even on Cloud — so the caller loops on startAt. A comment inserted mid-run
// shifts the window, which is why the result says how many the server claimed
// rather than pretending the count is stable.
func (c *Client) ListComments(
	ctx context.Context, key string, startAt, pageSize int,
) (CommentPage, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return CommentPage{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}

	query := url.Values{
		"startAt":    {strconv.Itoa(startAt)},
		"maxResults": {strconv.Itoa(pageSize)},
		// Oldest first, always. The server's default is undocumented, and a
		// conversation read in an unspecified order is not a conversation.
		"orderBy": {"created"},
	}

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) + "/comment",
		Query:  query,
	})
	if err != nil {
		return CommentPage{}, err
	}
	if err := transport.Err(resp); err != nil {
		return CommentPage{}, err
	}

	var page struct {
		StartAt    int          `json:"startAt"`
		MaxResults int          `json:"maxResults"`
		Total      *int         `json:"total"`
		Comments   []rawComment `json:"comments"`
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		return CommentPage{}, errs.Remote("MALFORMED_COMMENTS",
			"%s did not return usable comments", resp.URL).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}

	out := CommentPage{StartAt: page.StartAt, Total: page.Total}
	for _, raw := range page.Comments {
		converted, err := raw.convert(c.Body)
		if err != nil {
			return CommentPage{}, err
		}
		out.Comments = append(out.Comments, converted)
	}
	return out, nil
}

func runCommentList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	if inv.Jira == nil {
		return registry.StreamResult{},
			errs.Runtime("NO_SESSION", "issue comment list has no connection to Jira")
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return registry.StreamResult{}, err
	}
	client := &Client{Transport: conn, Site: info, Body: bodyMode(inv)}

	pageSize, err := resolvePageSize(inv.Flags.Int("page-size"))
	if err != nil {
		return registry.StreamResult{}, err
	}

	return streamOffsetPaged(inv, out, pageSize,
		func(startAt, want int) ([]Comment, *int, error) {
			page, err := client.ListComments(ctx, inv.Args[0], startAt, want)
			if err != nil {
				return nil, nil, err
			}
			return page.Comments, page.Total, nil
		},
		func(c Comment) *render.Node { return c.Node() })
}

// CommentListDoc renders comments as a document.
func CommentListDoc(comments []Comment, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(comments))
	for _, c := range comments {
		items = append(items, c.Node())
	}
	return render.List(KindCommentList, VersionCommentList, &render.Collection{
		Name:     "comments",
		Items:    items,
		Complete: complete,
		Columns:  CommentColumns(),
	})
}
