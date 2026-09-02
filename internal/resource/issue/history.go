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
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// Kinds the history command emits.
const (
	KindHistory    = "issue.history"
	VersionHistory = 1
)

func init() {
	registry.Register(historyCommand())

	render.RegisterSchema(KindHistory, ChangeSchema())
}

// ChangeSchema is the shape of one recorded change.
//
// A row is one *item*, not one save. Jira records a save as an entry holding
// however many fields moved at once, and nesting them would leave TSV with no
// honest projection: a column set has to name one field, and a save that
// touched three would have to pick. Flattening repeats the entry id and the
// timestamp across the items that shared them, which is what lets a consumer
// group them again.
func ChangeSchema() *render.Schema {
	return &render.Schema{
		Element: "change",
		Attrs: []render.Field{
			// The id of the save this item belonged to. Several items can carry
			// the same one.
			{Name: "id", Type: render.TypeString},
			// The field's display name, which is what Jira records and what a
			// person recognises.
			{Name: "field", Type: render.TypeString},
			// The field's id, where the server sends one. Jira 9.12 Data Center
			// sends none at all — measured, not assumed — and a custom field is
			// only identifiable by its id, so this is absent rather than
			// guessed from the display name. A consumer that needs to tell two
			// same-named custom fields apart cannot do it from a Data Center
			// changelog, and an empty attribute would hide that.
			{Name: "field-id", Type: render.TypeString, Optional: true},
			// "jira" for a built-in field and "custom" for a custom one, as the
			// server classifies it.
			{Name: "field-type", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			{Schema: authorSchema()},
			{Schema: render.Leaf("created", render.TypeTimestamp)},
			{Schema: valueSchema("from"), Optional: true},
			{Schema: valueSchema("to"), Optional: true},
		},
	}
}

// valueSchema is one side of a change.
//
// Optional as a whole, because a field set from nothing has no previous value
// and a field cleared has no new one, and those are facts rather than empty
// strings. The text is the display form; the id is what the field actually
// holds and is absent for a field whose value has no id, like a summary.
func valueSchema(element string) *render.Schema {
	return &render.Schema{
		Element: element,
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeString, Optional: true},
		},
		Text: &render.Field{Type: render.TypeString},
	}
}

// Change is one field moving from one value to another, in one save.
type Change struct {
	// ID identifies the save, not the item. Two fields changed together share
	// it.
	ID        string
	Author    User
	Created   string
	Field     string
	FieldID   string
	FieldType string
	// From and To are the display forms, and FromID and ToID what the field
	// held. Set reports whether the server sent that side at all: a field set
	// from nothing has no from, which is not the same as a from that is empty.
	From    string
	FromID  string
	HasFrom bool
	To      string
	ToID    string
	HasTo   bool
}

// Node renders one change.
func (c Change) Node() *render.Node { return c.node("") }

// node renders one change, naming the issue it happened to when there is one to
// name.
//
// `issue history` has no key on a row, because every row came from the one in
// the argument. `issue changes` merges rows from many issues and every one of
// them needs it. They share this builder so the two kinds cannot drift in what a
// change looks like, and the attribute is first when it is there, which is the
// order FeedChangeSchema declares.
func (c Change) node(issue string) *render.Node {
	n := render.El("change").
		AttrIf("issue", issue).
		Attr("id", c.ID).
		Attr("field", c.Field).
		AttrIf("field-id", c.FieldID).
		AttrIf("field-type", c.FieldType)

	n.Child(render.El("author").
		AttrIf("id", c.Author.ID).
		Attr("display", c.Author.Display))
	n.Leaf("created", c.Created)

	if c.HasFrom {
		n.Child(render.El("from").AttrIf("id", c.FromID).SetText(c.From))
	}
	if c.HasTo {
		n.Child(render.El("to").AttrIf("id", c.ToID).SetText(c.To))
	}
	return n
}

// HistoryColumns is the default TSV column set for `issue history`.
//
// The two value columns are last because they are the unbounded ones: a
// description edit puts a whole description in each.
func HistoryColumns() []render.Column {
	return []render.Column{
		{Header: "created", Path: "created"},
		{Header: "author", Path: "author@display"},
		{Header: "field", Path: "@field"},
		{Header: "from", Path: "from"},
		{Header: "to", Path: "to"},
	}
}

