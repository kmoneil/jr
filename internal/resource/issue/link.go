package issue

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kinds the link commands emit.
const (
	KindLinkList    = "issue.link.list"
	VersionLinkList = 1
)

func init() {
	registry.Register(linkListCommand())

	render.RegisterSchema(KindLinkList, LinkSchema())
}

// LinkSchema is the shape of one link, read from this issue's side.
func LinkSchema() *render.Schema {
	return &render.Schema{
		Element: "link",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeString},
			// The link type's own name, e.g. "Blocks".
			{Name: "type", Type: render.TypeString},
		},
		Children: []render.Child{
			// The phrase that applies from this issue's side. "blocks" and "is
			// blocked by" are the same link read from opposite ends, and a
			// caller acting on the wrong one acts on the wrong issue.
			{Schema: render.Leaf("relationship", render.TypeString)},
			{Schema: &render.Schema{
				Element: "issue",
				Attrs: []render.Field{
					{Name: "key", Type: render.TypeString},
					{Name: "status", Type: render.TypeString, Optional: true},
					{Name: "type", Type: render.TypeString, Optional: true},
				},
				Text: &render.Field{Type: render.TypeString},
			}},
		},
	}
}

// Link is one relationship between two issues, from the point of view of the
// issue it was read from.
//
// Direction is not decoration. "blocks" and "is blocked by" are the same link
// read from opposite ends, and a caller acting on the wrong one acts on the
// wrong issue — so the phrase that applies *here* is what gets reported, not
// the type's name.
type Link struct {
	ID string
	// Type is the link type's own name, e.g. "Blocks".
	Type string
	// Relationship is the phrase that applies from this issue's side, e.g.
	// "blocks" or "is blocked by".
	Relationship string
	// Other is the issue at the far end.
	Other       string
	OtherStatus string
	OtherType   string
	Summary     string
}

// LinkType is one relationship a site offers, with both its readings.
type LinkType struct {
	ID   string
	Name string
	// Inward and Outward are the two phrasings. A link written as
	// "inwardIssue <inward> outwardIssue" reads the other way as
	// "outwardIssue <outward> inwardIssue".
	Inward  string
	Outward string
}

// rawLink is the shape inside an issue's issuelinks field.
type rawLink struct {
	ID   string `json:"id"`
	Type struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Inward  string `json:"inward"`
		Outward string `json:"outward"`
	} `json:"type"`
	// Exactly one of these is set, and which one is what says the direction.
	InwardIssue  *rawLinkedIssue `json:"inwardIssue"`
	OutwardIssue *rawLinkedIssue `json:"outwardIssue"`
}

type rawLinkedIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Status  *struct {
			Name string `json:"name"`
		} `json:"status"`
		IssueType *struct {
			Name string `json:"name"`
		} `json:"issuetype"`
	} `json:"fields"`
}

func (r rawLink) convert() Link {
	out := Link{ID: r.ID, Type: r.Type.Name}

	// The field that is populated names the far end, and the phrase that goes
	// with it is the one that reads correctly from here. Jira writes a link as
	// "inwardIssue <inward> outwardIssue": so when the *outward* issue is the
	// far end, this issue is the inward one and "is blocked by" applies.
	var other *rawLinkedIssue
	switch {
	case r.OutwardIssue != nil:
		other, out.Relationship = r.OutwardIssue, r.Type.Inward
	case r.InwardIssue != nil:
		other, out.Relationship = r.InwardIssue, r.Type.Outward
	default:
		return out
	}

	out.Other = other.Key
	out.Summary = other.Fields.Summary
	if other.Fields.Status != nil {
		out.OtherStatus = other.Fields.Status.Name
	}
	if other.Fields.IssueType != nil {
		out.OtherType = other.Fields.IssueType.Name
	}
	return out
}

// Node renders one link.
func (l Link) Node() *render.Node {
	n := render.El("link").
		Attr("id", l.ID).
		Attr("type", l.Type)

	n.Leaf("relationship", l.Relationship)
	n.Child(render.El("issue").
		Attr("key", l.Other).
		AttrIf("status", l.OtherStatus).
		AttrIf("type", l.OtherType).
		SetText(l.Summary))
	return n
}

// LinkColumns is the default TSV column set for `issue link list`.
func LinkColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "relationship", Path: "relationship"},
		{Header: "key", Path: "issue@key"},
		{Header: "status", Path: "issue@status"},
		{Header: "summary", Path: "issue"},
	}
}

func linkListCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "link", "list"},
		Summary: "List the issues linked to one issue",
		Description: strings.TrimSpace(`
Returns every link on an issue, each read from this issue's side.

The relationship is the phrase that applies here — "blocks" or "is blocked by",
not the type's name. They are the same link seen from opposite ends, and a
caller acting on the wrong reading acts on the wrong issue.

Links arrive with the issue rather than from an endpoint of their own, so this
costs one request and takes no write tag.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue link list ENG-101",
			buildinfo.App + " issue link list ENG-101 --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "links",
		Columns:        LinkColumns(),
		Outputs: []registry.Output{
			{Kind: KindLinkList, Version: VersionLinkList},
		},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			return requireIssueKey(inv)
		},
		Stream: runLinkList,
	}
}

// ListLinks reads an issue's links.
//
// There is no links endpoint: they come back as a field on the issue, so this
// asks for exactly that field and nothing else.
func (c *Client) ListLinks(ctx context.Context, key string) ([]Link, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return nil, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()),
		Query:  url.Values{"fields": {"issuelinks"}},
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var body struct {
		Fields struct {
			IssueLinks []rawLink `json:"issuelinks"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return nil, errs.Remote("MALFORMED_LINKS",
			"%s did not return usable links", parsed).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}

	out := make([]Link, 0, len(body.Fields.IssueLinks))
	for _, raw := range body.Fields.IssueLinks {
		out = append(out, raw.convert())
	}
	// Ordered so two runs agree. The server promises nothing about the order of
	// this field, and a collection that reorders between invocations is one a
	// script cannot diff.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Relationship != out[j].Relationship {
			return out[i].Relationship < out[j].Relationship
		}
		return out[i].Other < out[j].Other
	})
	return out, nil
}

