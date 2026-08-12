//go:build !prompt

package cli

import "github.com/kmoneil/jr/internal/errs"

// canPrompt reports whether this build may ask a human for input.
//
// False here, and the constant is what the caller branches on rather than a
// runtime check, so a build without the tag does not link the terminal reader
// at all. That is the same compile-out the profiles are sold on: a reader
// binary cannot mutate Jira, and an agent binary cannot stop to ask a question
// nobody is there to answer.
const canPrompt = false

// promptSecret is unreachable in this build. It exists so the caller compiles
// without a build tag of its own — the decision lives in one place, in
// canPrompt, rather than being duplicated at every call site.
func promptSecret(string) ([]byte, error) {
	return nil, errs.Usage("NO_TOKEN_SOURCE", "this build cannot prompt")
}
