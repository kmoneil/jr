package jql

import (
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// DefaultSortField is the field results are ordered by when the caller names
// none.
//
// It is the issue key because pagination has to be stable, and the key is the
// only field that is both unique and immutable. Ordering by a mutable field
// such as updated means an issue edited mid-run moves between pages, and an
// offset-paginated deployment then skips or repeats it — a result that is
// quietly missing rows while reporting itself complete.
//
// It lives here rather than in the resource that queries issues, because two
// commands now have to agree on it: the one that sends the query and the one
// that says what would be sent. A second copy is a second thing to keep in
// step, and the whole point of `jql explain` is that it is not a second
// implementation.
const DefaultSortField = "issuekey"

// AppendOrder adds the ORDER BY clause every query carries.
//
// There is always one. Without it the order is whatever the server happens to
// do, which is undocumented, free to change, and not guaranteed to be the same
// between two requests — so a paged result could interleave two different
// orderings and nobody would see it happen.
//
// sort and order are the caller's --sort and --order, either of which may be
// empty. An unrecognized order is a usage error rather than a silent default,
// because "desc" misspelled and silently read as ascending reverses a result
// set without saying so.
func AppendOrder(b *Builder, sort, order string) error {
	if sort == "" {
		b.OrderBy(DefaultSortField, Desc)
		return nil
	}

	if order == "" {
		order = "asc"
	}
	dir, ok := ParseDirection(order)
	if !ok {
		return errs.Usage("INVALID_ORDER",
			"--order does not accept %q", order).
			WithDetail("valid values: asc, desc").
			WithRemedy("sorting is --sort <field> plus --order asc|desc")
	}
	b.OrderBy(sort, dir)

	// A caller's sort field is rarely unique — every issue updated in the same
	// bulk edit shares a timestamp — so ties would break arbitrarily and
	// differently each run. The key is the tiebreaker because it is the one
	// field guaranteed to make the order total.
	if !strings.EqualFold(sort, DefaultSortField) {
		b.OrderBy(DefaultSortField, Desc)
	}
	return nil
}

// SortsByKey reports whether a --sort and --order pair leaves the default key
// ordering in place, which is what keyset pagination requires.
func SortsByKey(sort, order string) bool {
	return sort == "" || (strings.EqualFold(sort, DefaultSortField) &&
		!strings.EqualFold(order, "asc"))
}
