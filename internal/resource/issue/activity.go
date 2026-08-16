package issue

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// Kinds the activity command emits.
const (
	KindActivity    = "issue.activity"
	VersionActivity = 1
)

// Event kinds. Closed, and extendable only by a schema bump, because branching
// on this attribute is the intended use.
const (
	EventComment    = "comment"
	EventTransition = "transition"
	EventField      = "field"
	EventWorklog    = "worklog"
)

// EventKinds is every kind an event can have, in the order they are documented.
func EventKinds() []string {
	return []string{EventComment, EventTransition, EventField, EventWorklog}
}

func init() {
	registry.Register(activityCommand())

	render.RegisterSchema(KindActivity, EventSchema())
}

// EventSchema is the shape of one thing that happened.
//
// The kind-specific parts are separate optional children rather than one
// polymorphic `detail`, because a consumer branching on `kind` wants the value
// and not a rendering of it: a transition's `from` and `to` are two values, and
// joining them into "In Progress -> Blocked" would make every reader parse the
// arrow back out.
func EventSchema() *render.Schema {
	return &render.Schema{
		Element: "event",
		Attrs: []render.Field{
			{Name: "kind", Type: render.TypeString, Enum: EventKinds()},
			{Name: "issue", Type: render.TypeString},
			// The field a transition or a field event moved. Absent on the
			// other two kinds, which move nothing.
			{Name: "field", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.Leaf("at", render.TypeTimestamp)},
			{Schema: authorSchema()},
			{Schema: valueSchema("from"), Optional: true},
			{Schema: valueSchema("to"), Optional: true},
			{Schema: bodySchema("body"), Optional: true},
			{Schema: &render.Schema{
				// A worklog's duration, as the server's own words and as the
				// seconds it resolved them to. "2h" is what somebody typed and
				// 7200 is what anything summing them needs.
				Element: "time-spent",
				Attrs:   []render.Field{{Name: "seconds", Type: render.TypeInt}},
				Text:    &render.Field{Type: render.TypeString},
			}, Optional: true},
		},
	}
}

// Event is one thing that happened to one issue.
type Event struct {
	Kind   string
	At     string
	Issue  string
	Author User

	// Field, From and To are set on a transition or a field change. HasFrom
	// and HasTo distinguish a side the server did not send from one it sent
	// empty, exactly as Change does.
	Field   string
	From    string
	FromID  string
	HasFrom bool
	To      string
	ToID    string
	HasTo   bool

	// Body is a comment's text and BodyFormat the markup it is in.
	Body       string
	BodyFormat string

	// TimeSpent is a worklog's duration as the server words it, and Seconds
	// the same duration resolved.
	TimeSpent string
	Seconds   int
}

// Node renders one event.
func (e Event) Node() *render.Node {
	n := render.El("event").
		Attr("kind", e.Kind).
		Attr("issue", e.Issue).
		AttrIf("field", e.Field)

	n.Leaf("at", e.At)
	n.Child(render.El("author").
		AttrIf("id", e.Author.ID).
		Attr("display", e.Author.Display))

	if e.HasFrom {
		n.Child(render.El("from").AttrIf("id", e.FromID).SetText(e.From))
	}
	if e.HasTo {
		n.Child(render.El("to").AttrIf("id", e.ToID).SetText(e.To))
	}
	if e.Body != "" {
		n.Child(render.El("body").Attr("format", e.BodyFormat).SetCDATA(e.Body))
	}
	if e.TimeSpent != "" {
		n.Child(render.El("time-spent").
			Attr("seconds", strconv.Itoa(e.Seconds)).
			SetText(e.TimeSpent))
	}
	return n
}

// ActivityColumns is the default TSV column set.
//
// Every column is a value and none of them is a rendering: a row of one kind
// leaves the columns the other kinds use empty, which is what "this event has
// no such part" looks like in a format with no way to omit a cell. The two
// unbounded columns, a comment body and a changed value, are last.
func ActivityColumns() []render.Column {
	return []render.Column{
		{Header: "at", Path: "at"},
		{Header: "issue", Path: "@issue"},
		{Header: "kind", Path: "@kind"},
		{Header: "author", Path: "author@display"},
		{Header: "field", Path: "@field"},
		{Header: "time-spent", Path: "time-spent"},
		{Header: "from", Path: "from"},
		{Header: "to", Path: "to"},
		{Header: "body", Path: "body"},
	}
}