func runLinkList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, err := readClientFor(ctx, inv, "issue link list")
	if err != nil {
		return registry.StreamResult{}, err
	}

	links, err := client.ListLinks(ctx, inv.Args[0])
	if err != nil {
		return registry.StreamResult{}, err
	}

	complete := true
	if !inv.Limit.All && len(links) > inv.Limit.N {
		links = links[:inv.Limit.N]
		complete = false
	}
	for _, link := range links {
		if err := out.Write(link.Node()); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), out.Count())
	return registry.StreamResult{Complete: complete}, nil
}

// LinkListDoc renders links as a document.
func LinkListDoc(links []Link, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(links))
	for _, l := range links {
		items = append(items, l.Node())
	}
	return render.List(KindLinkList, VersionLinkList, &render.Collection{
		Name: "links", Items: items, Complete: complete, Columns: LinkColumns(),
	})
}

// FetchLinkTypes reads the relationships this site offers.
//
// It stays in this package rather than in internal/site, unlike the field
// catalogue and transitions: those are there because another resource has to
// resolve against them, and nothing outside issues links issues.
func (c *Client) FetchLinkTypes(ctx context.Context) ([]LinkType, error) {
	path := c.Site.APIBase() + "/issueLinkType"

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path,
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var body struct {
		IssueLinkTypes []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Inward  string `json:"inward"`
			Outward string `json:"outward"`
		} `json:"issueLinkTypes"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return nil, errs.Remote("MALFORMED_LINK_TYPES",
			"%s did not return usable link types", path).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}

	out := make([]LinkType, 0, len(body.IssueLinkTypes))
	for _, t := range body.IssueLinkTypes {
		out = append(out, LinkType{
			ID: t.ID, Name: t.Name, Inward: t.Inward, Outward: t.Outward,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Direction says which end of a link an issue is at.
type Direction int

// The two ends. Jira writes a link as "inwardIssue <inward> outwardIssue", so
// the phrase a caller types decides which of their two issues goes where.
const (
	// Outward means the first issue is the outwardIssue: "A blocks B".
	Outward Direction = iota
	// Inward means the first issue is the inwardIssue: "A is blocked by B".
	Inward
)

// ResolveLinkType turns the phrase a caller typed into a type and a direction.
//
// The phrase is the input, not the type name, and that is deliberate. "Blocks"
// names a relationship without saying which way it runs, and the two readings
// create opposite links between the same pair of issues. Guessing would be
// wrong half the time and hard to notice afterwards — the issue that ends up
// blocked is the one nobody was watching.
func ResolveLinkType(types []LinkType, phrase string) (LinkType, Direction, error) {
	want := strings.TrimSpace(phrase)
	if want == "" {
		return LinkType{}, Outward, errs.Usage("INVALID_LINK_TYPE",
			"a relationship is required")
	}

	for _, t := range types {
		if strings.EqualFold(t.Outward, want) {
			return t, Outward, nil
		}
		if strings.EqualFold(t.Inward, want) {
			return t, Inward, nil
		}
	}

	// A bare type name matched nothing above, because the phrases are what is
	// compared. If it names a type, say which two phrases it could have meant
	// rather than choosing one.
	for _, t := range types {
		if strings.EqualFold(t.Name, want) {
			return LinkType{}, Outward, errs.Usage("AMBIGUOUS_LINK_DIRECTION",
				"%q names a link type, not a direction", phrase).
				WithDetail("it reads either way: %q or %q", t.Outward, t.Inward).
				WithRemedy("pass the one that reads correctly, e.g. "+
					"`issue link add ENG-1 %q ENG-2`", t.Outward)
		}
	}

	return LinkType{}, Outward, unknownLinkType(types, phrase)
}

// unknownLinkType lists every phrase the site offers.
//
// The whole set goes in rather than near matches: a site has a handful of link
// types, and the phrasing is customizable, so "did you mean" guesses less
// usefully than simply showing what exists.
func unknownLinkType(types []LinkType, phrase string) error {
	e := errs.Usage("UNKNOWN_LINK_TYPE",
		"this site has no relationship called %q", phrase)
	if len(types) == 0 {
		return e.WithDetail("this site offers no link types at all")
	}

	phrases := make([]string, 0, len(types)*2)
	for _, t := range types {
		phrases = append(phrases, `"`+t.Outward+`"`, `"`+t.Inward+`"`)
	}
	return e.WithDetail("available: %s", strings.Join(phrases, ", ")).
		WithRemedy("pass the phrase that reads correctly between your two issues")
}

// readClientFor is the opening every read verb in this package shares.
func readClientFor(
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
