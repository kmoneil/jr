package cli_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/cli"
	"github.com/kmoneil/jira-cli/internal/exitcode"
)

// TestEveryExampleParses runs every example a command publishes.
//
// `--help` is appended, so cobra parses the line and prints the help text
// without the command ever running: an unknown flag, a shorthand that does not
// exist, and a flag that moved to another command all fail here, and nothing
// reaches the network, the config, or Jira. That matters more than it sounds —
// `jr auth status --site your-site.atlassian.net` is a published example, and a
// test that *executed* the examples would resolve that name.
//
// It exists because `jr issue link add ENG-1 blocks ENG-2` shipped in --help
// with the arguments in an order the command does not take, and was fixed by
// hand after somebody typed it. An example is the one piece of documentation
// the binary itself publishes, TestCommandsAreDescribed only asks that one is
// present, and `make docs` copies whatever is there into docs/commands.md — so
// a wrong example is a wrong page as well as a wrong --help.
func TestEveryExampleParses(t *testing.T) {
	var checked int
	for _, c := range cli.Registry().All() {
		for line := range strings.SplitSeq(c.Example, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			args := splitExample(line)
			if len(args) == 0 {
				t.Errorf("%s publishes an example that never invokes %s: %q",
					c.Name(), buildinfo.App, line)
				continue
			}
			checked++

			t.Run(c.Name()+"/"+line, func(t *testing.T) {
				got := run(t, nil, append(args, "--help")...)
				if got.exit == exitcode.OK {
					return
				}
				t.Errorf("%s publishes an example the binary will not parse:\n"+
					"  %s\nexit %v\n%s", c.Name(), line, got.exit, got.stderr)
			})
		}
	}
	if checked < 40 {
		t.Errorf("only %d examples were parsed; this build cannot show the "+
			"rule holding across the surface", checked)
	}
	t.Logf("parsed %d published examples", checked)
}

// TestTheExampleSweepCanFail is the control. A parse check that accepts
// anything is worse than none, and `--help` is exactly the kind of short
// circuit that could swallow a bad flag on some future cobra.
func TestTheExampleSweepCanFail(t *testing.T) {
	got := run(t, nil, "issue", "list", "--no-such-flag", "--help")
	if got.exit != exitcode.Usage {
		t.Errorf("`issue list --no-such-flag --help` exited %v, want %v: "+
			"--help is short-circuiting the flag parse, so TestEveryExampleParses "+
			"is asserting nothing", got.exit, exitcode.Usage)
	}
}

// splitExample returns the arguments the example passes to this binary, or nil
// if it never invokes it.
//
// Examples are shell lines and three of them are pipelines: a token is fed to
// `auth login` by `printf | jr ...`, an attachment is piped *out* to `file`, and
// the completion script is sourced from `<(jr completion zsh)`. Taking the whole
// line would hand another program's arguments to this one; taking only the
// first segment would skip the two where jr is downstream. So: split the
// pipeline, unwrap a process substitution, and return the segment that runs jr.
func splitExample(line string) []string {
	for segment := range strings.SplitSeq(line, "|") {
		args := tokenize(segment)
		for i, arg := range args {
			// `source <(jr completion zsh)` — the wrapper belongs to the shell.
			trimmed := strings.TrimPrefix(strings.TrimPrefix(arg, "<("), "$(")
			if trimmed != buildinfo.App {
				continue
			}
			rest := append([]string{}, args[i+1:]...)
			if n := len(rest); n > 0 {
				rest[n-1] = strings.TrimSuffix(rest[n-1], ")")
			}
			return rest
		}
	}
	return nil
}

// tokenize splits one pipeline segment into words, honouring the quotes several
// examples need for a value with a space in it.
func tokenize(segment string) []string {
	var args []string
	var current strings.Builder
	quote := rune(0)
	started := false

	flush := func() {
		if started {
			args = append(args, current.String())
			current.Reset()
			started = false
		}
	}
	for _, r := range segment {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t':
			flush()
		default:
			started = true
			current.WriteRune(r)
		}
	}
	flush()
	return args
}
