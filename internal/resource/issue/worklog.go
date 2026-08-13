package issue

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/transport"
)

// Kinds the worklog commands emit.
const (
	KindWorklogList    = "issue.worklog.list"
	VersionWorklogList = 2
)

func init() {
	registry.Register(worklogListCommand())

	render.RegisterSchema(KindWorklogList, WorklogSchema())
}

// WorklogSchema is the shape of one recorded piece of work.
func WorklogSchema() *render.Schema {
	return &render.Schema{
		Element: "worklog",
		Attrs:   []render.Field{{Name: "id", Type: render.TypeString}},
		Children: []render.Child{
			{Schema: authorSchema()},
			{Schema: &render.Schema{
				// Reported twice and converted never: Jira's own wording as
				// text for reading, seconds as an attribute for arithmetic.
				// Deriving either from the other means re-implementing the
				// site's working day, which is configurable.
				Element: "time-spent",
				Attrs:   []render.Field{{Name: "seconds", Type: render.TypeInt}},
				Text:    &render.Field{Type: render.TypeString},
			}},
			// When the work happened, which is not when it was logged.
			{Schema: render.Leaf("started", render.TypeTimestamp)},
			{Schema: render.Leaf("created", render.TypeTimestamp)},
			{Schema: bodySchema("comment"), Optional: true},
		},
	}
}

// Worklog is one recorded piece of work.
type Worklog struct {
	ID     string
	Author User
	// TimeSpent is Jira's own wording, e.g. "3h 30m". Seconds is the same
	// quantity as a number, and both are reported because one is for reading
	// and the other is for arithmetic — deriving either from the other means
	// re-implementing Jira's working day, which is configurable per site.
	TimeSpent string
	Seconds   int
	// Started is when the work happened, which is not when it was logged.
	Started    string
	Created    string
	Comment    string
	BodyFormat string
}

type rawWorklog struct {
	ID               string          `json:"id"`
	Author           *rawUser        `json:"author"`
	TimeSpent        string          `json:"timeSpent"`
	TimeSpentSeconds int             `json:"timeSpentSeconds"`
	Started          string          `json:"started"`
	Created          string          `json:"created"`
	Comment          json.RawMessage `json:"comment"`
}

func (r rawWorklog) convert(mode BodyMode) (Worklog, error) {
	started, err := normalizeTime("started", r.Started)
	if err != nil {
		return Worklog{}, err
	}
	created, err := normalizeTime("created", r.Created)
	if err != nil {
		return Worklog{}, err
	}

	comment, format, err := decodeDescription(r.Comment, mode)
	if err != nil {
		return Worklog{}, err
	}
	return Worklog{
		ID: r.ID, Author: r.Author.convert(),
		TimeSpent: r.TimeSpent, Seconds: r.TimeSpentSeconds,
		Started: started, Created: created,
		Comment: comment, BodyFormat: format,
	}, nil
}

// Node renders one worklog entry.
func (w Worklog) Node() *render.Node {
	n := render.El("worklog").Attr("id", w.ID)

	n.Child(render.El("author").
		AttrIf("id", w.Author.ID).
		Attr("display", w.Author.Display))
	n.Child(render.El("time-spent").
		Attr("seconds", strconv.Itoa(w.Seconds)).
		SetText(w.TimeSpent))
	n.Leaf("started", w.Started)
	n.Leaf("created", w.Created)

	if w.Comment != "" {
		n.Child(render.El("comment").
			Attr("format", w.BodyFormat).
			SetCDATA(w.Comment))
	}
	return n
}

// WorklogColumns is the default TSV column set for `issue worklog list`.
func WorklogColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "author", Path: "author@display"},
		{Header: "started", Path: "started"},
		{Header: "seconds", Path: "time-spent@seconds"},
		{Header: "time-spent", Path: "time-spent"},
	}
}

func worklogListCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "worklog", "list"},
		Summary: "List the work logged against an issue",
		Description: strings.TrimSpace(`
Returns every worklog entry on an issue, oldest first.

Time is reported twice: as Jira words it, e.g. "3h 30m", and in seconds. One is
for reading and the other is for arithmetic, and deriving either from the other
means re-implementing the site's working day — which is configurable, so a
client that guessed at it would be wrong on exactly the sites that care.

Started is when the work happened. Created is when somebody said so. They are
often different, and summing the wrong one answers a different question.

An entry's note follows the same rule every body does: wiki markup on Data
Center is carried through, a Cloud document is converted to markdown or refused
naming what stopped it, and --raw-body emits the document itself.

Reading takes no write tag, so this is in every build.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue worklog list ENG-101",
			buildinfo.App + " issue worklog list ENG-101 --format json --limit all",
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
		CollectionName: "worklogs",
		Columns:        WorklogColumns(),
		Outputs: []registry.Output{
			{Kind: KindWorklogList, Version: VersionWorklogList},
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
		Stream: runWorklogList,
	}
}

// WorklogPage is one page plus what the server said about the rest.
type WorklogPage struct {
	Worklogs []Worklog
	// Total is nil when the server reported no count, which is not the same as
	// a count of zero. See CommentPage.Total and exhausted in client.go.
	Total *int
}

// ListWorklogs reads one page. Like comments, this endpoint pages by offset on
// both deployments.
func (c *Client) ListWorklogs(
	ctx context.Context, key string, startAt, pageSize int,
) (WorklogPage, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return WorklogPage{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) + "/worklog",
		Query: url.Values{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(pageSize)},
		},
	})
	if err != nil {
		return WorklogPage{}, err
	}
	if err := transport.Err(resp); err != nil {
		return WorklogPage{}, err
	}

	var page struct {
		StartAt  int          `json:"startAt"`
		Total    *int         `json:"total"`
		Worklogs []rawWorklog `json:"worklogs"`
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		return WorklogPage{}, errs.Remote("MALFORMED_WORKLOGS",
			"%s did not return usable worklogs", parsed).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}

	out := WorklogPage{Total: page.Total}
	for _, raw := range page.Worklogs {
		converted, err := raw.convert(c.Body)
		if err != nil {
			return WorklogPage{}, err
		}
		out.Worklogs = append(out.Worklogs, converted)
	}
	return out, nil
}

func runWorklogList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, err := readClientFor(ctx, inv, "issue worklog list")
	if err != nil {
		return registry.StreamResult{}, err
	}
	pageSize, err := resolvePageSize(inv.Flags.Int("page-size"))
	if err != nil {
		return registry.StreamResult{}, err
	}

	return streamOffsetPaged(inv, out, pageSize,
		func(startAt, want int) ([]Worklog, *int, error) {
			page, err := client.ListWorklogs(ctx, inv.Args[0], startAt, want)
			if err != nil {
				return nil, nil, err
			}
			return page.Worklogs, page.Total, nil
		},
		func(w Worklog) *render.Node { return w.Node() })
}

// WorklogListDoc renders worklogs as a document.
func WorklogListDoc(worklogs []Worklog, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(worklogs))
	for _, w := range worklogs {
		items = append(items, w.Node())
	}
	return render.List(KindWorklogList, VersionWorklogList, &render.Collection{
		Name: "worklogs", Items: items, Complete: complete, Columns: WorklogColumns(),
	})
}

// duration is Jira's own time format: an optional count of weeks, days, hours,
// and minutes, largest first.
var duration = regexp.MustCompile(
	`^\s*(?:\d+w\s*)?(?:\d+d\s*)?(?:\d+h\s*)?(?:\d+m\s*)?\s*$`,
)

// ValidateDuration rejects a time this tool knows Jira will not take.
//
// It is checked locally because the server's refusal is a 400 that names the
// whole request rather than the field, and because a mistyped duration is the
// kind of thing that logs 3 minutes where 3 hours was meant. What it does not
// do is convert: a week is not five days on every site, and this tool does not
// decide how long anyone's day is.
func ValidateDuration(spent string) error {
	trimmed := strings.TrimSpace(spent)
	if trimmed == "" {
		return errs.Usage("INVALID_DURATION", "a time is required").
			WithDetail(`Jira's format, e.g. "3h", "1d 4h", "2w"`)
	}
	// A string of only whitespace and separators matches the pattern without
	// carrying a quantity, so the pattern alone is not enough.
	if !duration.MatchString(trimmed) || !strings.ContainsAny(trimmed, "0123456789") {
		return errs.Usage("INVALID_DURATION", "%q is not a Jira duration", spent).
			WithDetail(`the format is a count of w, d, h, or m, largest first, ` +
				`e.g. "3h", "1d 4h", "2w 3d"`).
			WithRemedy("this tool does not convert between them, because a " +
				"working week is a site setting")
	}
	return nil
}
