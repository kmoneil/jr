package issue

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kinds the comment commands emit.
const (
	KindCommentList    = "issue.comment.list"
	VersionCommentList = 1
)

func init() {
	registry.Register(commentListCommand())
}

// Comment is one comment, in the shape this tool reports.
type Comment struct {
	ID     string
	Author User
	// Body is carried through unchanged, and BodyFormat says what it is: wiki
	// markup on Data Center, an ADF document as JSON on Cloud. Converting here
	// would mean shipping a half-converter and calling its output markdown.
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

func (r rawComment) convert() (Comment, error) {
	created, err := normalizeTime("created", r.Created)
	if err != nil {
		return Comment{}, err
	}
	updated, err := normalizeTime("updated", r.Updated)
	if err != nil {
		return Comment{}, err
	}

	body, format := decodeDescription(r.Body)
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

A body is carried through unchanged with its markup named: wiki on Data Center,
an Atlassian Document Format document as JSON on Cloud. Nothing is converted,
so nothing is mangled, and the format attribute says which you have.

A restricted comment carries the role or group it is limited to. That is worth
reading before quoting one: a comment that looks public in a list may not be.

Reading takes no write tag, so this is in every build.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue comment list ENG-101",
			buildinfo.App + " issue comment list ENG-101 --format json --limit all",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Flags: []registry.Flag{{
			Name: "page-size", Type: registry.TypeInt,
			Usage: "results per HTTP request, 1 to 100; transport tuning only",
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "comments",
		Columns:        CommentColumns(),
		Outputs: []registry.Output{
			{Kind: KindCommentList, Version: VersionCommentList},
		},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			return requireIssueKey(inv)
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
	Total    int
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
		Total      int          `json:"total"`
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
		converted, err := raw.convert()
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
	client := &Client{Transport: conn, Site: info}

	pageSize, err := resolvePageSize(inv.Flags.Int("page-size"))
	if err != nil {
		return registry.StreamResult{}, err
	}

	startAt := 0
	for {
		page, err := client.ListComments(ctx, inv.Args[0], startAt, pageSize)
		if err != nil {
			return registry.StreamResult{}, err
		}
		if len(page.Comments) == 0 {
			// The server ran out, whatever its total claimed. A loop bounded
			// only by what the server says is a loop the server controls.
			return registry.StreamResult{Complete: true}, nil
		}

		for _, comment := range page.Comments {
			if !inv.Limit.All && out.Count() >= inv.Limit.N {
				// Bounded by the caller, so it is not complete and says so.
				// There is no token: this endpoint pages by offset, and an
				// offset shifts when a comment is added above it.
				return registry.StreamResult{Complete: false}, nil
			}
			if err := out.Write(comment.Node()); err != nil {
				return registry.StreamResult{}, err
			}
		}
		inv.Progress.Update(out.Count(), page.Total)

		startAt += len(page.Comments)
		if startAt >= page.Total {
			return registry.StreamResult{Complete: true}, nil
		}
	}
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
