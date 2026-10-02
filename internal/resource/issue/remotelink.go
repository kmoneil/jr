package issue

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/transport"
)

// Kinds the remote link commands emit.
const (
	KindRemoteLinkList    = "issue.remotelink.list"
	VersionRemoteLinkList = 1
)

// withRemoteLinksFlag folds the remote links into the `issue get` record.
const withRemoteLinksFlag = "with-remote-links"

func init() {
	registry.Register(remoteLinkListCommand())

	render.RegisterSchema(KindRemoteLinkList, RemoteLinkSchema())
}

// RemoteLinkSchema is the shape of one remote link.
func RemoteLinkSchema() *render.Schema {
	return &render.Schema{
		Element: "remotelink",
		Attrs: []render.Field{
			// The id is what an activity item names: the changelog entry for an
			// added link carries it in `to`, and for a removed one in `from`.
			{Name: "id", Type: render.TypeString},
			// The identity an integration chooses so it can find its own link
			// again. Absent on a link a person added.
			{Name: "global-id", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			// The application that wrote the link, by name. A link a person
			// added arrives from Jira as an empty application object, and both
			// deployments send the same thing; empty-object and absent mean the
			// same fact, so it is reported here by absence.
			{Schema: render.Leaf("application", render.TypeString), Optional: true},
			{Schema: render.Leaf("relationship", render.TypeString), Optional: true},
			{Schema: render.Leaf("title", render.TypeString)},
			{Schema: render.Leaf("url", render.TypeString)},
			{Schema: render.Leaf("summary", render.TypeString), Optional: true},
			// The remote side's own resolution, where its application reports
			// one. Absent when it never has, which is every hand-added link.
			{Schema: render.Leaf("resolved", render.TypeBool), Optional: true},
		},
	}
}

// RemoteLink is one link from an issue to something outside Jira: a web page,
// a document, a commit or build in another tool.
type RemoteLink struct {
	ID       int64
	GlobalID string
	// Application is the name of the tool that wrote the link. Empty when a
	// person added it by hand.
	Application  string
	Relationship string
	Title        string
	URL          string
	Summary      string
	// Resolved is the remote side's own status, where its application reports
	// one. Nil when it never has.
	Resolved *bool
}

// rawRemoteLink is the shape the remotelink endpoint serves. The id is a JSON
// number, unlike most Jira ids, and both deployments serve the same shape.
type rawRemoteLink struct {
	ID          int64  `json:"id"`
	GlobalID    string `json:"globalId"`
	Application struct {
		Name string `json:"name"`
	} `json:"application"`
	Relationship string `json:"relationship"`
	Object       struct {
		URL     string `json:"url"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Status  struct {
			Resolved *bool `json:"resolved"`
		} `json:"status"`
	} `json:"object"`
}

func (r rawRemoteLink) convert() RemoteLink {
	return RemoteLink{
		ID:           r.ID,
		GlobalID:     r.GlobalID,
		Application:  r.Application.Name,
		Relationship: r.Relationship,
		Title:        r.Object.Title,
		URL:          r.Object.URL,
		Summary:      r.Object.Summary,
		Resolved:     r.Object.Status.Resolved,
	}
}

// Node renders one remote link.
func (l RemoteLink) Node() *render.Node {
	n := render.El("remotelink").
		Attr("id", strconv.FormatInt(l.ID, 10)).
		AttrIf("global-id", l.GlobalID)
	n.LeafIf("application", l.Application)
	n.LeafIf("relationship", l.Relationship)
	n.Leaf("title", l.Title)
	n.Leaf("url", l.URL)
	n.LeafIf("summary", l.Summary)
	if l.Resolved != nil {
		n.Leaf("resolved", strconv.FormatBool(*l.Resolved))
	}
	return n
}

// RemoteLinkColumns is the default TSV column set for `issue remotelink list`.
func RemoteLinkColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "application", Path: "application"},
		{Header: "relationship", Path: "relationship"},
		{Header: "title", Path: "title"},
		{Header: "url", Path: "url"},
	}
}

func remoteLinkListCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "remotelink", "list"},
		Summary: "List an issue's links to things outside Jira",
		Description: strings.TrimSpace(`
Returns every remote link on an issue: the web links a person pastes into it,
and the links an integration writes to a commit, a build, a document.

A link an application wrote names that application and may carry the remote
side's own resolved state. One a person added arrives with neither, and the
absent application is how the two are told apart. The id is the value an
activity item names when a link is added or removed.

Remote links live at their own endpoint, so this costs one request, and the
set arrives whole: the server neither pages nor truncates it. These are links
to things outside Jira; for the issue-to-issue kind, see issue link list.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue remotelink list ENG-101",
			buildinfo.App + " issue remotelink list ENG-101 --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "remotelinks",
		Columns:        RemoteLinkColumns(),
		Outputs: []registry.Output{
			{Kind: KindRemoteLinkList, Version: VersionRemoteLinkList},
		},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			return requireIssueKey(inv)
		},
		Stream: runRemoteLinkList,
	}
}

// ListRemoteLinks reads an issue's remote links.
//
// The endpoint serves the whole set as a bare array on both deployments, with
// no paging and no envelope, so one request is the entire conversation.
func (c *Client) ListRemoteLinks(ctx context.Context, key string) ([]RemoteLink, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return nil, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path: c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) +
			"/remotelink",
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw []rawRemoteLink
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, errs.Remote("MALFORMED_REMOTE_LINKS",
			"%s did not return usable remote links", parsed).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}

	out := make([]RemoteLink, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.convert())
	}
	// Ordered so two runs agree. The server answers in an order of its own
	// that is not creation order and is promised nowhere; the id is the one
	// value every link carries that cannot tie.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func runRemoteLinkList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, err := readClientFor(ctx, inv, "issue remotelink list")
	if err != nil {
		return registry.StreamResult{}, err
	}

	links, err := client.ListRemoteLinks(ctx, inv.Args[0])
	if err != nil {
		return registry.StreamResult{}, err
	}

	found := len(links)
	links, result := registry.Cut(inv.Limit, links)
	for _, link := range links {
		if err := out.Write(link.Node()); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), found)
	return result, nil
}

// RemoteLinkListDoc renders remote links as a document.
func RemoteLinkListDoc(links []RemoteLink, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(links))
	for _, l := range links {
		items = append(items, l.Node())
	}
	return render.List(KindRemoteLinkList, VersionRemoteLinkList, &render.Collection{
		Name: "remotelinks", Items: items, Complete: complete,
		Columns: RemoteLinkColumns(),
	})
}

// remoteLinksSchema is the container `issue get --with-remote-links` adds.
//
// Plain ListSchema, with no complete attribute: the endpoint hands the whole
// set over in one response, so unlike the comment thread this container cannot
// be partial, and declaring that it could would invite a consumer to check a
// value that is always true.
func remoteLinksSchema() *render.Schema {
	return render.ListSchema("remotelinks", "remotelink", RemoteLinkSchema())
}

// attachRemoteLinks fetches the remote links and hangs them off the issue.
func attachRemoteLinks(ctx context.Context, c *Client, doc *render.Doc, key string) error {
	links, err := c.ListRemoteLinks(ctx, key)
	if err != nil {
		return err
	}
	items := make([]*render.Node, 0, len(links))
	for _, l := range links {
		items = append(items, l.Node())
	}
	doc.Record.Child(render.ListEl("remotelinks", "remotelink", items...))
	return nil
}
