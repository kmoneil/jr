//go:build prompt

package cli

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/kmoneil/jr/internal/errs"
)

// canPrompt reports whether this build may ask a human for input.
//
// True only with the `prompt` tag, which the agent, reader, and ci profiles do
// not carry. Those builds keep the refusal: there is nobody at the other end,
// and a wait with no reader is the hang this tool refuses to be.
const canPrompt = true

// promptSecret asks for a secret on the terminal without echoing it.
//
// The prompt goes to **stderr**, not stdout, for the same reason every other
// diagnostic does: stdout carries the result document and nothing else, so a
// caller redirecting stdout to a file still sees what is being asked, and the
// file does not gain a line of prose.
//
// `term.ReadPassword` puts the terminal into raw mode and restores it, which is
// the whole reason this is worth a dependency: a hand-rolled `stty -echo` leaves
// the terminal with echo off if the process is killed between the two calls,
// and the person then types their next command into an invisible shell.
func promptSecret(label string) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// The caller checked before getting here; this is the guard that keeps
		// the check and the read from disagreeing if that ever stops being
		// true.
		return nil, errs.Usage("NO_TOKEN_SOURCE", "stdin is not a terminal to prompt on")
	}

	fmt.Fprintf(os.Stderr, "%s: ", label)
	secret, err := term.ReadPassword(fd)
	// The newline the user's Return did not echo, so whatever prints next
	// starts on its own line rather than after the prompt.
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, errs.Usage("TOKEN_UNREADABLE", "cannot read the token from the terminal").
			Wrap(err)
	}
	return secret, nil
}
