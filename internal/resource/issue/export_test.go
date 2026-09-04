package issue

import "slices"

// This file exists so the user-filter sweep can take its subjects from the list
// the command actually loops over, and it is the second of its kind in this
// tree, so it is worth a word.
//
// The invariant is that every `issue list` filter naming a person resolves that
// person. A sweep driven by a literal list in the test would assert it about
// the flags somebody remembered to copy, which is the same defect one layer
// out: `--reporter` was declared, documented, and left out of the resolution
// for as long as nobody typed a display name into it. Reading userFilterFlags
// means a tenth user-valued flag joins the sweep by existing.
//
// It hands back a copy, so a test cannot reorder or truncate what production
// reads. Being in `package issue` rather than `issue_test` is what makes this
// possible; every other test file here is external and should stay that way.

// UserFilterFlagsForTest is the list of `issue list` filters whose value names
// a person, as the command sees it.
func UserFilterFlagsForTest() []string { return slices.Clone(userFilterFlags) }

// SentinelFilterFlagsForTest names the filters that honour `unassigned` and
// `empty`, so a test can assert the sentinels are not spreading rather than
// hard-coding the one flag that takes them today.
func SentinelFilterFlagsForTest() []string {
	out := make([]string, 0, len(sentinelFilterFlags))
	for name := range sentinelFilterFlags {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// SuggestCurrentUserForTest exposes the near-miss hint.
//
// Unexported in production because only this package's three resolve call sites
// use it, and reaching it through them would need a live metadata lookup. What
// is worth pinning is the decision, which is a pure function of an error and a
// string.
func SuggestCurrentUserForTest(err error, input string) error {
	return suggestCurrentUser(err, input)
}