// rawHistory is one save, as both deployments describe it.
type rawHistory struct {
	ID      string    `json:"id"`
	Author  *rawUser  `json:"author"`
	Created string    `json:"created"`
	Items   []rawItem `json:"items"`
}

// rawItem is one field within a save.
//
// From and To are ids and arrive as strings or as null; FromString and ToString
// are the display forms. Both sides are pointers because absent and empty are
// different facts here: a field set from nothing sends null, and a field set to
// the empty string sends "".
type rawItem struct {
	Field      string  `json:"field"`
	FieldID    string  `json:"fieldId"`
	FieldType  string  `json:"fieldtype"`
	From       *string `json:"from"`
	FromString *string `json:"fromString"`
	To         *string `json:"to"`
	ToString   *string `json:"toString"`
}

// flatten turns one save into one Change per field it touched.
func (r rawHistory) flatten() ([]Change, error) {
	created, err := normalizeTime("created", r.Created)
	if err != nil {
		return nil, err
	}
	author := r.Author.convert()

	out := make([]Change, 0, len(r.Items))
	for _, item := range r.Items {
		c := Change{
			ID: r.ID, Author: author, Created: created,
			Field: item.Field, FieldID: item.FieldID, FieldType: item.FieldType,
		}
		c.From, c.FromID, c.HasFrom = side(item.FromString, item.From)
		c.To, c.ToID, c.HasTo = side(item.ToString, item.To)
		out = append(out, c)
	}
	return out, nil
}

// side reduces one half of an item to text, id, and whether the server sent it.
//
// A side is present when either half of it is. Jira sends a null id and a
// display string for a field with no id of its own, like a summary, and sends
// an id with a null string for a value whose display form it did not resolve.
func side(text, id *string) (string, string, bool) {
	if text == nil && id == nil {
		return "", "", false
	}
	var out, outID string
	if text != nil {
		out = *text
	}
	if id != nil {
		outID = *id
	}
	return out, outID, true
}

// HistoryPage is one page of changes plus what the server said about the rest.
//
// Total is the server's count of *saves*, not of the flattened rows, which is
// why it is carried separately rather than derived: paging is over saves and
// the rows are what comes out the other side.
type HistoryPage struct {
	Changes []Change
	// Saves is how many entries this page held, which is what an offset
	// advances by.
	Saves int
	// Total is nil when the changelog reported no count. See CommentPage.Total
	// and exhausted in client.go: an absent count read as zero ends a paged
	// listing after its first page and calls the result complete.
	Total *int
}

