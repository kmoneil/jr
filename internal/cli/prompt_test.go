//go:build prompt

package cli_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoginPromptsOnATerminal is the reason this feature exists, and it needs a
// real pty to test: the whole behaviour turns on stdin being a terminal, and
// every other harness in this tree deliberately hands commands a pipe.
//
// It drives the built binary under `script`, which allocates a pty, types a
// token, and checks three things at once — that the prompt appeared, that the
// token was accepted, and that the token was not echoed back onto the screen,
// which is the property a person is trusting when they type a secret in front
// of somebody.
func TestLoginPromptsOnATerminal(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("no script(1) to allocate a pty with")
	}

	const token = "s3cr3t-token-value"
	before, after := promptExchange(t, token+"\n")

	if !strings.Contains(before, "API token for jira.example.invalid") {
		t.Errorf("no prompt naming the site:\n%s", before)
	}
	if !strings.Contains(after, `authenticated="true"`) {
		t.Errorf("the typed token was not accepted:\n%s", after)
	}
	// The one that matters, and it is asserted on the transcript *after* the
	// prompt only.
	//
	// script(1) copies its own stdin into the pty, and the line discipline
	// echoes that before the child has read anything or turned echo off — so a
	// naive "the token is nowhere in the output" fails on the harness rather
	// than on the program. Typing only once the prompt has appeared, and
	// judging only what follows it, makes the echo state under test the one
	// ReadPassword set.
	if strings.Contains(after, token) {
		t.Errorf("the token was echoed to the terminal:\n%s", after)
	}
}

// promptExchange runs `auth login` under a pty, waits for the prompt, then
// types input. It returns the transcript up to the prompt and the transcript
// after it.
func promptExchange(t *testing.T, input string) (before, after string) {
	t.Helper()
	// The bare command, with no token flag at all, is the one a person types
	// and the one this feature exists for.
	return promptExchangeWith(t, "", input)
}

// promptExchangeWith drives the same exchange with extra flags, so the two
// spellings a human can reach the prompt by are both covered: naming no source,
// and naming --token-stdin at a terminal.
func promptExchangeWith(t *testing.T, flags, input string) (before, after string) {
	t.Helper()

	bin := buildForPrompt(t)
	home := t.TempDir()

	cmd := exec.Command("script", "-q", "-c",
		bin+" auth login --site jira.example.invalid --no-verify"+flags,
		"/dev/null")
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_STATE_HOME="+filepath.Join(home, "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Read until the prompt appears, then type. Bounded, because a wait that
	// cannot give up is a hang: if the prompt never comes this fails with the
	// transcript rather than blocking the suite.
	const prompt = "API token for"
	deadline := time.Now().Add(20 * time.Second)
	var seen strings.Builder
	buf := make([]byte, 256)
	for !strings.Contains(seen.String(), prompt) {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("no prompt within 20s; transcript so far:\n%s", seen.String())
		}
		n, err := stdout.Read(buf)
		seen.Write(buf[:n])
		if err != nil {
			break
		}
	}
	before = seen.String()

	// The prompt arriving is not the same event as echo being off, and this
	// harness can see only the first.
	//
	// The program writes "API token for ...: " and then calls
	// term.ReadPassword, which is what clears ECHO. Between those two the
	// terminal still echoes, and a test that types the instant the prompt
	// bytes appear can land inside that window: the token comes back on the
	// transcript, the assertion below fires, and it is the harness that was
	// too quick rather than the program that failed. It happened on CI at
	// 11:47 and never once in six local runs, which is what that class of bug
	// looks like.
	//
	// Nothing observable says "the ioctl has landed". The pty belongs to
	// script(1), so the test cannot read its termios, and a byte sent to
	// detect echo would be consumed as part of the token. So this waits a
	// fixed interval instead, and it is chosen against what the assertion is
	// really about: whether echo is off by the time a *person* could type. A
	// person is three orders of magnitude slower than this.
	//
	// The assertion keeps its teeth. A build that never disabled echo echoes
	// the token after this wait exactly as it would without it.
	time.Sleep(500 * time.Millisecond)

	if _, err := io.WriteString(stdin, input); err != nil {
		t.Fatalf("type: %v", err)
	}
	_ = stdin.Close()

	rest, _ := io.ReadAll(stdout)
	_ = cmd.Wait()
	return before, string(rest)
}

// TestTokenStdinAtATerminalAlsoPrompts covers the other way a person gets here.
//
// Somebody who has read the scripted form and typed --token-stdin by habit is
// asking for the same thing, and refusing them with a pipeline to copy was the
// original complaint. A script is unaffected: it hands over a pipe, not a
// terminal, and the flag means exactly what it always did there.
func TestTokenStdinAtATerminalAlsoPrompts(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("no script(1) to allocate a pty with")
	}

	before, after := promptExchangeWith(t, " --token-stdin", "typed-token\n")

	if !strings.Contains(before, "API token for") {
		t.Errorf("--token-stdin at a terminal did not prompt:\n%s", before)
	}
	if !strings.Contains(after, `authenticated="true"`) {
		t.Errorf("the typed token was not accepted:\n%s", after)
	}
}

// TestLoginRefusesAnEmptyPromptRatherThanStoringIt covers the Return-on-an-empty-
// prompt case, which otherwise stores a credential that fails every later
// command in a way that looks like the token expired.
func TestLoginRefusesAnEmptyPromptRatherThanStoringIt(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("no script(1) to allocate a pty with")
	}

	_, after := promptExchange(t, "\n")

	if !strings.Contains(after, "EMPTY_TOKEN") {
		t.Errorf("an empty prompt was not refused:\n%s", after)
	}
}

// buildForPrompt builds the binary with the prompt tag, because that is the
// build this behaviour exists in and the test binary itself is not it.
func buildForPrompt(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "jr")
	build := exec.Command("go", "build", "-tags", "prompt", "-o", bin, "../../cmd/jr")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}
