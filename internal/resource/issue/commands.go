package issue

import (
	"context"
	"slices"
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

func init() {
	registry.Register(listCommand())
	registry.Register(getCommand())

	render.RegisterSchema(KindList, Schema())
	render.RegisterSchema(KindGet, GetSchema())
}

// Schema is the shape of an issue, as `jr contract` reports it and as
// render.Doc.Validate holds every emitted issue to.
func Schema() *render.Schema {
	return &render.Schema{
		Element: "issue",
		Attrs: []render.Field{
			{Name: "key", Type: render.TypeString},
			{Name: "id", Type: render.TypeString},
			{Name: "type", Type: render.TypeString, Optional: true},
			{Name: "priority", Type: render.TypeString, Optional: true},
			{Name: "project", Type: render.TypeString, Optional: true},
			{Name: "resolution", Type: render.TypeString, Optional: true},
			{Name: "parent", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.Leaf("summary", render.TypeString)},
			{Schema: &render.Schema{
				Element: "status",
				Attrs: []render.Field{{
					// A project can rename a status to anything; the category
					// stays one of four values, which is what anything
					// automated should branch on.
					Name: "category", Type: render.TypeString,
					Enum: []string{
						site.CategoryToDo, site.CategoryInProgress,
						site.CategoryDone, site.CategoryUnknown,
					},
				}},
				Text: &render.Field{Type: render.TypeString},
			}},
			{Schema: &render.Schema{
				// Always present, and empty when nobody is assigned: absent and
				// unassigned are different facts and this kind reports both.
				Element: "assignee",
				Attrs: []render.Field{
					{Name: "id", Type: render.TypeString},
					{Name: "display", Type: render.TypeString},
				},
			}},
			{Schema: &render.Schema{
				// Fetched on every request since the beginning and rendered
				// nowhere until v4, so `--reporter` could filter on a value no
				// output could show. Same shape as the assignee, and always
				// present for the same reason.
				Element: "reporter",
				Attrs: []render.Field{
					{Name: "id", Type: render.TypeString},
					{Name: "display", Type: render.TypeString},
				},
			}},
			{Schema: render.Leaf("created", render.TypeTimestamp), Optional: true},
			{Schema: render.Leaf("updated", render.TypeTimestamp), Optional: true},
			// Present only with --url. It is not a field Jira sends: it is
			// built here from the deployment's baseUrl, because Jira's own
			// `self` is the REST endpoint and opens JSON rather than an issue.
			{Schema: render.Leaf("url", render.TypeString), Optional: true},
			// Present only with --age, and never instead of `updated`. It is a
			// rendering of how long ago that timestamp was — "3 hours",
			// "14 days" — so it is a string and not a duration: it is for
			// reading, and the instant it derives from is right beside it.
			{Schema: render.Leaf("age", render.TypeString), Optional: true},
			{Schema: &render.Schema{
				// Mixed content in CDATA, with the markup named rather than
				// guessed at: wiki on Data Center, adf on Cloud.
				Element: "description",
				Attrs: []render.Field{{
					Name: "format", Type: render.TypeString,
					Enum: []string{BodyWiki, BodyADF, BodyMarkdown},
				}},
				Text: &render.Field{Type: render.TypeString},
			}, Optional: true},
			{Schema: render.ListSchema("labels", "label",
				render.Leaf("label", render.TypeString))},
			{Schema: render.ListSchema("components", "component",
				render.Leaf("component", render.TypeString)), Optional: true},
			{Schema: render.ListSchema("fix-versions", "fix-version",
				render.Leaf("fix-version", render.TypeString)), Optional: true},
			// Present only with --with-comments, on either command.
			//
			// This lived on GetSchema alone until `issue list --with-comments`
			// existed, and the reason was exact rather than cautious: a row
			// could not carry a thread, so declaring that it could was a lie
			// `make golden` caught. A row can now, and the two kinds share this
			// shape for the same reason they share every other part of it — a
			// listed issue and a fetched one parse identically.
			{Schema: commentsSchema(), Optional: true},
		},
		Extra: &render.Extra{
			Named: "the id of a field requested with --field, e.g. customfield_10042",
			Type:  render.TypeString,
		},
	}
}

// GetSchema is the issue shape plus what only `issue get` can carry.
//
// `issue list` and `issue get` deliberately share `Schema()` — a row and a
// record parse identically, and get "simply has more of it filled in". Adding
// the comment thread to the shared schema said that a *list row* could carry
// one, which is false, and changed the shape of `issue.list` without changing
// its version. `make golden` refused it, which is the gate doing exactly its
// job: one edit, two kinds, and only one of them meant.
//
// So get gets a superset instead. Everything list has, plus one element list
// cannot have, which keeps the shared-shape promise true in the direction it
// was actually making.
func GetSchema() *render.Schema {
	s := Schema()
	s.Attrs = append(s.Attrs, render.Field{
		// The baseline `issue edit --if-unchanged` compares against. It is on
		// get and not on the shared shape because a baseline you did not read
		// is not a baseline: a caller holding one has, by construction, fetched
		// the issue. Putting it on a row as well would also mean sixty-odd
		// bytes on every row of every listing to serve the one caller in a
		// hundred who is about to write.
		//
		// Optional because an issue Jira reports no `updated` for cannot be
		// given one, and saying so by absence beats a token that matches
		// anything.
		Name: "precondition", Type: render.TypeString, Optional: true,
	})
	return s
}

// commentsSchema is the thread an issue carries with --with-comments.
//
// ListSchema plus one attribute. `complete` is not part of ListSchema because
// most containers cannot be partial — labels and components arrive whole with
// the issue — and declaring it on all of them would invite a consumer to check
// a value that is always true.
func commentsSchema() *render.Schema {
	s := render.ListSchema("comments", "comment", CommentSchema())
	s.Attrs = append(s.Attrs,
		// The server's count of the whole thread, beside `count`, which is how
		// many arrived. Two numbers rather than one because `complete` says
		// whether they agree and never says by how much, and a caller deciding
		// whether to spend a second request on `issue comment list` needs the
		// size of what it would fetch.
		//
		// Optional because a server that sent no count has not told this tool
		// how long the thread is, and neither path that fills this container can
		// ask again: the record fetches one bounded page by design, and the row
		// gets its thread as a projection inside a search response. An absent
		// `total` means unknown, it always comes with complete="false", and it
		// is the reason to spend that second request rather than a reason not
		// to. Writing zero there instead was published for two releases.
		render.Field{Name: "total", Type: render.TypeInt, Optional: true},
		render.Field{Name: render.CompleteAttr, Type: render.TypeBool},
		// Where in the thread the first returned comment sits. Present only
		// when it is not zero, which in practice means a Cloud search
		// projection: that caps at twenty and returns the *newest* twenty, so
		// a 25-comment issue arrives as comments 6 to 25. Data Center returns
		// the thread from the oldest and omits this.
		render.Field{Name: "start-at", Type: render.TypeInt, Optional: true},
	)
	return s
}

func listCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "list"},
		Summary: "List issues matching a query",
		Description: strings.TrimSpace(`
Builds a JQL query from the flags, or takes one whole with --jql, and returns
the matching issues.

--limit says how many results you want and is not capped: the client pages
until it has them. --page-size tunes the transport and is rarely worth setting.
There is deliberately no offset flag — Cloud pages by cursor, so an offset
could not be honored, and --page-token is opaque precisely so the same flag
works against both deployments.

A result cut short by --limit or by --max-requests is never reported as
complete. It exits 3, says so on stderr, and carries a token to resume from.

Raw JQL from --jql is always parenthesized before being combined with the other
filters, so an OR inside it cannot escape the project scope. A fragment whose
own parentheses do not balance is refused rather than sent, because wrapping
contains only a fragment that balances.

Results come back ordered by issue key descending unless --sort names a field.
That is close to creation order and is not update order: a date filter narrows
the set and never orders it, so "everything touched today, newest first" is
--updated-after -1d --sort updated --order desc.

Every list filter has both directions: --status and --not-status, --type and
--not-type, --label and --not-label. Each repeats, so --not-status Done
--not-status Closed sends status NOT IN (Done, Closed).

None of them splits on commas. --label a,b asks for one label whose name
contains a comma, which Jira genuinely stores, so splitting it would make that
label unaskable. What differs is what the server does with the mistake, and it
differs by field: Jira validates a status and an issue type, so a comma typo
there comes back as a 400 naming the value. It does not validate a label at
all. Repeat the flag rather than joining values with a comma.

A label filter is checked here instead, because a label nothing carries is a
legal question and an empty answer to it looks exactly like an empty answer to
a correct one. A label no issue on this site carries produces the warning
UNKNOWN_LABEL on stderr, and the query still runs and still exits 0: asking
about a label nobody uses is allowed, and not being able to tell that is what
was not. It costs one request per label, it is not cached, and a site that
cannot answer is left alone rather than guessed at. It says nothing about
whether a label that does exist will match here: this query may be scoped to
one project and a label lives site-wide.

--assignee and --reporter ask who an issue belongs to. --involving,
--was-assignee, --worklog-author, and --changed-by ask who touched it, which is
a different question: --updated-after means somebody updated the issue, not
that you did.

Two limits are worth knowing rather than discovering. JQL cannot search comment
authorship at all, so nothing here finds "issues I commented on" and --involving
says so rather than approximating it. And CHANGED names one field at a time —
there is no way to ask whether any field changed — so --changed-field defaults
to status and everything else has to be asked for.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue list --project ENG --status 'In Progress'",
			buildinfo.App + " issue list --jql 'labels IN (retry, transport)' --limit all",
			buildinfo.App + " issue list --assignee currentUser --sort updated --order desc",
			buildinfo.App + " issue list --not-status Done --not-status Closed --updated-after -1d",
			buildinfo.App + " issue list --involving currentUser --updated-after -7d",
			buildinfo.App + " issue list --changed-by currentUser --changed-after -1w",
		}, "\n"),
		Flags: []registry.Flag{
			{
				Name: "jql", Type: registry.TypeString,
				Usage: "raw JQL, combined with the other filters and always parenthesized",
			},
			{
				Name: "status", Type: registry.TypeString, Repeatable: true,
				Usage: "status to match; repeat for several, never comma-joined",
			},
			{
				Name: "not-status", Type: registry.TypeString, Repeatable: true,
				Usage: "status to exclude; repeat for several, never comma-joined",
			},
			{
				Name: "label", Type: registry.TypeString, Repeatable: true,
				Usage: "label to match; repeat for several. A comma is part of " +
					"the label, and one no issue carries is warned about",
			},
			{
				Name: "not-label", Type: registry.TypeString, Repeatable: true,
				Usage: "label to exclude; repeat for several. A comma is part of " +
					"the label, and one no issue carries is warned about",
			},
			{
				Name: "type", Short: "t", Type: registry.TypeString, Repeatable: true,
				Usage: "issue type to match; repeat for several, never comma-joined",
			},
			{
				Name: "not-type", Type: registry.TypeString, Repeatable: true,
				Usage: "issue type to exclude; repeat for several, never comma-joined",
			},
			{
				Name: "assignee", Short: "a", Type: registry.TypeString,
				Usage: "assignee, by display name, email, or id; " +
					"the word currentUser resolves to the caller",
			},
			{
				Name: "reporter", Type: registry.TypeString,
				Usage: "reporter, by display name, email, or id; " +
					"the word currentUser resolves to the caller",
			},
			{
				Name: "creator", Type: registry.TypeString,
				Usage: "who filed the issue, which unlike the reporter cannot " +
					"be changed afterwards",
			},
			{
				Name: "involving", Type: registry.TypeString,
				Usage: "one person across any of " +
					strings.Join(InvolvingFields, ", ") +
					"; comment authorship is not searchable in JQL, " +
					"so it is not included",
			},
			{
				Name: "watcher", Type: registry.TypeString,
				Usage: "who is watching; Jira allows this for yourself only, " +
					"unless you can manage watchers",
			},
			{
				Name: "voter", Type: registry.TypeString,
				Usage: "who voted; Jira allows this for yourself only, " +
					"unless you can view voters",
			},
			{
				Name: "worklog-author", Type: registry.TypeString,
				Usage: "who logged work against the issue",
			},
			{
				Name: "was-assignee", Type: registry.TypeString,
				Usage: "who the issue was assigned to at any point, whoever holds it now",
			},
			{
				Name: "changed-by", Type: registry.TypeString,
				Usage: "who changed --changed-field",
			},
			{
				Name: "changed-field", Type: registry.TypeString,
				Default: DefaultChangedField,
				Usage: "the field --changed-by and --changed-after apply to; " +
					"JQL cannot ask whether any field changed, only a named one",
			},
			{
				Name: "created-after", Type: registry.TypeString,
				Usage: "only issues created on or after this date or offset, e.g. -7d; " +
					"every date on this command is evaluated in the Jira " +
					"account's timezone, which " + buildinfo.App + " user me reports",
			},
			{
				Name: "created-before", Type: registry.TypeString,
				Usage: "only issues created on or before this date or offset",
			},
			{
				Name: "updated-after", Type: registry.TypeString,
				Usage: "only issues updated on or after this date or offset; " +
					"updated by anyone, which is not the same as updated by you",
			},
			{
				Name: "updated-before", Type: registry.TypeString,
				Usage: "only issues updated on or before this date or offset",
			},
			{
				Name: "changed-after", Type: registry.TypeString,
				Usage: "only issues whose --changed-field changed on or after this date",
			},
			{
				Name: "changed-before", Type: registry.TypeString,
				Usage: "only issues whose --changed-field changed on or before this date",
			},
			{
				Name: "worklog-after", Type: registry.TypeString,
				Usage: "only issues with work logged on or after this date",
			},
			{
				Name: "worklog-before", Type: registry.TypeString,
				Usage: "only issues with work logged on or before this date",
			},
			{
				Name: "sort", Short: "s", Type: registry.TypeString,
				Usage: "field to sort by, e.g. updated; " +
					"results are ordered by issue key when this is not given, " +
					"which is close to creation order and not update order",
			},
			{
				Name: "order", Short: "o", Type: registry.TypeEnum,
				Enum: []string{"asc", "desc"},
				// No declared default, because there is no single one: the key
				// ordering nobody asked for descends and a named --sort
				// ascends. Declaring "asc" printed a default that the common
				// invocation did not use, next to a flag that was then ignored.
				Usage: "sort direction; applies to --sort, or to the issue key " +
					"ordering when there is no --sort " +
					"(default desc for the key, asc for a named field)",
			},
			fieldFlag(),
			contextFieldsFlag(),
			urlFlag(),
			ageFlag(),
			{
				Name: withCommentsFlag, Type: registry.TypeBool,
				Usage: "include each issue's comment thread, in the request " +
					"the page already costs; what arrives differs by " +
					"deployment, so read the count and start-at a row reports " +
					"rather than assuming you have the whole conversation",
			},
			{
				Name: "all-projects", Type: registry.TypeBool,
				Usage: "search every project the credential can see, ignoring " +
					"the context's; required to exhaust an unfiltered query",
			},
			{
				Name: "page-size", Type: registry.TypeInt,
				Usage: "results per HTTP request, 1 to 100; transport tuning only",
			},
			{
				Name: "page-token", Type: registry.TypeString,
				Usage: "resume from a next-page-token returned by a previous run",
			},
		},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "issues",
		Columns:        ListColumns(),
		ColumnsFor:     listColumnsFor,
		Validate:       validateList,
		Outputs:        []registry.Output{{Kind: KindList, Version: VersionList}},
		ExitCodes: []exitcode.Code{
			// Usage covers an unresolvable --assignee as well as a bad flag.
			exitcode.Partial, exitcode.Usage, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Stream: runList,
	}
}

// runList streams matching issues.
//
// Rows go out as each page arrives rather than after the last request, so a
// long run produces output immediately, a downstream `head` can stop it early,
// and an interrupt leaves the caller with what was fetched.
func runList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	if inv.Jira == nil {
		return registry.StreamResult{},
			errs.Runtime("NO_SESSION", "issue list has no connection to Jira")
	}

	query := listQuery(inv)

	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return registry.StreamResult{}, err
	}
	client := &Client{Transport: conn, Site: info}

	// A thread the server clipped makes the whole run incomplete. The caller
	// asked for the comments; some of them are missing; "complete" would be
	// false in the only sense the word has here. It is recorded across pages
	// rather than per row because the envelope has one attribute and TSV has
	// none at all — exit 3 and the stderr warning are what a script sees, and
	// the per-row `count` against `total` says which rows to look at.
	var clipped bool

	result, err := client.ListStream(ctx, ListOptions{
		Query:        query,
		Limit:        inv.Limit,
		PageSize:     inv.Flags.Int("page-size"),
		PageToken:    inv.Flags.String("page-token"),
		Fields:       RequestedFields(resolvedFields(inv)),
		WithComments: inv.Flags.Bool(withCommentsFlag),
	}, func(page []Issue, total int) error {
		if anyThreadClipped(page) {
			clipped = true
		}
		if err := stampPage(inv, info, page); err != nil {
			return err
		}
		nodes := make([]*render.Node, 0, len(page))
		for _, i := range page {
			nodes = append(nodes, i.Node())
		}
		if err := out.Write(nodes...); err != nil {
			return err
		}
		// The server discloses the total on the first page, so a long run can
		// say how long it will be from its first second rather than never.
		inv.Progress.Update(out.Count(), total)
		return nil
	})
	if err != nil {
		return registry.StreamResult{}, err
	}

	if clipped {
		// No token, deliberately. A resume token continues the *result set*,
		// and the result set was exhausted — what is missing is inside a row,
		// and the way to get it is `issue comment list` on that key. Close
		// refuses complete plus a token; this is the other pairing, incomplete
		// with nothing to resume, which the contract already documents from
		// `issue history` against Data Center.
		return registry.StreamResult{Complete: false, PartialElement: "comments"}, nil
	}
	return registry.StreamResult{
		Complete:      result.Complete,
		NextPageToken: result.NextPageToken,
	}, nil
}

// anyThreadClipped reports whether the server sent part of a comment thread on
// any row of this page.
func anyThreadClipped(page []Issue) bool {
	for _, i := range page {
		if i.HasThread && !i.ThreadComplete() {
			return true
		}
	}
	return false
}

// stampPage applies the two flags that derive a column from what already
// arrived rather than asking Jira for more.
func stampPage(inv *registry.Invocation, info site.Info, page []Issue) error {
	if inv.Flags.Bool(urlFlagName) {
		if err := stampURLs(page, info); err != nil {
			return err
		}
	}
	if inv.Flags.Bool(ageFlagName) {
		// One instant for the whole page, so two rows a slow page apart are
		// not reported as different ages for that reason alone.
		stampAges(page, time.Now())
	}
	return nil
}

// RequestedFields returns what to ask Jira for.
//
// --field is additive. Replacing the default set instead would mean
// `--field customfield_10042` silently blanked the status and assignee columns,
// which looks like every issue is unassigned rather than like a flag that
// narrowed the request.
func RequestedFields(resolved []string) []string {
	out := DefaultFields()
	have := map[string]bool{}
	for _, id := range out {
		have[id] = true
	}
	// Every resolved id that is not already being asked for, native or not.
	//
	// ExtraFieldNames is the wrong question here and was being used for it:
	// that answers "which ids need an element of their own", so it drops the
	// natives — and five of them, description, resolution, parent, components
	// and fixVersions, are modelled by this package and *not* in DefaultFields,
	// because a listing does not fetch them. Naming one produced a column over
	// a field nobody had asked Jira for, and an always-empty cell. Which ids
	// need an element and which need fetching are two questions with two
	// answers, which is the same conflation that made --field a no-op one layer
	// along.
	for _, id := range resolved {
		if have[id] {
			continue
		}
		have[id] = true
		out = append(out, id)
	}
	return out
}

// resolvedFieldsKey is where the ids resolved during validation are left for
// the rest of the invocation.
const resolvedFieldsKey = "issue.fields"

// noContextFieldsFlag suppresses the context's default field set for one
// invocation.
//
// Named for the *context*, not for "defaults". `--no-default-fields` was the
// obvious spelling and it is ambiguous here: DefaultFields() already means the
// native set this tool always asks for — summary, status, assignee — and a flag
// that reads as though it drops those is a flag that gets used by mistake once.
const noContextFieldsFlag = "no-context-fields"

// fieldFlag is --field itself, declared once for the same reason its opt-out
// is. It was written out twice, identically, in `issue list` and `issue get`,
// which is one place for the two to drift apart and no benefit.
func fieldFlag() registry.Flag {
	return registry.Flag{
		Name: "field", Type: registry.TypeString, Repeatable: true,
		Usage: "extra field to include, by id or name, e.g. " +
			"customfield_10042 or 'Story Points'; " +
			"added to the default set and to the context's, " +
			"repeat for several; a subresource such as comment or worklog is " +
			"refused, naming the command that reads it",
	}
}

// contextFieldsFlag is the opt-out, declared once because both commands that
// take --field need the same question answered the same way.
//
// It exists so the union has an escape hatch: fields from the context always
// apply, and a caller who wants only what they typed can say so. Without it,
// nothing on the command line could produce a request narrower than the
// context, and a value the caller cannot suppress is a value the caller does
// not control.
func contextFieldsFlag() registry.Flag {
	return registry.Flag{
		Name: noContextFieldsFlag, Type: registry.TypeBool,
		Usage: "ignore the field set stored in the context, and ask only for " +
			"the fields named by --field",
	}
}

// validateList refuses a --jql fragment that cannot be contained and the one
// invocation that is a sweep rather than a query, then resolves the field
// names.
//
// All three have to happen here rather than in the body: this is a streaming
// command, so its header — and its columns — go out before the body runs, and a
// rejection from inside it would arrive after bytes were already on stdout.
func validateList(ctx context.Context, inv *registry.Invocation) error {
	if err := requirePageSize(inv); err != nil {
		return err
	}
	if err := requirePageToken(inv); err != nil {
		return err
	}
	if err := refuseUncontainableJQL(inv); err != nil {
		return err
	}
	if err := refuseUnconstrainedSweep(inv); err != nil {
		return err
	}
	if err := validateChangedFilter(inv); err != nil {
		return err
	}
	if err := validateUserFilters(ctx, inv); err != nil {
		return err
	}
	if err := validateBrowseURL(ctx, inv); err != nil {
		return err
	}
	if err := validateFields(ctx, inv); err != nil {
		return err
	}

	// Last, and after every refusal above it. This one spends requests and
	// cannot fail the command, so an invocation that was going to be refused
	// is refused without paying for a diagnostic nobody will read.
	warnUnknownLabels(ctx, inv)
	return nil
}

// requirePageSize refuses a --page-size that can never work, before a session
// exists.
//
// The bounds are arithmetic on a number the caller typed, and resolvePageSize
// was reached only from inside the request loop — past Connect, and so past the
// deployment probe. `--page-size 101` on a cold cache came back NETWORK at
// exit 9, which publishes a usage error as retryable.
//
// The body still resolves the value; this refuses one it would refuse anyway.
// Shared by the three commands that declare the flag, because a bound enforced
// in three places is a bound that will eventually be enforced in two.
//
// Zero is checked here and nowhere below, which is the whole shape of this
// function. The flag's own help says 1 to 100; `--page-size 101` was refused
// with the comment three lines above resolvePageSize arguing that a value the
// server cannot honour is refused rather than clamped, and `--page-size 0` —
// the same sentence at the other end of the range — was answered by silently
// doing 50. Nothing caught it because zero is resolvePageSize's sentinel for
// "unset", and at the layer that reads the flag an explicit zero and an absent
// flag were the same value. registry.Flags.WasSet is what tells them apart, so
// the bound is applied to what the caller typed and an omitted flag still pages
// at the default.
func requirePageSize(inv *registry.Invocation) error {
	n, given := inv.Flags.Int("page-size"), inv.Flags.WasSet("page-size")
	if !given {
		return nil
	}
	if n < MinPageSize || n > MaxPageSize {
		return invalidPageSize(n)
	}
	return nil
}

// requirePageToken refuses a --page-token this tool could not have issued,
// before a session exists.
//
// Only the shape is checked here. Which deployment minted it is DecodePageToken's
// question and stays in the body, because answering it means knowing which site
// is on the other end.
func requirePageToken(inv *registry.Invocation) error {
	_, err := ParsePageToken(inv.Flags.String("page-token"))
	return err
}

// validateChangedFilter refuses --changed-field on its own.
//
// It selects what --changed-by and the changed dates ask about and narrows
// nothing by itself, so `--changed-field assignee` alone is a flag that changes
// no output — the one thing a flag is not allowed to be.
//
// The default value is exempt because it is not something the caller typed, and
// cobra fills it in before the harvest, so nothing downstream can tell an
// explicit `--changed-field status` from an absent one. That case is accepted
// and is a genuine no-op rather than a wrong answer.
func validateChangedFilter(inv *registry.Invocation) error {
	// Empty counts as the default: cobra fills the flag in, but an Invocation
	// built in code — a library caller, or a test — carries only what was set.
	switch inv.Flags.String("changed-field") {
	case "", DefaultChangedField:
		return nil
	}
	for _, companion := range []string{"changed-by", "changed-after", "changed-before"} {
		if inv.Flags.String(companion) != "" {
			return nil
		}
	}
	return errs.Usage("INCOMPLETE_FILTER",
		"--changed-field names the field to ask about and does not filter on its own").
		WithRemedy("add --changed-by, --changed-after, or --changed-before")
}

// refuseUncontainableJQL holds the guarantee --help states: a fragment from
// --jql is parenthesized before it is combined with the other filters, so an OR
// inside it cannot escape the project scope.
//
// One pair of parentheses only contains a fragment whose own parentheses
// balance. `a) OR (1=1` closes the wrapper and opens a new group after it:
//
//	project = "ENG" AND (a) OR (1=1) ORDER BY issuekey DESC
//
// AND binds tighter than OR, so Jira reads that as
// `(project = "ENG" AND a) OR (1=1)`. The scope is gone, and the result comes
// back as an ordinary complete document — wider than was asked for, and
// reporting itself as if it were not.
//
// jql.Validate is what `jr jql explain` already calls on the same input, which
// is why it refused this fragment while `issue list` sent it. The surface that
// describes the query and the surface that sends it have to agree about it.
//
// An empty value is the flag being unset: harvest records every string flag
// whether or not it was passed, so "" and "not given" are the same value here.
// A blank-but-not-empty fragment is a real one and is refused, because
// `--jql '   '` would otherwise reach Jira as the clause `(   )`.
func refuseUncontainableJQL(inv *registry.Invocation) error {
	fragment := inv.Flags.String("jql")
	if fragment == "" {
		return nil
	}
	return jql.ValidateFragment(fragment)
}

// refuseUnconstrainedSweep stops `issue list --limit all` with nothing set.
//
// The default bound is what makes the unfiltered query harmless: it costs one
// request and fifty rows, and refusing it would be a judgement about what
// somebody meant. --limit all is different. It pages until the instance is
// exhausted, and on a production Data Center a personal access token inherits
// every project its owner was ever added to — so the result is every issue in
// every project they can see, which is rarely what was meant and is not
// something to find out afterwards.
//
// --all-projects is how to mean it. The refusal is not that the request cannot
// be honored; it is that a request this wide should be stated rather than
// arrived at.
func refuseUnconstrainedSweep(inv *registry.Invocation) error {
	if !inv.Limit.All || inv.Flags.Bool("all-projects") {
		return nil
	}
	if listQuery(inv).Constrained() {
		return nil
	}
	return errs.Usage("UNCONSTRAINED_QUERY",
		"issue list --limit all with no filter would return every issue in "+
			"every project this credential can see").
		WithDetail("any of these constrains it: %s", strings.Join(constrainingFlags, ", ")).
		WithRemedy("set --project, add a filter, or pass --all-projects to mean it")
}

// constrainingFlags is what the refusal offers instead, so the caller does not
// have to go and read --help to find out what would have been enough.
//
// Every filter belongs here and in QueryOptions.Constrained, which are two
// lists that have to agree. A filter missing from Constrained talks the guard
// out of firing and turns a --watcher query into a full-instance sweep; a
// filter missing from here narrows the query and is not offered as a way to.
// TestEveryFilterConstrainsTheSweepGuard holds the two to each other.
var constrainingFlags = []string{
	"--project", "--jql", "--status", "--not-status",
	"--label", "--not-label", "--type", "--not-type",
	"--assignee", "--reporter", "--creator", "--involving", "--watcher",
	"--voter", "--worklog-author", "--was-assignee", "--changed-by",
	"--created-after", "--created-before", "--updated-after", "--updated-before",
	"--changed-after", "--changed-before", "--worklog-after", "--worklog-before",
}

// ConstrainingFlags exposes that list to the test that holds it to
// QueryOptions.Constrained.
func ConstrainingFlags() []string { return slices.Clone(constrainingFlags) }

// validateFields resolves --field against the site's catalogue and leaves the
// ids on the invocation.
//
// It happens here rather than in the command body because the columns are
// computed from those ids before the body runs — a streaming command's header
// goes out first — and because a name that resolves to nothing has to be
// refused before any bytes reach stdout.
func validateFields(ctx context.Context, inv *registry.Invocation) error {
	fromContext := contextFields(inv)
	fromFlag := inv.Flags.StringSlice("field")

	if len(fromContext) == 0 && len(fromFlag) == 0 {
		// No fields from either source means no catalogue, and no catalogue
		// means no request. The common invocation must not pay for a feature it
		// did not ask for.
		//
		// A context *with* a field set now always pays, once per TTL. That is
		// the trade the set exists for — not having to remember — but it is a
		// real cost on a cold cache and it is why the set is opt-in.
		inv.SetValue(resolvedFieldsKey, []string(nil))
		return nil
	}
	if inv.Jira == nil {
		return errs.Runtime("NO_SESSION", "--field cannot be resolved without a connection to Jira")
	}

	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return err
	}
	catalogue, err := meta.Fields(ctx)
	if err != nil {
		return err
	}

	// Resolved separately so a failure can say which source named the field.
	// A context carrying a field Jira has since renamed would otherwise fail
	// every read with an error that reads as though the caller mistyped a flag
	// they never passed.
	contextIDs, err := ResolveFields(catalogue, fromContext)
	if err != nil {
		return contextFieldFailure(err)
	}
	flagIDs, err := ResolveFields(catalogue, fromFlag)
	if err != nil {
		return err
	}

	// Context first, so column order does not change when a caller adds an
	// ad-hoc --field. ExtraFieldNames collapses two spellings of one field to a
	// single id, so a field named in both places produces one column.
	inv.SetValue(resolvedFieldsKey, append(contextIDs, flagIDs...))
	return nil
}

// contextFields is the default field set, unless this invocation opted out.
func contextFields(inv *registry.Invocation) []string {
	if inv.Jira == nil || inv.Flags.Bool(noContextFieldsFlag) {
		return nil
	}
	return inv.Jira.Fields()
}

// contextFieldFailure re-points a resolution error at the context that supplied
// the name, because nothing the caller typed on this command line caused it.
//
// The original error keeps its code and message — it is still the same unknown
// field — and only the remedy changes, since the fix is somewhere the caller was
// not looking.
func contextFieldFailure(err error) error {
	e := errs.Coerce(err)
	return e.WithRemedy("this field comes from your context, not this command: "+
		"run `%s context show` to see the set, and "+
		"`%s context edit <name> --unset field` to clear it", buildinfo.App, buildinfo.App)
}

// resolvedFields returns the ids validation resolved.
func resolvedFields(inv *registry.Invocation) []string {
	ids, _ := inv.Value(resolvedFieldsKey).([]string)
	return ids
}

// listColumnsFor appends a column per requested field, so asking for one
// changes the default TSV output rather than only the structured formats.
//
// --url appends after those, not before, so turning it on cannot move a column
// that --field already placed.
func listColumnsFor(inv *registry.Invocation) []render.Column {
	cols := append(ListColumns(), ExtraColumns(resolvedFields(inv))...)
	if inv.Flags.Bool(urlFlagName) {
		cols = append(cols, urlColumn())
	}
	if inv.Flags.Bool(ageFlagName) {
		cols = append(cols, ageColumn())
	}
	if inv.Flags.Bool(withCommentsFlag) {
		cols = append(cols, threadColumns()...)
	}
	return cols
}

// threadColumns are what --with-comments adds to TSV.
//
// A thread is not a column: twenty comment bodies in one cell is the defect
// UNRENDERABLE_FIELD refuses, so the bodies live in the structured formats and
// the default format carries the two numbers that let a script decide whether
// it has the whole conversation. They are separate columns rather than one
// "20/25" cell because a column holds a value, not a notation somebody has to
// parse.
func threadColumns() []render.Column {
	return []render.Column{
		{Header: "comments", Path: "comments@count"},
		{Header: "comments-total", Path: "comments@total"},
	}
}

// DefaultFields is what `issue list` asks Jira for.
//
// It is exactly what the default columns and the XML payload need. Asking for
// "*all" instead would be simpler and would make every list an order of
// magnitude larger for fields nobody rendered.
func DefaultFields() []string {
	return []string{
		"summary", "status", "assignee", "reporter",
		"priority", "issuetype", "project", "created", "updated", "labels",
	}
}

// ListDoc renders a result. The complete attribute comes straight from the
// client, which is the only thing that knows whether the result set ran out.
func ListDoc(result *ListResult) *render.Doc {
	items := make([]*render.Node, 0, len(result.Issues))
	for _, i := range result.Issues {
		items = append(items, i.Node())
	}
	return render.List(KindList, VersionList, &render.Collection{
		Name:          "issues",
		Items:         items,
		Complete:      result.Complete,
		NextPageToken: result.NextPageToken,
		Columns:       ListColumns(),
	})
}

// ListQueryFor exposes listQuery for a test that needs to see what the flags
// resolved to without sending anything.
func ListQueryFor(inv *registry.Invocation) QueryOptions { return listQuery(inv) }

// listQuery reads the filters off one invocation.
//
// It is one function because two callers need the same answer: the guard that
// decides whether the query constrains anything, and the body that sends it. A
// second copy would let them disagree about what "unfiltered" means, which is
// the one thing the guard exists to be right about.
func listQuery(inv *registry.Invocation) QueryOptions {
	opt := QueryOptions{
		JQL:         inv.Flags.String("jql"),
		Statuses:    inv.Flags.StringSlice("status"),
		NotStatuses: inv.Flags.StringSlice("not-status"),
		Labels:      inv.Flags.StringSlice("label"),
		NotLabels:   inv.Flags.StringSlice("not-label"),
		Types:       inv.Flags.StringSlice("type"),
		NotTypes:    inv.Flags.StringSlice("not-type"),

		Assignee: resolvedUser(inv, "assignee"),
		Reporter: resolvedUser(inv, "reporter"),
		Creator:  resolvedUser(inv, "creator"),

		Involving:     resolvedUser(inv, "involving"),
		Watcher:       resolvedUser(inv, "watcher"),
		Voter:         resolvedUser(inv, "voter"),
		WorklogAuthor: resolvedUser(inv, "worklog-author"),
		WasAssignee:   resolvedUser(inv, "was-assignee"),
		ChangedBy:     resolvedUser(inv, "changed-by"),
		ChangedField:  inv.Flags.String("changed-field"),

		CreatedAfter:  inv.Flags.String("created-after"),
		CreatedBefore: inv.Flags.String("created-before"),
		UpdatedAfter:  inv.Flags.String("updated-after"),
		UpdatedBefore: inv.Flags.String("updated-before"),
		ChangedAfter:  inv.Flags.String("changed-after"),
		ChangedBefore: inv.Flags.String("changed-before"),
		WorklogAfter:  inv.Flags.String("worklog-after"),
		WorklogBefore: inv.Flags.String("worklog-before"),

		Sort:  inv.Flags.String("sort"),
		Order: inv.Flags.String("order"),
	}
	// Project comes from the resolved context rather than a local flag:
	// --project is global, and it is a default, never a requirement.
	// --all-projects lifts it, which is the only way past a context that sets
	// one — an empty --project falls back to the context rather than clearing
	// it.
	if inv.Jira != nil && !inv.Flags.Bool("all-projects") {
		opt.Project = inv.Jira.Project()
	}
	return opt
}

// Constrained reports whether these options narrow the result set at all.
//
// The sort does not count. Ordering an unbounded query does not make it
// smaller, and treating it as a filter is how a guard against sweeping the
// instance gets talked out of firing by --sort updated.
//
// --changed-field does not count either, and is the one exception worth
// naming: it carries a default, so it is never empty, and reading it as a
// filter would mean every invocation looked constrained. It selects what
// --changed-by looks at rather than narrowing anything by itself, which is
// also why validateChangedFilter refuses it on its own.
func (o QueryOptions) Constrained() bool {
	for _, v := range []string{
		o.Project, o.JQL,
		o.Assignee, o.Reporter, o.Creator, o.Involving,
		o.Watcher, o.Voter, o.WorklogAuthor, o.WasAssignee, o.ChangedBy,
		o.CreatedAfter, o.CreatedBefore, o.UpdatedAfter, o.UpdatedBefore,
		o.ChangedAfter, o.ChangedBefore, o.WorklogAfter, o.WorklogBefore,
	} {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return len(o.Statuses) > 0 || len(o.NotStatuses) > 0 ||
		len(o.Labels) > 0 || len(o.NotLabels) > 0 ||
		len(o.Types) > 0 || len(o.NotTypes) > 0
}

// QueryOptions are the filter flags, before they become JQL.
type QueryOptions struct {
	Project string
	JQL     string
	// Each list filter carries both directions. `status NOT IN (Closed, Done)`
	// is the question a caller asks far more often than any one status they
	// want, and writing it as a --jql fragment is the one path where a typo
	// becomes a wider result set rather than a refusal. Having it for one field
	// and not the next is a surface people have to memorise, so all three pair
	// up.
	Statuses    []string
	NotStatuses []string
	Labels      []string
	NotLabels   []string
	Types       []string
	NotTypes    []string

	// Who the issue belongs to now.
	Assignee string
	Reporter string
	Creator  string

	// Who touched it. Involving is the OR bundle over InvolvingFields;
	// WasAssignee and ChangedBy ask the changelog rather than the issue.
	Involving     string
	Watcher       string
	Voter         string
	WorklogAuthor string
	WasAssignee   string
	ChangedBy     string
	// ChangedField is what ChangedBy and the changed dates apply to. JQL has no
	// way to ask whether *any* field changed, so this is always one field and
	// defaults to DefaultChangedField.
	ChangedField string

	CreatedAfter  string
	CreatedBefore string
	UpdatedAfter  string
	UpdatedBefore string
	ChangedAfter  string
	ChangedBefore string
	WorklogAfter  string
	WorklogBefore string

	Sort  string
	Order string
	// BeforeKey bounds the query below an issue key, which is how keyset
	// pagination resumes. It is set by the client, never by a flag.
	BeforeKey string
}

// BuildQuery turns filter flags into JQL.
//
// Every value goes in as data through the builder. Nothing here concatenates a
// string, and the raw fragment is parenthesized by the renderer rather than by
// this function remembering to.
func BuildQuery(opt QueryOptions) (string, error) {
	b := jql.New()

	if opt.Project != "" {
		b.Project(opt.Project)
	}
	b.In("status", opt.Statuses...)
	b.NotIn("status", opt.NotStatuses...)
	b.In("labels", opt.Labels...)
	b.NotIn("labels", opt.NotLabels...)
	b.In("issuetype", opt.Types...)
	b.NotIn("issuetype", opt.NotTypes...)

	addParticipants(b, opt)
	if err := addDates(b, opt); err != nil {
		return "", err
	}
	if err := addHistory(b, opt); err != nil {
		return "", err
	}

	if opt.JQL != "" {
		b.Raw(opt.JQL)
	}

	// The keyset bound goes in as a clause like any other filter, so it is
	// quoted and combined by the builder rather than spliced into a string.
	if opt.BeforeKey != "" {
		b.Clause(SortKey, jql.OpLt, jql.Text(opt.BeforeKey))
	}

	if err := jql.AppendOrder(b, opt.Sort, opt.Order); err != nil {
		return "", err
	}
	return b.Render()
}

// SortKey is the field results are ordered by when the caller names none.
//
// The policy lives in internal/jql, because `jql explain` has to produce the
// same string this does — a second copy of the ordering rules would make the
// explanation a second implementation, and the two would drift on the first
// change to either.
const SortKey = jql.DefaultSortField

// SortsByKey reports whether these options leave the default key ordering in
// place, which is what keyset pagination requires.
func (o QueryOptions) SortsByKey() bool { return jql.SortsByKey(o.Sort, o.Order) }

// DefaultChangedField is what --changed-by looks at when nothing names a field.
//
// JQL's CHANGED operator takes one field. There is no way to ask whether *any*
// field changed, so "issues I edited" is not a question this or any other Jira
// client can put to the server — only "issues whose status I changed", and the
// same for each field named one at a time.
const DefaultChangedField = "status"

// InvolvingFields is what --involving expands to.
//
// Exported because the flag's own help text is built from it: a bundle that
// does not say which fields it covers is a bundle whose result is short for
// reasons the caller cannot see.
//
// Watchers and voters are deliberately absent. Jira restricts both to the
// calling user unless the credential can manage watchers or view voters, so
// folding them in would make --involving succeed or fail by permission rather
// than by what it matched. They stay as their own flags, where the caller opts
// into that.
//
// Comments are absent because they cannot be present. JQL has no field for
// comment authorship — `comment ~ text` searches bodies, and the
// `issueFunction in commented(...)` everyone reaches for is ScriptRunner.
var InvolvingFields = []string{"assignee", "reporter", "creator", "worklogAuthor"}

// addParticipants adds the filters that name a person.
func addParticipants(b *jql.Builder, opt QueryOptions) {
	addUser(b, "assignee", opt.Assignee)
	addUser(b, "reporter", opt.Reporter)
	addUser(b, "creator", opt.Creator)
	addUser(b, "watcher", opt.Watcher)
	addUser(b, "voter", opt.Voter)
	addUser(b, "worklogAuthor", opt.WorklogAuthor)
	addInvolving(b, opt.Involving)
}

// addInvolving ORs one user across every field that can name them.
//
// The disjunction goes in as a single condition, so the builder parenthesizes
// it when it renders beside the others. Adding the four clauses individually
// would AND them, which asks for issues where one person is all four at once.
func addInvolving(b *jql.Builder, value string) {
	if value == "" {
		return
	}
	exprs := make([]jql.Expr, 0, len(InvolvingFields))
	for _, field := range InvolvingFields {
		exprs = append(exprs, userExpr(field, value))
	}
	b.Where(jql.AnyOf(exprs...))
}

// addDates adds every window filter. A missing value is not a filter.
func addDates(b *jql.Builder, opt QueryOptions) error {
	for _, d := range []struct {
		field, value string
		since        bool
	}{
		{"created", opt.CreatedAfter, true},
		{"created", opt.CreatedBefore, false},
		{"updated", opt.UpdatedAfter, true},
		{"updated", opt.UpdatedBefore, false},
		{"worklogDate", opt.WorklogAfter, true},
		{"worklogDate", opt.WorklogBefore, false},
	} {
		if d.value == "" {
			continue
		}
		build := jql.Until
		if d.since {
			build = jql.Since
		}
		expr, err := build(d.field, d.value)
		if err != nil {
			return err
		}
		b.Where(expr)
	}
	return nil
}

// addHistory adds the filters that ask the changelog rather than the issue.
func addHistory(b *jql.Builder, opt QueryOptions) error {
	if opt.WasAssignee != "" {
		b.Was("assignee", userValue(opt.WasAssignee))
	}
	return addChanged(b, opt)
}

// addChanged builds the one CHANGED clause, from up to three flags.
//
// They are one clause rather than three because CHANGED's qualifiers are
// predicates on a single operator. Three separate clauses would ask for an
// issue whose status changed at some point, and was changed by this person at
// some point, and changed within this window at some point — three facts that
// need not be the same event.
func addChanged(b *jql.Builder, opt QueryOptions) error {
	if opt.ChangedBy == "" && opt.ChangedAfter == "" && opt.ChangedBefore == "" {
		return nil
	}

	preds := make([]jql.Predicate, 0, 3)
	if opt.ChangedBy != "" {
		preds = append(preds, jql.By(userValue(opt.ChangedBy)))
	}
	for _, d := range []struct {
		value string
		after bool
	}{
		{opt.ChangedAfter, true},
		{opt.ChangedBefore, false},
	} {
		if d.value == "" {
			continue
		}
		when, err := jql.ParseDate(d.value)
		if err != nil {
			return err
		}
		build := jql.Before
		if d.after {
			build = jql.After
		}
		preds = append(preds, build(when))
	}

	b.Changed(changedField(opt), preds...)
	return nil
}

// changedField is the field a CHANGED clause asks about.
//
// The flag carries the default, so this only fires for a QueryOptions built by
// hand — a caller of the library, or a test. Defaulting in both places means
// the two cannot disagree about what an unset field means.
func changedField(opt QueryOptions) string {
	if opt.ChangedField != "" {
		return opt.ChangedField
	}
	return DefaultChangedField
}

// addUser appends the clause a user-valued filter becomes.
func addUser(b *jql.Builder, field, value string) { b.Where(userExpr(field, value)) }

// userExpr is the clause a user-valued filter becomes, or nil for an unset one.
//
// It exists separately from addUser because --involving needs the clauses as
// values to OR together rather than as conditions to AND onto the builder.
func userExpr(field, value string) jql.Expr {
	switch {
	case value == "":
		return nil
	case isCurrentUser(value):
		// currentUser is a JQL function, and quoting it as a string would
		// search for a user literally named "currentUser" and return nothing.
		return &jql.Clause{Field: field, Op: jql.OpEq, Value: &jql.Func{Name: "currentUser"}}
	case IsAssigneeSentinel(value):
		return &jql.Clause{Field: field, Op: jql.OpIs, Value: jql.Empty}
	default:
		return jql.Eq(field, value)
	}
}

// userValue is the right-hand side of a predicate naming a person.
//
// Unlike userExpr there is no empty case. `BY EMPTY` is not JQL, and a
// predicate with nobody in it is not built at all — validateUserFilters
// refuses the sentinel words everywhere except --assignee, which is the one
// filter for which "unassigned" is a real answer rather than a word.
func userValue(value string) jql.Value {
	if isCurrentUser(value) {
		return &jql.Func{Name: "currentUser"}
	}
	return jql.Text(value)
}

func getCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "get"},
		Summary: "Fetch one issue in full",
		Description: strings.TrimSpace(`
Returns a single issue with its description, resolution, components, and fix
versions — the fields worth fetching one issue at a time.

Data Center serves wiki markup, which is carried through unchanged. Cloud
serves an Atlassian Document Format object, which is converted to markdown —
losslessly, or not at all: a description holding something markdown cannot
represent is an error naming it rather than an approximation. --raw-body emits
the document itself. The format attribute says which you have.

The issue shape here is the same one issue list emits for a row, so a caller
parses both identically. It simply has more of it filled in.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue get ENG-101",
			buildinfo.App + " issue get ENG-101 --format json",
			buildinfo.App + " issue get ENG-101 --field customfield_10042",
			buildinfo.App + " issue get ENG-101 --raw-body",
			buildinfo.App + " issue get ENG-101 --url",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Flags: []registry.Flag{
			fieldFlag(),
			contextFieldsFlag(), rawBodyFlag(), urlFlag(), ageFlag(),
			{
				Name: withCommentsFlag, Type: registry.TypeBool,
				Usage: "include the comment thread, oldest first; costs a second " +
					"request, and a thread longer than " +
					strconv.Itoa(commentCap) + " is reported incomplete with exit 3",
			},
		},
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindGet, Version: VersionGet}},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Usage, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: validateGet,
		Run:      runGet,
	}
}