// ListHistory reads an issue's changelog.
//
// The two deployments are structurally different here and the difference is
// not cosmetic. Cloud serves a paged /issue/{key}/changelog. Data Center does
// not have that route at all — it answers 404 — and returns the whole history
// under expand=changelog on the issue itself. So this pages on one deployment
// and cannot on the other, and the caller is told which happened rather than
// being handed two shapes wearing one name.
func (c *Client) ListHistory(
	ctx context.Context, key string, startAt, pageSize int,
) (HistoryPage, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return HistoryPage{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}

	req := transport.Request{Method: transport.MethodGet}
	if c.Site.Kind == site.Cloud {
		req.Path = c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) + "/changelog"
		req.Query = url.Values{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(pageSize)},
		}
	} else {
		req.Path = c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String())
		req.Query = url.Values{
			"expand": {"changelog"},
			// The changelog is the whole point of the request and the fields
			// are not. Asking for one small field rather than none keeps the
			// response to the history plus an issue key: `fields=` empty is
			// read as "every field" by both deployments, which would fetch a
			// description to throw it away.
			"fields": {"summary"},
		}
	}

	resp, err := c.Transport.Do(ctx, req)
	if err != nil {
		return HistoryPage{}, err
	}
	if err := transport.Err(resp); err != nil {
		return HistoryPage{}, err
	}

	// Cloud's paged bean carries the entries in `values`; Data Center nests a
	// `changelog` object holding `histories`. Decoding both here rather than
	// branching twice keeps the two spellings beside each other, where the
	// difference is visible.
	var body struct {
		Values     []rawHistory `json:"values"`
		Total      *int         `json:"total"`
		StartAt    int          `json:"startAt"`
		MaxResults int          `json:"maxResults"`
		Changelog  *struct {
			Histories []rawHistory `json:"histories"`
			Total     *int         `json:"total"`
			StartAt   int          `json:"startAt"`
		} `json:"changelog"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return HistoryPage{}, errs.Remote("MALFORMED_HISTORY",
			"%s did not return a usable changelog", resp.URL).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}

	saves, total := body.Values, body.Total
	if body.Changelog != nil {
		saves, total = body.Changelog.Histories, body.Changelog.Total
	}

	out := HistoryPage{Saves: len(saves), Total: total}
	for _, save := range saves {
		changes, err := save.flatten()
		if err != nil {
			return HistoryPage{}, err
		}
		out.Changes = append(out.Changes, changes...)
	}
	return out, nil
}

func historyCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "history"},
		Summary: "List what changed on an issue, and who changed it",
		Description: strings.TrimSpace(`
Returns an issue's recorded changes, oldest first: who moved which field, when,
and what it moved between.

This is the question ` + "`issue get`" + ` cannot answer. An issue reports what it is
now; the changelog is the only record of how it got there, and the only one
that survives a change being reverted — two edits that end where they started
leave the issue looking untouched and leave two entries here.

One row is one field. Jira records a save as an entry holding however many
fields moved at once, and each of those becomes its own row carrying the same
change id and timestamp, so a consumer can group them again. A column set has
to name one field, so nesting them would leave the default format with nothing
honest to project.

The two deployments differ, and the result says which one answered. Cloud
serves a paged changelog and this pages through it. Data Center has no such
endpoint — it answers 404 — and returns the whole history alongside the issue
instead, so there the entire changelog arrives in one request or not at all. A
Data Center history longer than that one response can carry is reported
incomplete rather than silently cut.

--changed-field narrows the rows to the fields you name, and it is the flag to
reach for before reading this output at all. A description edit puts a whole
description on each side of one row, twice for a revision, and an issue with a
few of those costs more to read than every other question about it put
together. --changed-field status is "did anybody move this, and when".

It matches what the changelog records: the field's display name, and its id
where the server sends one. It does not resolve names through the site's field
catalogue, because Jira 9.12 Data Center sends no field id in a changelog at
all, so resolving a name to an id would match nothing there. It is applied
here rather than by the server — neither deployment's changelog route filters
by field — so it saves output and context, not requests.

A --changed-field that matches nothing, on an issue that has changes, warns
and names the fields the issue does hold. Asking about a field nobody touched
is a legal question, so it still exits 0.

Comment authorship is not recorded here. Jira writes a field transition to the
changelog and a comment is not a field transition, so "what did this person do
to this issue" needs this and ` + "`issue comment list`" + ` both.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue history ENG-101",
			buildinfo.App + " issue history ENG-101 --changed-field status",
			buildinfo.App + " issue history ENG-101 --format json --limit all",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Flags: []registry.Flag{{
			Name: changedFieldFlag, Type: registry.TypeString, Repeatable: true,
			Usage: "only changes to this field, by the name the changelog " +
				"records or by its id; repeat for several; matched here rather " +
				"than by the server, so it saves output and not requests",
		}, {
			Name: notChangedFieldFlag, Type: registry.TypeString, Repeatable: true,
			Usage: "drop changes to this field; repeat for several, and it " +
				"wins over --" + changedFieldFlag + " where both name one",
		}, {
			Name: "page-size", Type: registry.TypeInt,
			Usage: "results per HTTP request, 1 to 100; transport tuning only, " +
				"and Cloud only — Data Center serves the whole changelog in " +
				"one response and has nothing to page",
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "changes",
		Columns:        HistoryColumns(),
		Outputs: []registry.Output{
			{Kind: KindHistory, Version: VersionHistory},
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
		Stream: runHistory,
	}
}

func runHistory(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	if inv.Jira == nil {
		return registry.StreamResult{},
			errs.Runtime("NO_SESSION", "issue history has no connection to Jira")
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

	// One filter for the whole run, because it accumulates what it rejected
	// across every page in order to say what the issue holds when it matched
	// nothing.
	filter := newHistoryFilter(inv)
	defer filter.warnIfNothingMatched(inv)

	if info.Kind != site.Cloud {
		return streamWholeHistory(ctx, inv, out, client, filter)
	}
	return streamPagedHistory(ctx, inv, out, client, pageSize, filter)
}

// streamPagedHistory is the Cloud path.
//
// It is `streamOffsetPaged` with one difference that rules the shared helper
// out: the offset counts *saves* and a row is one flattened item, so the loop
// has to advance by what the server sent rather than by what was written. A
// save that moved three fields produces three rows, and advancing by rows would
// skip two saves per page and report the result complete.
func streamPagedHistory(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
	client *Client, pageSize int, filter *historyFilter,
) (registry.StreamResult, error) {
	startAt := 0
	for {
		page, err := client.ListHistory(ctx, inv.Args[0], startAt, pageSize)
		if err != nil {
			return registry.StreamResult{}, err
		}
		if page.Saves == 0 {
			// The server ran out, whatever its total claimed. A loop bounded
			// only by what the server says is a loop the server controls.
			return registry.StreamResult{Complete: true}, nil
		}

		// Filtered before the rows are written and after the page is counted.
		// The offset advances by saves, which is what the server paginates,
		// so removing rows cannot make the loop skip or repeat a save — and
		// --limit therefore bounds the rows the caller asked for rather than
		// the rows the server happened to send.
		bounded, err := writeRows(inv, out, filter.apply(page.Changes),
			func(c Change) *render.Node { return c.Node() })
		if err != nil {
			return registry.StreamResult{}, err
		}
		if bounded {
			return registry.StreamResult{Complete: false}, nil
		}
		inv.Progress.Update(out.Count(), reportedTotal(page.Total))

		startAt += page.Saves
		if exhausted(startAt, page.Total) {
			return registry.StreamResult{Complete: true}, nil
		}
		// No total means keep going; the `page.Saves == 0` check above is what
		// ends it then, one request later than a reported total would.
	}
}

// streamWholeHistory is the Data Center path: one request, everything in it.
//
// It deliberately does not loop, and that is measured rather than assumed. On
// Jira 9.12, 120 saves came back as 120 with the changelog reporting
// `maxResults=120`, a count wearing a bound's name; asking the same route for
// `startAt=50&maxResults=10` returned all 120 again, beginning at the first.
// So a loop that advanced by what it received would write the same entries a
// second time and then call the result complete.
//
// Where the server says it holds more than it sent, that is reported rather
// than resolved, because there is no second request that would get the rest.
func streamWholeHistory(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
	client *Client, filter *historyFilter,
) (registry.StreamResult, error) {
	page, err := client.ListHistory(ctx, inv.Args[0], 0, 0)
	if err != nil {
		return registry.StreamResult{}, err
	}

	bounded, err := writeRows(inv, out, filter.apply(page.Changes),
		func(c Change) *render.Node { return c.Node() })
	if err != nil {
		return registry.StreamResult{}, err
	}
	inv.Progress.Update(out.Count(), reportedTotal(page.Total))

	// Complete when the caller's limit did not stop it and the server sent
	// every save it claimed to have.
	//
	// A changelog that claimed nothing is not a changelog that was whole. This
	// path makes exactly one request by design, so there is no second one to
	// resolve the doubt with, and an unknown length is reported as partial.
	return registry.StreamResult{
		Complete: !bounded && exhausted(page.Saves, page.Total),
	}, nil
}

// HistoryDoc renders changes as a document, for a caller that buffers rather
// than streams.
func HistoryDoc(changes []Change, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(changes))
	for _, c := range changes {
		items = append(items, c.Node())
	}
	return render.List(KindHistory, VersionHistory, &render.Collection{
		Name:     "changes",
		Items:    items,
		Complete: complete,
		Columns:  HistoryColumns(),
	})
}
