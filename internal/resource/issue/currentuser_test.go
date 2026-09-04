package issue_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/resource/issue"
)

// TestANearMissForCurrentUserIsToldAboutIt is issue 133.
//
// The refusal was right and its remedy pointed away from the fix. On a site
// with people whose display names contain "me" it offered four of them and said
// "pass one of these exactly", which reads as though one of those humans was
// meant; on a site with none it offered `user list`, a search for a person who
// does not exist. Reproduced against a real Cloud site on 0.13.0, where the
// second branch fired: the reported one is not the only one.
//
// Both branches now lead with the word that works.
func TestANearMissForCurrentUserIsToldAboutIt(t *testing.T) {
	for _, input := range []string{
		// Semantic misses. No edit distance connects these to the sentinel.
		"me", "@me", "self", "myself", "mine", "ME", " me ",
		// Spelling misses, which internal/nearest catches without a list.
		"current-user", "current_user", "currentusr", "currentUse",
	} {
		t.Run(input, func(t *testing.T) {
			for _, remedy := range []string{
				"pass one of these exactly, by name or by id",
				"search for one with `jr user list <text>`",
			} {
				original := errs.Usage("UNKNOWN_USER",
					"no user on this site is called %q", input).WithRemedy("%s", remedy)

				got := errs.Coerce(issue.SuggestCurrentUserForTest(original, input))
				if !strings.Contains(got.Remedy, "currentUser") {
					t.Errorf("remedy = %q, and it never names currentUser", got.Remedy)
				}
				// The name list is not taken away: the guess can be wrong, and
				// somebody may genuinely have meant a colleague.
				if !strings.Contains(got.Remedy, remedy) {
					t.Errorf("remedy = %q, want it to keep %q", got.Remedy, remedy)
				}
				if !strings.HasPrefix(got.Remedy, "did you mean currentUser?") {
					t.Errorf("remedy = %q, want the suggestion to lead", got.Remedy)
				}
			}
		})
	}
}

// TestARealNameIsNotToldAboutCurrentUser is the false-positive guard, and the
// reason the alias set is written down rather than guessed at.
//
// A refusal that suggested `currentUser` to somebody who typed a colleague's
// name would be the same defect this fixes, pointing the other way.
func TestARealNameIsNotToldAboutCurrentUser(t *testing.T) {
	for _, input := range []string{
		"Ada Lovelace", "grace", "zzzznobody", "ada@example.invalid",
		"712020:8f3a", "unassigned", "",
	} {
		original := errs.Usage("UNKNOWN_USER",
			"no user on this site is called %q", input).
			WithRemedy("search for one with `jr user list <text>`")

		got := errs.Coerce(issue.SuggestCurrentUserForTest(original, input))
		if strings.Contains(got.Remedy, "currentUser") {
			t.Errorf("%q was told about currentUser: %q", input, got.Remedy)
		}
	}
}

// TestOnlyAnUnknownUserGetsTheSuggestion keeps the hint off refusals it has
// nothing to say about.
//
// `--reporter unassigned` is refused with INVALID_USER and its remedy already
// names currentUser, written two functions from the one being changed. Adding a
// second mention would be this tool having two answers to one mistake, which is
// what internal/nearest exists to prevent.
func TestOnlyAnUnknownUserGetsTheSuggestion(t *testing.T) {
	other := errs.Usage("INVALID_USER", "--reporter names a person").
		WithRemedy("use a display name, an email, an id, or the word currentUser")

	got := errs.Coerce(issue.SuggestCurrentUserForTest(other, "me"))
	if got.Code != "INVALID_USER" {
		t.Errorf("code = %q, want it untouched", got.Code)
	}
	if strings.Count(got.Remedy, "currentUser") != 1 {
		t.Errorf("remedy = %q, want the one mention it already had", got.Remedy)
	}
}

// TestNoErrorStaysNoError covers the happy path, where this must cost nothing.
func TestNoErrorStaysNoError(t *testing.T) {
	if err := issue.SuggestCurrentUserForTest(nil, "me"); err != nil {
		t.Errorf("a resolved user produced %v", err)
	}
}