// validateGet resolves the field names and settles --url before anything is
// fetched, so a site that cannot supply a base URL is a refusal rather than a
// record with the one element the caller asked for missing.
//
// The key is parsed first, and that order is the whole point rather than a
// tidiness. Both of the checks below reach Jira when their flag is set, and the
// deployment probe behind them is a round trip on a cold cache — so a key that
// needs no network at all was queueing behind one that does, and `issue get
// foo` against an unreachable site reported NETWORK at exit 9. That advertises
// a typo as retryable, which it is not.
func validateGet(ctx context.Context, inv *registry.Invocation) error {
	if err := requireIssueKey(inv); err != nil {
		return err
	}
	if err := validateBrowseURL(ctx, inv); err != nil {
		return err
	}
	return validateFields(ctx, inv)
}

func runGet(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "issue get has no connection to Jira")
	}

	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	client := &Client{Transport: conn, Site: info, Body: bodyMode(inv)}

	fields := append(DetailFields(), ExtraFieldNames(resolvedFields(inv))...)
	issue, err := client.Get(ctx, inv.Args[0], fields)
	if err != nil {
		return nil, err
	}

	if inv.Flags.Bool(urlFlagName) {
		// Through the same helper the listing uses, so one issue and a row
		// carry the same string. Two builders would eventually disagree about
		// a trailing slash.
		one := []Issue{issue}
		if err := stampURLs(one, info); err != nil {
			return nil, err
		}
		issue = one[0]
	}
	if inv.Flags.Bool(ageFlagName) {
		one := []Issue{issue}
		stampAges(one, time.Now())
		issue = one[0]
	}

	// Always, and not behind a flag. A caller cannot ask for a precondition
	// they do not yet know they need — the decision to write comes after the
	// read — and a safety value that has to be requested in advance is one
	// nobody has when it matters. It costs no request: the issue is already here.
	issue.Precondition, err = EncodePrecondition(info, issue.Key, issue.updatedRaw)
	if err != nil {
		return nil, err
	}

	doc := GetDoc(issue)
	if inv.Flags.Bool(withCommentsFlag) {
		// A second request, always, and only when asked for. Comments are a
		// subresource with their own endpoint and their own paging, not a
		// field, so they cannot arrive with the issue.
		if err := attachComments(ctx, client, doc, inv.Args[0]); err != nil {
			return nil, err
		}
	}
	return doc, nil
}

