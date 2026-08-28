package issue

import (
	"slices"
	"strings"

	"github.com/kmoneil/jr/internal/registry"
)

// changedFieldFlag selects which fields' changes `issue history` reports.
const changedFieldFlag = "changed-field"

// UnmatchedFieldCode is the warning a history carries when its --changed-field
// matched nothing and the issue had changes to match against.
const UnmatchedFieldCode = "UNKNOWN_CHANGED_FIELD"

// historyFilter decides which recorded changes a run reports.
//
// The zero value keeps everything, which is what an invocation with no
// --changed-field wants and costs one nil check per row.
type historyFilter struct {
	want []string
	// seen collects the field names this filter rejected, so a run that matched
	// nothing can say what the issue does hold. A mistyped field and a field
	// nobody touched produce the same empty answer otherwise, which is the same
	// defect UNKNOWN_LABEL exists for on issue list.
	seen []string
	// matched counts what survived, because "nothing matched" is only worth
	// reporting when something was there to match.
	matched int
}

// newHistoryFilter reads the flag.
func newHistoryFilter(inv *registry.Invocation) *historyFilter {
	f := &historyFilter{}
	for _, v := range inv.Flags.StringSlice(changedFieldFlag) {
		if v = strings.TrimSpace(v); v != "" {
			f.want = append(f.want, strings.ToLower(v))
		}
	}
	return f
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
	if f == nil || len(f.want) == 0 {
		return true
	}
	if slices.Contains(f.want, strings.ToLower(c.Field)) ||
		(c.FieldID != "" && slices.Contains(f.want, strings.ToLower(c.FieldID))) {
		f.matched++
		return true
	}
	if !slices.Contains(f.seen, c.Field) {
		f.seen = append(f.seen, c.Field)
	}
	return false
}

// apply returns the changes this filter keeps, in order.
func (f *historyFilter) apply(changes []Change) []Change {
	if f == nil || len(f.want) == 0 {
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
