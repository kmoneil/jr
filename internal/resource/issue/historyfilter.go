package issue

import (
	"slices"
	"strings"

	"github.com/kmoneil/jr/internal/registry"
)

// changedFieldFlag selects which fields' changes a command reports, and
// notChangedFieldFlag drops them.
//
// The negative is spelled --not-changed-field rather than the --exclude-field
// issue 119 proposed, because every negative twin in this tool is --not-: the
// list filters are --not-status, --not-label and --not-type, and there is no
// --exclude- anything. A second spelling for one idea is a second thing to
// remember, and the issue was proposing a capability rather than a name.
const (
	changedFieldFlag    = "changed-field"
	notChangedFieldFlag = "not-changed-field"
)

// UnmatchedFieldCode is the warning a history carries when its --changed-field
// matched nothing and the issue had changes to match against.
const UnmatchedFieldCode = "UNKNOWN_CHANGED_FIELD"

// historyFilter decides which recorded changes a run reports.
//
// The zero value keeps everything, which is what an invocation with no
// --changed-field wants and costs one nil check per row.
type historyFilter struct {
	want []string
	// drop is the negative twin, and it is applied first. A caller who names
	// the same field in both has asked for a contradiction, and excluding wins
	// because it is the more specific instruction: --changed-field is often a
	// wide net somebody then trims.
	drop []string
	// seen collects the field names this filter rejected, so a run that matched
	// nothing can say what the issue does hold. A mistyped field and a field
	// nobody touched produce the same empty answer otherwise, which is the same
	// defect UNKNOWN_LABEL exists for on issue list.
	seen []string
	// matched counts what survived, because "nothing matched" is only worth
	// reporting when something was there to match.
	matched int
}

// newHistoryFilter reads both flags.
func newHistoryFilter(inv *registry.Invocation) *historyFilter {
	f := &historyFilter{}
	f.want = lowerFields(inv.Flags.StringSlice(changedFieldFlag))
	f.drop = lowerFields(inv.Flags.StringSlice(notChangedFieldFlag))
	return f
}

// lowerFields trims and folds the values of one repeatable field flag.
//
// **It never yields an empty string, and keepField depends on that.** An event
// that moves no field, which is every comment and every worklog, is offered to
// the filter with an empty name, and it survives because "" cannot be in a list
// this built. Guarding the comparisons with `name != ""` instead would look
// like the thing keeping comments in the feed while being unreachable, and a
// guard that cannot fire is indistinguishable from one that works.
// TestAFieldlessEventSurvivesBecauseTheFilterCannotHoldAnEmptyName is what
// makes it fail if this ever returns one.
func lowerFields(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, strings.ToLower(v))
		}
	}
	return out
}

// keepField decides one event or change by the field it moved.
//
// An empty name never matches a positive filter and is never dropped by a
// negative one, which is what makes this usable from `issue activity`, where a
// comment and a worklog move no field at all. So --not-changed-field Rank keeps
// the conversation and drops the grooming, and --changed-field assignee keeps
// assignee changes alone. A caller who wants a kind rather than a field has
// --kind, which is the flag for that question.
func (f *historyFilter) keepField(name, id string) bool {
	if f == nil {
		return true
	}
	lname, lid := strings.ToLower(name), strings.ToLower(id)
	named := func(list []string) bool {
		return slices.Contains(list, lname) || (id != "" && slices.Contains(list, lid))
	}

	if named(f.drop) {
		return false
	}
	if len(f.want) == 0 {
		return true
	}
	if named(f.want) {
		f.matched++
		return true
	}
	// The empty name is excluded here and only here. seen becomes the list of
	// fields a fruitless --changed-field is told about, and "this issue has
	// changes to , status" helps nobody.
	if name != "" && !slices.Contains(f.seen, name) {
		f.seen = append(f.seen, name)
	}
	return false
}

// keep reports whether this change is one the caller asked for, and records
// what it saw either way.
//
// It matches on what the changelog actually carries — the field's display name,
// and its id where the server sent one — and never through the site's field
// catalogue. That is not a shortcut. Jira 9.12 Data Center sends no field id at
// all, measured and written into ChangeSchema, so resolving 'Story Points' to
// customfield_10042 and comparing against the id would match nothing on half
// the installed base. The changelog's own name is already the friendly one.
func (f *historyFilter) keep(c Change) bool {
	return f.keepField(c.Field, c.FieldID)
}

// active reports whether either flag was given, so a caller can skip the walk.
func (f *historyFilter) active() bool {
	return f != nil && (len(f.want) > 0 || len(f.drop) > 0)
}

// apply returns the changes this filter keeps, in order.
func (f *historyFilter) apply(changes []Change) []Change {
	if !f.active() {
		return changes
	}
	out := make([]Change, 0, len(changes))
	for _, c := range changes {
		if f.keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// warnIfNothingMatched says so when the filter removed everything.
//
// The empty answer to a mistyped field and the empty answer to a field nobody
// touched are the same bytes, and this is the same argument UNKNOWN_LABEL makes
// on issue list, for free: the candidate list is the rows already fetched, so
// there is no request and nothing to cache. It names what the issue does hold,
// because a refusal that only says "unknown" leaves the caller to go and read
// the whole history to find their typo — which is the output they were trying
// to avoid.
//
// It is a warning and not a refusal. Asking about a field this issue never
// changed is a legal question with a correct answer, and exit 0 is that answer.
func (f *historyFilter) warnIfNothingMatched(inv *registry.Invocation) {
	if f == nil || len(f.want) == 0 || f.matched > 0 || len(f.seen) == 0 {
		return
	}
	warn(inv, UnmatchedFieldCode,
		"no recorded change matches --"+changedFieldFlag+" "+
			strings.Join(f.want, ", ")+"; this issue has changes to "+
			strings.Join(f.seen, ", "))
}
