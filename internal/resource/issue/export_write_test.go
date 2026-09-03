//go:build write

package issue

import "github.com/kmoneil/jr/internal/site"

// CommentIsAcceptedForTest exposes the transition-screen check.
//
// The check is unexported because nothing outside this package decides it, and
// it is reached in production only through sendMove, which needs a resolved
// transition from a live metadata call. Testing it through that would be
// testing the metadata plumbing; what is worth pinning is the decision itself,
// which is a pure function of a transition and a string.
func CommentIsAcceptedForTest(t site.Transition, comment string) error {
	return commentIsAccepted(t, comment)
}