// eventsFrom turns one issue's projections into events.
//
// Transitions and field changes are the same source and differ only in which
// field moved, so `status` becomes a transition and everything else a field
// change. That is a rendering decision made once here rather than by every
// consumer writing `if field == "status"`.
func eventsFrom(i Issue) []Event {
	var out []Event
	for _, c := range i.Thread {
		out = append(out, Event{
			Kind: EventComment, At: c.Created, Issue: i.Key, Author: c.Author,
			Body: c.Body, BodyFormat: c.BodyFormat,
		})
	}
	for _, w := range i.Work {
		out = append(out, Event{
			Kind: EventWorklog, At: w.Started, Issue: i.Key, Author: w.Author,
			TimeSpent: w.TimeSpent, Seconds: w.Seconds,
			Body: w.Comment, BodyFormat: w.BodyFormat,
		})
	}
	for _, c := range i.Changes {
		kind := EventField
		if strings.EqualFold(c.Field, "status") {
			kind = EventTransition
		}
		out = append(out, Event{
			Kind: kind, At: c.Created, Issue: i.Key, Author: c.Author,
			Field: c.Field,
			From:  c.From, FromID: c.FromID, HasFrom: c.HasFrom,
			To: c.To, ToID: c.ToID, HasTo: c.HasTo,
		})
	}
	return out
}

// sortEvents orders a batch newest first, tie-broken by issue key and kind so
// two runs over the same data produce the same rows in the same order.
//
// **The server's order is not a contract and cannot be inherited.** Measured
// 2026-08-12: Cloud returns one issue's changelog oldest-first from
// /issue/{key}/changelog and newest-first from the same data under
// expand=changelog on the search, and Data Center returns it oldest-first from
// the projection. Three orderings across two deployments and one feature, so
// this sorts what it was given and never trusts arrival order.
func sortEvents(events []Event) {
	sort.SliceStable(events, func(a, b int) bool {
		x, y := events[a], events[b]
		if x.At != y.At {
			return x.At > y.At
		}
		if x.Issue != y.Issue {
			return x.Issue < y.Issue
		}
		return x.Kind < y.Kind
	})
}

// Activity flag names.
const (
	sinceFlag = "since"
	userFlag  = "user"
	kindFlag  = "kind"
)

func activityCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "activity"},
		Summary: "List what happened across issues, as events rather than rows",
		Description: strings.TrimSpace(`
Merges four sources into one time-ordered feed: comments, transitions, other
field changes, and worklogs. Newest first.

This is the question the filters on ` + "`issue list`" + ` each answer part of.
--involving finds issues somebody touched; --changed-by finds issues whose one
named field they moved. Neither says what was done, and assembling that by hand
is where the answer stops being checkable.

--since is required and bounds the candidate set, because a feed with no time
bound is a sweep of every issue the credential can see. It bounds each event as
well, which is a second job and the one this command is for: an issue updated
yesterday holds comments from years ago, and reporting them because their issue
matched answers a question about issues while claiming to answer one about
events. An absolute date is read in the Jira account's timezone, which is what
Jira reads it in and costs one request to learn; a relative offset names an
instant and costs nothing; a date function is refused, because computing one
here would substitute this client's notion of a boundary for the server's.

**Where the comment half comes from.** Comment authorship is not searchable in
JQL on either deployment, so comments are matched here rather than by the
server, over the issues --since selected. That set is exact for a question about
a window: adding a comment moves the issue's own updated timestamp, so an issue
somebody commented on inside the window is inside the window, whatever else was
or was not done to it.

The servers also bound what they inline, differently and in different
directions. Cloud returns the newest 20 comments of a longer thread and Data
Center returns all of them; both return the *oldest* 20 worklogs, which for a
feed about recent work is the wrong twenty, so an issue with more than that
costs one further request to read them properly. Anything still clipped is
reported: the run exits 3 and the rows say which issue and which source.

Exit 3 is sharper than it looks when --user is given. It means some events were
not sent, so it also means this person may have events here that you cannot
see — a comment of theirs can sit outside the twenty Cloud returned. An empty
feed that exits 3 is not the same answer as an empty feed that exits 0.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue activity --since -7d",
			buildinfo.App + " issue activity --since -7d --user ada",
			buildinfo.App + " issue activity --since -1d --kind transition --format json",
		}, "\n"),
		Flags: []registry.Flag{{
			Name: sinceFlag, Type: registry.TypeString,
			Usage: "only events at or after this date or offset, e.g. -7d; " +
				"required, and it bounds the issues searched as well as the " +
				"events reported; a date function like startOfWeek() is " +
				"refused here, because this command compares dates itself",
			Required: true,
		}, {
			Name: userFlag, Type: registry.TypeString,
			Usage: "only events by this person, by display name, email, or id; " +
				"the word currentUser resolves to the caller",
		}, {
			Name: kindFlag, Type: registry.TypeString, Repeatable: true,
			Usage: "only events of this kind: comment, transition, field, or " +
				"worklog; repeat for several",
		}, {
			Name: "jql", Type: registry.TypeString,
			Usage: "raw JQL narrowing the issues searched, combined with " +
				"--since and always parenthesized",
		}, rawBodyFlag(), {
			Name: "page-size", Type: registry.TypeInt,
			Usage: "issues per HTTP request, 1 to 100; transport tuning only",
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "events",
		Columns:        ActivityColumns(),
		Outputs: []registry.Output{
			{Kind: KindActivity, Version: VersionActivity},
		},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Usage, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: validateActivity,
		Stream:   runActivity,
	}
}

// validateActivity refuses what cannot be honored before a byte reaches stdout,
// which for a streaming command is every check it has.
func validateActivity(ctx context.Context, inv *registry.Invocation) error {
	// The raw fragment first, so this command answers a bad --jql with the same
	// code every other command that takes one answers with. Ordering the checks
	// by which flag is most likely to be wrong would make the verdict depend on
	// which command a caller happened to use.
	if raw := inv.Flags.String("jql"); raw != "" {
		if err := jql.ValidateFragment(raw); err != nil {
			return err
		}
	}
	for _, k := range inv.Flags.StringSlice(kindFlag) {
		if !isEventKind(k) {
			return errs.Usage("UNKNOWN_EVENT_KIND",
				"%q is not an event kind", k).
				WithDetail("the kinds are %s", strings.Join(EventKinds(), ", ")).
				WithRemedy("pass one of them, or drop --kind for all four")
		}
	}
	since := inv.Flags.String(sinceFlag)
	if _, err := jql.ParseDate(since); err != nil {
		return err
	}
	if err := resolveActivityCutoff(ctx, inv, since); err != nil {
		return err
	}
	if err := resolveActivityUser(ctx, inv); err != nil {
		return err
	}
	// The same server-side verdict issue list gets. Both send the fragment, so
	// both refuse a query Jira would answer with a confident empty result;
	// getting one of them is worse than getting neither, because a caller would
	// have to know which command checks.
	if err := refuseQueryJiraDoesNotUnderstand(ctx, inv); err != nil {
		return err
	}
	return requirePageSize(inv)
}

// resolveActivityCutoff turns --since into the instant the event filter uses,
// and refuses what it cannot turn into one.
//
// The refusal is the point. --since does two jobs: it goes to the server as
// `updated >=`, which bounds the *issues*, and it bounds each *event* here.
// This command emits events, and an issue updated yesterday holds comments from
// years ago, so a --since that reaches only the first job answers a question
// about issues while claiming to answer one about events. That is what it did
// for four of the seven forms it accepted, at exit 0 and complete="true".
func resolveActivityCutoff(
	ctx context.Context, inv *registry.Invocation, since string,
) error {
	var loc *time.Location
	switch jql.ClassifyDate(since) {
	case jql.DateFunction:
		// Resolving one here means computing it, and computing it means
		// substituting this client's notion of a boundary for Jira's:
		// startOfWeek() carries the server's idea of which day a week starts
		// on. docs/output-contract.md says dates are passed through for that
		// reason, and this command is the one that also has to compare them.
		// So the honest answer is to decline the combination rather than to
		// bound the issues and leave the events unbounded.
		return errs.Usage("UNBOUNDABLE_DATE",
			"--%s cannot take a date function on this command", sinceFlag).
			WithDetail("%s filters events in this process, and computing %s "+
				"here would substitute this client's boundary for Jira's",
				strings.Join([]string{buildinfo.App, "issue", "activity"}, " "),
				strings.TrimSpace(since)).
			WithRemedy("use an absolute date like 2026-08-10, or a relative " +
				"offset like -7d")
	case jql.DateAbsolute:
		// An absolute literal is a wall clock, and Jira reads it in the
		// account's zone. Only this form needs the request.
		resolved, err := accountLocation(ctx, inv)
		if err != nil {
			return err
		}
		loc = resolved
	case jql.DateRelative, jql.DateInvalid:
		// An offset names an instant, which is the same in every zone, so this
		// form costs no request. DateInvalid cannot arrive: ParseDate refused
		// it above, and ActivityCutoff reports it rather than guessing.
	}

	cutoff := ActivityCutoff(since, loc, time.Now())
	if cutoff == "" {
		return errs.Usage("UNBOUNDABLE_DATE",
			"--%s cannot be resolved to an instant", sinceFlag).
			WithDetail("input: %s", strings.TrimSpace(since)).
			WithRemedy("use an absolute date like 2026-08-10, or a relative " +
				"offset like -7d")
	}
	inv.SetValue(activitySinceKey, cutoff)
	return nil
}

// accountLocation reads the timezone Jira evaluates this caller's dates in.
//
// One GET to /myself, and only for the forms that need it. Both deployments
// have been recorded sending a zone, so an account without one is a refusal
// rather than a fallback.
func accountLocation(
	ctx context.Context, inv *registry.Invocation,
) (*time.Location, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION",
			"--%s cannot be resolved without a connection to Jira", sinceFlag)
	}
	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	me, err := site.Whoami(ctx, meta.Client, meta.Info)
	if err != nil {
		return nil, err
	}
	return me.Location()
}

func isEventKind(k string) bool {
	for _, known := range EventKinds() {
		if k == known {
			return true
		}
	}
	return false
}

// activityUserKey is where the resolved --user is left for the body.
const activityUserKey = "issue.activity.user"

// resolveActivityUser turns --user into an identity the feed can match events
// against.
//
// Unlike the filters on `issue list`, this one cannot become a JQL clause: three
// of the four event kinds are matched here, in this process, against what the
// projections returned. So `currentUser` has to become an actual account rather
// than staying a function the server would have evaluated.
func resolveActivityUser(ctx context.Context, inv *registry.Invocation) error {
	value := strings.TrimSpace(inv.Flags.String(userFlag))
	if value == "" {
		return nil
	}
	if inv.Jira == nil {
		return errs.Runtime("NO_SESSION",
			"--%s cannot be resolved without a connection to Jira", userFlag)
	}
	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return err
	}
	if isCurrentUser(value) {
		me, err := site.Whoami(ctx, meta.Client, meta.Info)
		if err != nil {
			return err
		}
		inv.SetValue(activityUserKey, site.User{ID: me.ID, Display: me.Display})
		return nil
	}
	user, err := meta.ResolveUser(ctx, value)
	if err != nil {
		return err
	}
	inv.SetValue(activityUserKey, user)
	return nil
}

// byUser reports whether an event is this person's.
//
// Matched on the id where both sides have one, and on the display name
// otherwise. Data Center's changelog author carries a username and Cloud's an
// accountId, and both carry a display name, so the fallback is what keeps the
// filter working where the id spellings differ.
func byUser(e Event, want site.User) bool {
	if want.ID != "" && e.Author.ID != "" {
		return strings.EqualFold(want.ID, e.Author.ID)
	}
	return want.Display != "" && strings.EqualFold(want.Display, e.Author.Display)
}

func runActivity(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	if inv.Jira == nil {
		return registry.StreamResult{},
			errs.Runtime("NO_SESSION", "issue activity has no connection to Jira")
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
	want := activityFilter(inv)

	var clipped, atLimit bool
	result, err := client.ListStream(ctx, ListOptions{
		Query: QueryOptions{
			Project:      inv.Jira.Project(),
			JQL:          inv.Flags.String("jql"),
			UpdatedAfter: inv.Flags.String(sinceFlag),
		},
		Limit:    registry.Limit{All: true},
		PageSize: pageSize,
		Fields:   DefaultFields(),
		// The three projections this feed is made of, on one request per page.
		WithComments:  want.kinds[EventComment],
		WithWorklogs:  want.kinds[EventWorklog],
		WithChangelog: want.kinds[EventTransition] || want.kinds[EventField],
	}, func(page []Issue, total int) error {
		events, short, err := eventsForPage(ctx, client, page, want)
		if err != nil {
			return err
		}
		if short {
			clipped = true
		}
		sortEvents(events)
		// Bounded by --limit like any other collection. The issue search is
		// deliberately unbounded — every candidate has to be read before its
		// events can be merged and sorted — so this is the only place the
		// caller's limit can apply, and without it the flag was declared,
		// bound, and dropped.
		bounded, err := writeRows(inv, out, events,
			func(e Event) *render.Node { return e.Node() })
		if err != nil {
			return err
		}
		if bounded {
			atLimit = true
			return errStopPaging
		}
		inv.Progress.Update(out.Count(), total)
		return nil
	})
	if err != nil && !errors.Is(err, errStopPaging) {
		return registry.StreamResult{}, err
	}
	switch {
	case atLimit:
		// Bounded by the caller. There is no resume token: an event feed is
		// merged and sorted from three projections across a page of issues,
		// and an offset into the result would not describe a place any request
		// can start from.
		return registry.StreamResult{Complete: false}, nil
	case clipped:
		return registry.StreamResult{Complete: false, PartialElement: "event"}, nil
	}
	return registry.StreamResult{Complete: result.Complete}, nil
}

// errStopPaging ends the page loop once the caller's limit is reached, so a
// bounded feed stops fetching rather than reading every candidate and throwing
// the rest away.
var errStopPaging = errors.New("stop paging")

// eventsForPage turns one page of issues into the events the caller asked for,
// topping up the worklogs that the projection cut off.
//
// It reports whether anything is still missing after that. Comments are the one
// source with no second request available here — `issue comment list` is the
// command for that — so a clipped Cloud thread makes the run incomplete rather
// than costing another round trip per issue.
func eventsForPage(
	ctx context.Context, client *Client, page []Issue, want activityWant,
) ([]Event, bool, error) {
	var short bool
	var events []Event
	for _, i := range page {
		clipped, err := topUpWorklogs(ctx, client, &i)
		if err != nil {
			return nil, false, err
		}
		if clipped || (i.HasThread && !i.ThreadComplete()) {
			short = true
		}
		for _, e := range eventsFrom(i) {
			if want.accepts(e) {
				events = append(events, e)
			}
		}
	}
	return events, short, nil
}

// topUpWorklogs refetches an issue's worklogs when the projection cut them off,
// and reports whether they are still short afterwards.
//
// Both deployments inline the *oldest* twenty, which for a feed about what
// happened lately is precisely the wrong twenty. This is one request per issue
// that has more than twenty, and none at all for the rest: the projection has
// already answered for them.
func topUpWorklogs(ctx context.Context, client *Client, i *Issue) (bool, error) {
	if !i.HasWork || i.WorkComplete() {
		return false, nil
	}
	topped, err := client.ListWorklogs(ctx, i.Key, 0, MaxPageSize)
	if err != nil {
		return false, err
	}
	i.Work = topped.Worklogs
	// Still short unless the server said how many there are and this has them
	// all. A server that reported no count has not said the feed is whole, and
	// a feed that quietly drops entries is the one thing this command must not
	// produce.
	return !exhausted(len(i.Work), topped.Total), nil
}

// activityWant is what the caller asked to see.
type activityWant struct {
	kinds map[string]bool
	user  site.User
	since string
}

// accepts reports whether one event passes the filters.
//
// The `since` bound is applied to the *event* and not only to the issue: an
// issue updated yesterday can hold a comment from last year, and a feed that
// reported it because its issue matched would be answering a question about
// issues while claiming to answer one about events.
func (w activityWant) accepts(e Event) bool {
	if !w.kinds[e.Kind] {
		return false
	}
	if w.since != "" && e.At < w.since {
		return false
	}
	if w.user.ID == "" && w.user.Display == "" {
		return true
	}
	return byUser(e, w.user)
}

// activityFilter reads the flags into the filter the body applies.
func activityFilter(inv *registry.Invocation) activityWant {
	kinds := map[string]bool{}
	asked := inv.Flags.StringSlice(kindFlag)
	if len(asked) == 0 {
		asked = EventKinds()
	}
	for _, k := range asked {
		kinds[k] = true
	}
	user, _ := inv.Value(activityUserKey).(site.User)
	since, _ := inv.Value(activitySinceKey).(string)
	return activityWant{kinds: kinds, user: user, since: since}
}

// activitySinceKey is where the resolved --since instant is left for the body.
const activitySinceKey = "issue.activity.since"

// ActivityCutoff turns --since into the instant an event is compared against.
//
// Exported for the test that holds it to the set `jql.ParseDate` accepts,
// because getting this wrong shows up as a feed that is quietly too wide or too
// narrow rather than as a failure. It was wrong for exactly that reason: this
// carried its own list of two layouts against the four `jql` accepts, and the
// two it could not read resolved to "no bound at all".
//
// `jql.ParseDate` settles whether the input is *valid* and what the query
// should carry; this settles what the client-side filter compares to, which is
// a separate job because three of the four kinds are filtered here rather than
// by the server. Both now read the same enumeration, so a form cannot be
// accepted in one and unresolvable in the other.
//
// loc is the timezone on the Jira account's profile. An absolute literal is a
// wall clock and Jira reads it in that zone, so reading it here in UTC is wrong
// by the offset: five hours of over-reporting on America/Chicago, and nine
// hours of silently dropped events on Asia/Tokyo.
//
// An empty return means the input names no instant this process can compute,
// which is a refusal at the caller and never an unbounded filter.
func ActivityCutoff(input string, loc *time.Location, now time.Time) string {
	t, ok := jql.ResolveDate(input, loc, now)
	if !ok {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