// commentCap bounds the thread an issue carries.
//
// registry.DefaultLimit, because this is the same question `--limit` answers
// everywhere else — how much of an unbounded remote set to take by default —
// and answering it with a different number here would be a second convention
// for one idea.
const commentCap = registry.DefaultLimit

// withCommentsFlag folds the discussion into the record.
const withCommentsFlag = "with-comments"

// attachComments fetches the thread and hangs it off the issue.
//
// The container carries complete="false" when the thread is longer than the
// cap, which is what makes the record able to admit it holds part of something
// — the whole reason this needed a design decision rather than a fetch. Exit 3
// and the stderr warning follow from render.Doc.IsComplete with no branch here.
func attachComments(ctx context.Context, c *Client, doc *render.Doc, key string) error {
	page, err := c.ListComments(ctx, key, 0, commentCap)
	if err != nil {
		return err
	}

	items := make([]*render.Node, 0, len(page.Comments))
	for _, comment := range page.Comments {
		items = append(items, comment.Node())
	}
	container := render.ListEl("comments", "comment", items...)
	// A total the server did not report leaves the attribute off, rather than
	// writing zero. This command asks for one bounded page and has no second
	// request to settle it with, so "I do not know how long this thread is" is
	// an answer it genuinely has to give, and an optional attribute is where it
	// gives it.
	if page.Total != nil {
		container.Attr("total", strconv.Itoa(*page.Total))
	}

	// Total is the server's count of the whole thread, so it decides this
	// rather than a full page doing so: exactly `commentCap` comments is a
	// complete thread, not a truncated one, and guessing from the page size
	// would report every such issue as partial forever.
	//
	// A server that reported no total has not said the thread is whole, and
	// this cannot page to find out — it deliberately fetches one bounded page.
	// So unknown is not complete.
	container.Attr(render.CompleteAttr,
		strconv.FormatBool(exhausted(len(page.Comments), page.Total)))

	doc.Record.Child(container)
	return nil
}

// GetDoc renders one issue as a record, so it defaults to XML: a description
// full of newlines and code fences is mixed content, and XML carries it without
// an escaping tax.
func GetDoc(i Issue) *render.Doc {
	return render.Record(KindGet, VersionGet, i.Node())
}
