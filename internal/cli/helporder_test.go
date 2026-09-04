package cli_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"

	// Every resource, so the sweep covers the shipped surface and not only the
	// built-ins.
	_ "github.com/kmoneil/jr/internal/commands"
)

// TestHelpLeadsWithTheReference holds a leaf's `--help` to the order a reader
// needs it in: what the command is, how to call it, what flags it takes, and
// only then the detail.
//
// Cobra's default prints Long first, and Long is prose here rather than a
// sentence. `jr issue list --help` was 119 lines with `Usage:` on line 55, so
// the first of its 38 flags arrived on line 60: two 80x24 screens of argument
// before the reference a reader opened `--help` to consult. That is what a user
// reported on 2026-09-04 as documentation with good information that is hard to
// follow, and measuring it found the same shape on `issue activity` (57 lines),
// `auth login` (49) and `issue edit` (44).
//
// The prose is not shortened and this test does not check that it is. It checks
// only that it comes after, because the operative half of it is also in the
// flag usages printed above: `--sort` says the ordering is by issue key,
// `--label` says a comma is part of the label. Moving the argument below the
// reference costs a reader nothing and hands them the flag list first.
func TestHelpLeadsWithTheReference(t *testing.T) {
	commands := cli.Registry().All()
	if len(commands) == 0 {
		t.Fatal("the registry reports no commands at all")
	}

	var checked int
	for _, rc := range commands {
		if strings.TrimSpace(rc.Description) == "" {
			continue
		}
		t.Run(rc.Name(), func(t *testing.T) {
			args := append(strings.Fields(rc.UseLine()), "--help")
			got := run(t, nil, args...)
			if got.exit != 0 {
				t.Fatalf("%s --help exited %v: %s", rc.UseLine(), got.exit, got.stderr)
			}
			out := got.stdout

			// The first line of the declared prose, which is what has to end up
			// below the reference. Matching the whole of it would break on
			// cobra's rewrapping; the first line is enough to locate it.
			lead := strings.TrimSpace(strings.SplitN(strings.TrimSpace(rc.Description), "\n", 2)[0])
			at := strings.Index(out, lead)
			if at < 0 {
				t.Fatalf("%s --help does not contain its own description at all, "+
					"so this test read the output and asserted nothing about it.\n"+
					"Looked for: %q", rc.UseLine(), lead)
			}

			usage := strings.Index(out, "\nUsage:")
			if usage < 0 {
				t.Fatalf("%s --help prints no Usage: block", rc.UseLine())
			}
			if usage > at {
				t.Errorf("%s --help prints its description before Usage:. A reader "+
					"opens --help holding the command name already, to find a flag, "+
					"and this puts prose between them.", rc.UseLine())
			}

			// Flags only when the command has any of its own. Every command
			// inherits the globals, so `Flags:` is nearly always present, but a
			// command declaring none has nothing to bury.
			if len(rc.AllFlags()) > 0 {
				flags := strings.Index(out, "\nFlags:")
				if flags < 0 {
					t.Fatalf("%s declares %d flags and --help prints no Flags: block",
						rc.UseLine(), len(rc.AllFlags()))
				}
				if flags > at {
					t.Errorf("%s --help prints its description before Flags:, "+
						"pushing the flag list %d bytes down the page.",
						rc.UseLine(), flags-at)
				}
			}
			checked++
		})
	}

	if checked == 0 {
		t.Error("no command carried a description, so this test asserted nothing")
	}
	t.Logf("checked %d commands carrying prose", checked)
}
