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
//
// order applies whether or not sort was given. It used to be read only
// alongside a sort, so `--order asc` on its own was accepted, dropped, and
// answered with the descending default — the flag doing nothing while its own
// help said it set the direction. Ordering the default field the way the caller
// asked is the honest reading, and it costs the keyset precondition rather than
// correctness: SortsByKey sees the ascending key and the client pages by offset,
// which is the fallback that already exists for every other ordering.
func AppendOrder(b *Builder, sort, order string) error {
	dir, err := direction(sort, order)
	if err != nil {
		return err
	}
	if sort == "" {
		b.OrderBy(DefaultSortField, dir)
		return nil
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

// direction resolves --order for whatever is being ordered.
//
// An unset direction is two defaults and not one, and which applies depends on
// whether the caller named a field. The key ordering nobody asked for descends,
// because the newest issue first is the useful listing and because that is the
// order keyset pagination resumes in. A field the caller did name ascends,
// which is the conventional default for a sort and what `--sort updated` has
// always meant. Collapsing them onto one constant would either reverse every
// unsorted listing or turn `--sort summary` into reverse alphabetical.
func direction(sort, order string) (Direction, error) {
	if order == "" {
		if sort == "" {
			return Desc, nil
		}
		return Asc, nil
	}
	dir, ok := ParseDirection(order)
	if !ok {
		return "", errs.Usage("INVALID_ORDER",
			"--order does not accept %q", order).
			WithDetail("valid values: asc, desc").
			WithRemedy("sorting is --sort <field> plus --order asc|desc")
	}
	return dir, nil
}

// SortsByKey reports whether a --sort and --order pair leaves the default key
// ordering in place, which is what keyset pagination requires.
//
// Both halves matter, and the direction is the half that was missing: an empty
// sort used to answer yes whatever the order, which was true only for as long
// as AppendOrder ignored the order beside it. `--order asc` now orders the key
// ascending, and resuming that with an `issuekey < last` bound would page
// backwards through issues the caller had already been sent while reporting
// itself complete.
func SortsByKey(sort, order string) bool {
	if sort != "" && !strings.EqualFold(sort, DefaultSortField) {
		return false
	}
	dir, err := direction(sort, order)
	return err == nil && dir == Desc
}
