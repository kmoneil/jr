package cli_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"

	// The rule holds for every command the binary ships, not only the
	// built-ins, so the resources are linked in here too.
	_ "github.com/kmoneil/jr/internal/commands"
)

// TestEveryFlagChangesWhatTheCommandDoes drives each flag on each command twice
// — once absent, once set — and requires the two runs to differ somewhere a
// caller could see.
//
// "A flag either affects the output or does not exist" has been an invariant
// since `--field` shipped fetching a field and discarding it, and until now it
// was enforced by reading the code. It missed the next one: `--order` was
// declared, bound, harvested, passed to the ordering policy, and dropped by an
// early return that only looked at `--sort`, so `jr issue list --order asc`
// returned rows newest-first and said nothing. Every gate in the suite passed,
// because every gate reads the *declaration* — the flag was declared, typed,
// enumerated, documented, and classified — and none of them ran the command
// with the flag on and with it off to see whether anything moved.
//
// What "moved" means here is deliberately generous: the requests the command
// made, the columns it would print, the document it emitted, or the error it
// failed with. A flag that only changes the error is still doing something, and
// narrowing this to "changes the request" would demand a plausible response for
// sixty commands. The defect this catches is the flag that changes *nothing*,
// which is the one that ships.
//
// A flag that moves nothing must be named in flagWithNoObservableEffect with
// the reason, so a new one fails here rather than being classified quietly.
func TestEveryFlagChangesWhatTheCommandDoes(t *testing.T) {
	requireNothingIsWritten(t)
	var moved, exempt int

	for _, c := range cli.Registry().All() {
		if why := commandNotDriven(c); why != "" {
			continue
		}
		for _, f := range c.AllFlags() {
			name := c.Name() + "/--" + f.Name
			if _, ok := flagWithNoObservableEffect[name]; ok {
				exempt++
				continue
			}
			_, values, ok := probePair(f)
			if !ok {
				t.Errorf("%s: this sweep has no probe value for a %s flag; "+
					"add one to probePair or exempt the flag with a reason",
					name, f.Type)
				continue
			}

			t.Run(name, func(t *testing.T) {
				// Both deployments, and moving either is enough. --raw-body
				// converts a Cloud document and is inert against Data Center's
				// wiki markup by design, so a one-deployment sweep would report
				// a working flag as dead — and, run the other way round, would
				// be blind to a flag that only works on one of them.
				var seen []string
				for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
					base := drive(t, c, kind, f.Name, nil)
					for _, value := range values {
						with := drive(t, c, kind, f.Name, func(flags registry.Flags) {
							setProbe(flags, f, value)
						})
						if base != with {
							return
						}
					}
					seen = append(seen, string(kind)+":\n"+base)
				}
				t.Errorf("--%s changed nothing on %s, against either deployment.\n%s\n"+
					"A flag that is accepted and then dropped is the --order "+
					"defect: declared, bound, harvested, and ignored. Make it "+
					"reach the request, the columns, or the document — or add "+
					"%q to flagWithNoObservableEffect with the reason it "+
					"cannot be seen from here.",
					f.Name, c.Name(), strings.Join(seen, "\n"), name)
			})
			moved++
		}
	}

	// A sweep that ran nothing is worse than no sweep. The ci profile is the
	// smallest tag set and therefore the binding number; the full build sweeps
	// far more. This is a floor against a sweep that quietly stopped selecting
	// anything, not a coverage claim.
	if moved < 40 {
		t.Errorf("only %d flags were driven; this build cannot show the rule "+
			"holding across the surface", moved)
	}
	t.Logf("drove %d flags, %d exempt with a written reason", moved, exempt)
}

// TestTheFlagSweepCanFail is the question every sweep has to answer about
// itself, and this one has two ways of passing for the wrong reason.
//
// The first is noise: the sweep declares a flag alive the moment two runs
// differ, so a fingerprint carrying anything nondeterministic — a timestamp, a
// generated key, an unsorted map — would pass every flag in the build,
// including the dead ones. Driving the same command twice with the same flags
// has to produce the same bytes.
//
// The second is a comparison that cannot fail. A command whose flag genuinely
// does nothing must produce two identical fingerprints, or the sweep above is
// an expensive way of printing "ok".
func TestTheFlagSweepCanFail(t *testing.T) {
	requireNothingIsWritten(t)
	var driven int
	for _, c := range cli.Registry().All() {
		if commandNotDriven(c) != "" {
			continue
		}
		driven++
		t.Run(c.Name(), func(t *testing.T) {
			first := drive(t, c, site.DataCenter, "", nil)
			second := drive(t, c, site.DataCenter, "", nil)
			if first == second {
				return
			}
			t.Errorf("driving %s twice produced two different results, so "+
				"every flag on it passes the sweep whatever it does:\n%s\n%s",
				c.Name(), first, second)
		})
	}
	if driven == 0 {
		t.Fatal("no command was driven, so this proves nothing about the sweep")
	}

	// The negative control: a flag nothing reads.
	inert := &registry.Command{
		Path:    []string{"probe", "inert"},
		Summary: "A command with a flag its body ignores",
		Flags: []registry.Flag{
			{Name: "ignored", Type: registry.TypeString, Usage: "read by nothing"},
		},
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: "probe.inert", Version: 1}},
		Run: func(context.Context, *registry.Invocation) (*render.Doc, error) {
			return render.Record("probe.inert", 1, render.El("probe")), nil
		},
	}
	base := drive(t, inert, site.DataCenter, "ignored", nil)
	with := drive(t, inert, site.DataCenter, "ignored", func(flags registry.Flags) {
		flags.SetString("ignored", "probe")
	})
	if base != with {
		t.Errorf("a flag nothing reads moved the fingerprint, so the sweep "+
			"reports every flag alive and can never fail:\n%s\n%s", base, with)
	}
}

// TestTheFlagSweepAccountsForEveryFlag is the other half, and the half that
// makes the exemptions honest.
//
// Every flag in the build is either driven above or named in one of the two
// maps with a reason. A new command lands in neither by default, and a flag
// that stops being driven — because its command grew a required argument this
// harness cannot supply — has to be written down rather than silently dropping
// out of the sweep. `notAFilter` on issue list works the same way, and it is
// what let `--order` sit for months behind the words "meaningless without
// --sort", which was a classification standing in for an assertion.
//
// It reads Command.Flags rather than Command.AllFlags, so the one derived flag
// (--limit, which Paginated implies) is outside its accounting. That is a gap,
// and it is written here rather than left to be discovered: driving
// --limit needs a fixture that returns more rows than the bound, and this
// harness answers most collections with none, so 16 of 18 paginated commands
// would have to be exempted as "the response was empty" rather than driven.
// Sixteen classifications standing in for an assertion is the thing the
// paragraph above is about. The flag's effect is asserted directly instead, by
// TestCompleteResultIsSilent and the truncation goldens, and the accounting is
// picked up in TestEveryBoundFlagIsDeclared, which compares the bound surface
// against the declared one. See _plans/backlog/drive-limit-in-the-flag-sweep.md.
func TestTheFlagSweepAccountsForEveryFlag(t *testing.T) {
	named := map[string]bool{}
	for name := range flagWithNoObservableEffect {
		named[name] = true
	}

	var unreachable int
	for _, c := range cli.Registry().All() {
		why := commandNotDriven(c)
		for _, f := range c.AllFlags() {
			name := c.Name() + "/--" + f.Name
			if why != "" {
				unreachable++
			}
			delete(named, name)
		}
		// A command with no flags is not a gap in a flag sweep, whether or not
		// this harness could drive it.
		if why == "" || len(c.AllFlags()) == 0 {
			continue
		}
		if commandNotSwept[c.Name()] == "" {
			t.Errorf("%s has %d flags the sweep never drives (%s) and is not "+
				"in commandNotSwept; add it with the reason and with where "+
				"those flags are covered instead, or teach the harness to "+
				"drive it", c.Name(), len(c.AllFlags()), why)
		}
	}
	for name := range commandNotSwept {
		if _, ok := registry.Lookup(name); !ok {
			continue // Behind a tag this build does not have.
		}
		if commandNotDriven(mustLookup(t, name)) == "" {
			t.Errorf("%s is excused from the flag sweep and the sweep drives "+
				"it; delete the excuse", name)
		}
	}

	// An exemption whose command this build does not contain is not stale, it
	// is behind a tag: the reader build has no write verbs, and holding it to
	// the full build's exemptions would fail every profile but one.
	for name := range named {
		command, _, _ := strings.Cut(name, "/--")
		if _, built := registry.Lookup(command); !built {
			continue
		}
		t.Errorf("%q is exempted from the flag sweep and no such flag exists "+
			"on a command this build has; the declaration moved and the "+
			"exemption outlived it", name)
	}
	t.Logf("%d flags belong to commands this harness cannot drive", unreachable)
}

// requireNothingIsWritten holds the sweep to the claim writesToDisk makes on
// its behalf: it drives a hundred commands and leaves nothing behind.
//
// The claim was false when it was first written, which is why this exists
// rather than a comment saying the harness is careful.
func requireNothingIsWritten(t *testing.T) {
	t.Helper()
	before := listing(t)
	t.Cleanup(func() {
		if after := listing(t); after != before {
			t.Errorf("the sweep wrote to the working tree.\nbefore: %s\nafter:  %s",
				before, after)
		}
	})
}

func listing(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the working directory: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

// commandNotDriven reports why a command cannot be run by this harness, or "".
//
// The reasons are structural rather than a list of names: a command with a
// required argument this harness cannot invent, or one that owns stdout, cannot
// be driven whatever its flags are. commandNotSwept holds the ones that are
// structurally drivable and still excluded, which is a different claim and
// needs a sentence each.
func commandNotDriven(c *registry.Command) string {
	if writesToDisk[c.Name()] != "" {
		return writesToDisk[c.Name()]
	}
	if c.OwnsStdout {
		return "it owns stdout: running it here would speak a protocol at the test"
	}
	if c.Run == nil && c.Stream == nil {
		return "it has no body"
	}
	if !c.NeedsJira {
		// A local command's observable is a file under the state directory or
		// a document built from the config, and this harness has neither. They
		// are covered where they can be: auth and context have their own
		// end-to-end tests against a temporary home.
		return "it does not reach Jira, so a recorded request cannot show its flags working"
	}
	if reason := missingArg(c); reason != "" {
		return reason
	}
	return ""
}

// writesToDisk names the commands whose output is a file, which this harness
// will not drive.
//
// Not a preference: driving `issue attachment download` once left an
// `attachment-10000` and an `other` in internal/cli, named after the probe
// values, because the sweep drives every flag and the flag it was driving was
// --output. A sweep that scatters files through the working tree is a sweep
// nobody runs twice, and it is also a sweep whose second run reads the first
// one's leftovers.
var writesToDisk = map[string]string{
	"issue.attachment.download": "its result is a file in the working " +
		"directory, and driving --output writes one per probe value",
	"issue.attachment.upload": "its argument is a file that has to exist " +
		"before the command runs",
}

// missingArg reports the first required positional this harness has no value
// for. A wrong value is worse than none: the command refuses it identically in
// both runs, every flag looks dead, and the sweep reports a wall of failures
// about the argument rather than the flags.
func missingArg(c *registry.Command) string {
	for _, a := range c.Args {
		if !a.Required {
			continue
		}
		if argProbe(c, a) == "" {
			return fmt.Sprintf("its %q argument has no probe value here", a.Name)
		}
	}
	return ""
}

// argProbe is a value each positional will accept, by command and then by the
// argument's own name.
//
// Every one of these has to survive the command's own validation, or the run
// stops before the flags are read. They are checked against the parsers rather
// than guessed: an issue key must parse as one, an id must be digits.
func argProbe(c *registry.Command, a registry.Arg) string {
	if v, ok := argByCommand[c.Name()+"/"+a.Name]; ok {
		return v
	}
	switch a.Name {
	case "key", "issue", "from", "to":
		return "ENG-1"
	case "project":
		return "ENG"
	case "id", "board", "sprint", "epic":
		return "1"
	case "name", "query", "user", "body", "assignee":
		return "probe"
	case "relationship":
		return "Blocks"
	case "time":
		return "1h"
	}
	return ""
}

// argByCommand names the positionals whose meaning is narrower than their name.
var argByCommand = map[string]string{
	"issue.move/transition":        "Done",
	"issue.comment.delete/id":      "10000",
	"issue.worklog.delete/id":      "10000",
	"issue.attachment.download/id": "10000",
	"sprint.close/id":              "2",
	"sprint.add/sprint":            "2",
	"epic.add/epic":                "ENG-9",
	"issue.link.add/to":            "ENG-9",
	"user.get/user":                "probe",
	// Three commands whose positional is called `key` and means a *project*
	// key. The generic case answers an issue key, which they refuse locally,
	// so every flag on them was measured against a refusal.
	"project.components/key": "ENG",
	"project.statuses/key":   "ENG",
	"project.versions/key":   "ENG",
}

// flagWithNoObservableEffect names the flags that move nothing this harness can
// see, with the reason each one cannot be seen from here.
//
// Every entry is a claim that the flag works and that the *observation* is what
// falls short — not that the flag is allowed to do nothing. A flag that really
// does nothing is the defect this file exists for, and belongs in the code or
// in the bin, never here.
var flagWithNoObservableEffect = map[string]string{
	// The key's whole effect is on the ledger under the state directory: it is
	// claimed before the request and completed after, and it never appears in
	// the request itself — that is the design, since Jira has no idempotency
	// header. This harness has no state directory on purpose, so there is
	// nothing here for it to move. TestCreateReplaysUnderOneKey drives the
	// behaviour that matters, against a real ledger in a temp dir and a
	// cassette that refuses to answer a duplicate.
	"issue.create/--idempotency-key": "its effect is a claim in the ledger, " +
		"not a field in the request; TestCreateReplaysUnderOneKey and " +
		"TestAFailedCreateKeepsItsClaim",
	"issue.clone/--idempotency-key": "same ledger, same reason; " +
		"TestEveryWriteVerbIsSafeByConstruction covers the declaration",
	"issue.move/--idempotency-key": "same ledger, and the claim comes before " +
		"the transitions are read; TestMoveRunsAsARegisteredCommand",
}

// commandNotSwept names every command carrying flags this sweep never drives,
// with the reason and with where those flags are covered instead.
//
// All of them are local: their effect is a file under the state directory or a
// credential in the store, and this harness deliberately has neither — a sweep
// that wrote to a real home would be a sweep nobody could run twice. They are
// the one class where an end-to-end test already exists per behaviour, because
// nothing about them needs a server.
var commandNotSwept = map[string]string{
	"issue.attachment.upload": "its argument is a file on disk, and reading " +
		"one is the first thing it does; internal/resource/issue/" +
		"attachment_write_test.go drives it against a temp file",
	"issue.attachment.download": "it writes the attachment into the working " +
		"directory; internal/resource/issue/attachment_test.go drives " +
		"--output and --force against a temp dir",

	"auth.login": "its effect is a credential in the store and a context in " +
		"the config; TestAuthLifecycle and TestLoginTrimsTheToken drive it " +
		"against a temporary home",
	"auth.logout": "same store; TestLogoutCannotRemoveAnEnvironmentCredential",
	"auth.status": "reads the store rather than Jira; " +
		"TestAuthStatusSaysWhetherTheCredentialIsSiteScoped",
	"auth.token": "prints the credential; TestAuthStatusNeverRevealsTheToken " +
		"and TestTokenIsNotAcceptedOnTheCommandLine",
	"context.create": "writes config.toml; TestContextLifecycle",
	// Both are paginated and neither reaches Jira, so their one flag is
	// --limit and a recorded request cannot show it working. Both prove it
	// end to end instead, through cli.Main, which is the stronger form: the
	// real exit code and the real stderr, which is the pair a script checks.
	"context.list": "reads config.toml rather than Jira; " +
		"TestContextListTruncatesAndSaysSo drives --limit past the row count",
	"schema": "describes the binary rather than a site; " +
		"TestSchemaIsCompleteWithNoFlags drives --limit against the command list",
	"context.delete": "writes config.toml; TestContextErrors",
	"context.edit": "writes config.toml, and every flag on it is covered one " +
		"at a time by TestEditingOneSettingLeavesTheRestAlone, " +
		"TestUnsetClearsWhatAnEmptyFlagCannot, and " +
		"TestARepeatableEnumAcceptsEveryLegalValue",
}

func mustLookup(t *testing.T, name string) *registry.Command {
	t.Helper()
	c, ok := registry.Lookup(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	return c
}

// probePair is two legal values for a flag: the one a run that is not testing
// it uses, and the one the run under test sets.
//
// They have to differ. A required flag is set in every run — leaving it out
// would make both runs the same refusal and every flag on the command read as
// dead — so testing it means varying it, and a second value is the only way to.
// Both must be values the flag accepts: a refusal is a difference, and a sweep
// that passes because two runs failed differently is measuring nothing.
func probePair(f registry.Flag) (base string, alts []string, ok bool) {
	switch f.Type {
	case registry.TypeBool:
		return "false", []string{"true"}, true
	case registry.TypeInt:
		return "7", []string{"9"}, true
	case registry.TypeEnum:
		base = f.Default
		if base == "" && len(f.Enum) > 0 {
			base = f.Enum[0]
		}
		// Every value, not one of them. An enum whose default behaviour
		// matches one of its own values — `--order` unset orders the key
		// descending, so `--order desc` is rightly a no-op — would otherwise
		// read as dead on whichever value the sweep happened to pick.
		return base, f.Enum, len(f.Enum) > 0
	}
	base = "probe"
	if v, named := probeByFlag[f.Name]; named {
		base = v
	}
	alt := "other"
	if v, named := probeAltByFlag[f.Name]; named {
		alt = v
	}
	return base, []string{alt}, true
}

// companionFlags are the flags a command needs before the one under test has
// anything to act on.
//
// --body-format says how to read a body, and a command given no body formats
// nothing: both runs produce the same request and a working flag reads as dead.
// The same is true of a verb that refuses an empty change — `issue edit` with
// no field to edit never reaches the point where --dry-run would matter.
var companionFlags = map[string]map[string]string{
	"issue.create":      {"description": "probe"},
	"issue.edit":        {"description": "probe"},
	"issue.worklog.add": {"comment": "probe"},
}

// probeAltByFlag is the second value, wherever "other" is not one the flag
// accepts.
var probeAltByFlag = map[string]string{
	// One row against a fixture that answers with two, which is the only way a
	// bound is observable at all: `--limit 50` and `--limit 1` against an empty
	// collection produce the same request and the same document.
	"limit":          "1",
	"since":          "-3651d",
	"kind":           "worklog",
	"jql":            "labels = second",
	"sort":           "created",
	"created-after":  "-8d",
	"created-before": "-2d",
	"updated-after":  "-8d",
	"updated-before": "-2d",
	"changed-after":  "-8d",
	"changed-before": "-2d",
	"worklog-after":  "-8d",
	"worklog-before": "-2d",
	"started":        "2026-03-01T00:00:00Z",
	"start-date":     "2026-03-01T00:00:00Z",
	"end-date":       "2026-04-01T00:00:00Z",
	"parent":         "ENG-3",
	"epic":           "ENG-3",
	"assignee":       "currentUser",
	"reporter":       "currentUser",
	"creator":        "currentUser",
	"involving":      "currentUser",
	"watcher":        "currentUser",
	"voter":          "currentUser",
	"worklog-author": "currentUser",
	"was-assignee":   "currentUser",
	"changed-by":     "currentUser",
	"time-spent":     "2h",
	"board":          "2",
	"sprint":         "2",
	"page-size":      "25",
}

// probeByFlag is where a flag's value has to be shaped rather than merely
// present: a date, a key, a JQL fragment.
var probeByFlag = map[string]string{
	// A required date flag, and far enough back to include the fixture's own
	// timestamps. `issue activity` filters events by this as well as bounding
	// the query with it, so a window that excludes the fixture leaves the feed
	// empty and every other flag on the command reads as dead.
	"since":          "-3650d",
	"kind":           "comment",
	"jql":            "labels = probe",
	"sort":           "updated",
	"created-after":  "-7d",
	"created-before": "-1d",
	"updated-after":  "-7d",
	"updated-before": "-1d",
	"changed-after":  "-7d",
	"changed-before": "-1d",
	"worklog-after":  "-7d",
	"worklog-before": "-1d",
	"started":        "2026-01-01T00:00:00Z",
	"start-date":     "2026-01-01T00:00:00Z",
	"end-date":       "2026-02-01T00:00:00Z",
	"parent":         "ENG-2",
	"epic":           "ENG-2",
	"assignee":       "currentUser",
	"reporter":       "currentUser",
	"creator":        "currentUser",
	"involving":      "currentUser",
	"watcher":        "currentUser",
	"voter":          "currentUser",
	"worklog-author": "currentUser",
	"was-assignee":   "currentUser",
	"changed-by":     "currentUser",
	"time-spent":     "1h",
	"board":          "1",
	"sprint":         "1",
	"page-size":      "25",
}

func setProbe(flags registry.Flags, f registry.Flag, value string) {
	switch f.Type {
	case registry.TypeBool:
		flags.SetBool(f.Name, value == "true")
	case registry.TypeInt:
		n := 0
		for _, r := range value {
			n = n*10 + int(r-'0')
		}
		flags.SetInt(f.Name, n)
	default:
		flags.SetString(f.Name, value)
	}
}

// drive runs one command against a transport that answers everything and
// records what it was asked, and returns everything a caller could have seen.
//
// Validate runs first, exactly as the CLI runs it, because that is where a
// streaming command resolves what its columns will be — a flag whose only
// effect is a column moves nothing if the columns are read before Validate.
func drive(
	t *testing.T, c *registry.Command, kind site.Kind, testing string,
	set func(registry.Flags),
) string {
	t.Helper()

	rt := &recordingTransport{kind: kind}
	flags := registry.NewFlags()
	for _, f := range c.AllFlags() {
		// A mutating command previews rather than pretends to write: the
		// dry-run document *is* the request it would have sent, which is a
		// sharper observable than a fabricated 200.
		if f.Name == "dry-run" || f.Name == "yes" {
			// Except when either is under test. Forcing the flag itself on in
			// both runs is how a sweep reports --dry-run doing nothing, and
			// forcing --dry-run while --yes is under test does the same thing
			// one step removed: the gate lets a preview through unconfirmed,
			// which is deliberate — you look at what would happen in order to
			// decide whether to allow it — so a dry run makes --yes a no-op.
			if f.Name != testing && testing != "yes" {
				flags.SetBool(f.Name, true)
			}
			continue
		}
		// A required flag is part of the invocation, not part of what is being
		// varied. Leaving it out makes both runs the same refusal, and every
		// flag on the command reads as dead — which is how `jql explain`, whose
		// --jql is required, reported --sort and --order doing nothing.
		if f.Required && f.Name != testing {
			if v, _, ok := probePair(f); ok {
				setProbe(flags, f, v)
			}
		}
	}
	for name, value := range companionFlags[c.Name()] {
		if name == testing {
			continue
		}
		flags.SetString(name, value)
	}
	if set != nil {
		set(flags)
	}

	var args []string
	for _, a := range c.Args {
		if v := argProbe(c, a); v != "" {
			args = append(args, v)
		}
	}

	inv := &registry.Invocation{
		Jira:     sweepSession{rt: rt, kind: kind},
		Args:     args,
		Flags:    flags,
		Limit:    sweepLimit(t, c, flags),
		Format:   render.XML,
		Stderr:   io.Discard,
		Progress: registry.NoProgress,
	}

	var out strings.Builder
	failure := ""
	if err := runFor(t.Context(), c, inv, &out); err != nil {
		e := errs.Coerce(err)
		failure = e.Code + ": " + e.Message + " " + e.Detail
	}

	return freezeNow(strings.Join([]string{
		"requests:\n  " + strings.Join(rt.seen, "\n  "),
		"columns: " + columnsOf(c, inv),
		"output: " + out.String(),
		"failed: " + failure,
	}, "\n"))
}

// sweepLimit resolves --limit the way the CLI layer does, from the flag if the
// probe set one and from the command's declared default otherwise.
//
// The harness used to hardcode DefaultLimit here, which made --limit the one
// declared flag that could not move the fingerprint: the probe set a value in
// registry.Flags and the invocation was built from a constant, so the flag was
// read by nothing this sweep could see. It only became visible when --limit
// stopped being synthesised at the binder and started being declared like
// every other flag, which is the same fix in the other direction.
func sweepLimit(t *testing.T, c *registry.Command, flags registry.Flags) registry.Limit {
	t.Helper()
	if !c.Paginated {
		return registry.Limit{N: registry.DefaultLimit}
	}
	text := c.LimitFlag().Default
	if v := flags.String("limit"); v != "" {
		text = v
	}
	limit, err := registry.ParseLimit(text)
	if err != nil {
		t.Fatalf("the probe value %q for --limit does not parse: %v", text, err)
	}
	return limit
}

// nowish matches a timestamp in the shapes this tool sends and receives.
var nowish = regexp.MustCompile(
	`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})`)

// freezeNow replaces a timestamp the run just generated with a fixed word.
//
// `issue worklog add` stamps the current instant into its request, so two runs
// a millisecond apart differ — and a sweep that reads any difference as "the
// flag works" would pass every flag on that command, including a dead one. Only
// timestamps near now are frozen: a value a flag *put* there, like the probe's
// 2026-01-01, is the difference being measured and has to survive.
func freezeNow(s string) string {
	now := time.Now()
	return nowish.ReplaceAllStringFunc(s, func(stamp string) string {
		for _, layout := range []string{
			time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000-0700",
		} {
			t, err := time.Parse(layout, stamp)
			if err != nil {
				continue
			}
			if d := now.Sub(t); d > -time.Minute && d < time.Minute {
				return "<the instant this ran>"
			}
			return stamp
		}
		return stamp
	})
}

// runFor validates and then runs, streaming or not, the way the CLI does.
func runFor(
	ctx context.Context, c *registry.Command, inv *registry.Invocation, out *strings.Builder,
) error {
	// The gate, because it is what every real caller runs first and because
	// --yes has no other effect: the command body never reads it. A sweep that
	// skipped the gate would report the confirmation flag on four destructive
	// commands as dead.
	if err := registry.Gate(c, inv); err != nil {
		return err
	}
	if c.Validate != nil {
		if err := c.Validate(ctx, inv); err != nil {
			return err
		}
	}
	if c.Stream == nil {
		doc, err := c.Run(ctx, inv)
		if err != nil {
			return err
		}
		return render.Write(out, doc, render.XML)
	}

	stream, err := render.NewStream(out, render.XML, render.StreamSpec{
		Kind: c.Kind(), Version: c.KindVersion(),
		Name: c.CollectionName, Columns: columnsFor(c, inv),
	})
	if err != nil {
		return err
	}
	result, err := c.Stream(ctx, inv, stream)
	if err != nil {
		return err
	}
	return stream.Close(result.Complete, result.NextPageToken)
}

func columnsFor(c *registry.Command, inv *registry.Invocation) []render.Column {
	if c.ColumnsFor != nil {
		return c.ColumnsFor(inv)
	}
	return c.Columns
}

func columnsOf(c *registry.Command, inv *registry.Invocation) string {
	var names []string
	for _, col := range columnsFor(c, inv) {
		names = append(names, col.Header)
	}
	return strings.Join(names, ",")
}

// recordingTransport answers every request the same way and remembers what it
// was asked. The answer is deliberately thin: a command that cannot parse it
// fails, and the failure is identical in both runs, so the request it already
// made is still the difference the sweep is looking for.
type recordingTransport struct {
	kind site.Kind
	seen []string
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := ""
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = strings.TrimSpace(string(b))
	}
	query := r.URL.Query()
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+strings.Join(query[k], "|"))
	}
	rt.seen = append(rt.seen,
		r.Method+" "+r.URL.Path+"?"+strings.Join(parts, "&")+" "+body)

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(sweepResponse(r.URL.Path, rt.kind))),
		Request:    r,
	}, nil
}

// sweepResponse is a body of the right shape for each endpoint this sweep
// reaches.
//
// **None of this is evidence.** It asserts nothing about what Jira sends, and
// no test here may ever read it that way: a claim about the server belongs in a
// recorded cassette under a resource's testdata, named in the Data Center
// manifest. These exist for one reason — a flag whose effect is on a *value*
// cannot be seen when the value is absent. `--raw-body` converts a description
// and `--age` renders a timestamp, so against `{}` both are correctly invisible
// and the sweep would report two working flags as dead.
//
// Keep them minimal for that reason. A field nobody's flag depends on is a
// field this file is quietly asserting Jira sends.
func sweepResponse(path string, kind site.Kind) string {
	switch {
	// A fixed clock. `issue changes` bounds its window with the site's own
	// time and mints an absolute JQL bound from it, so a moving one would put
	// a different date in every request and make every flag on that command
	// read as alive.
	case strings.HasSuffix(path, "/serverInfo"):
		return `{"serverTime":"2026-08-17T09:30:00.000-0500"}`
	// Before everything, because a raw --jql is checked against the server in
	// Validate now and a clean verdict is what a site with nothing to complain
	// about answers. Data Center has no parse endpoint and asks the bounded
	// search below instead.
	case strings.HasSuffix(path, "/jql/parse"):
		return `{"queries":[{"query":"x","errors":[],"warnings":[]}]}`
	case strings.HasSuffix(path, "/field"):
		return `[` + sweepField("customfield_10042", "Story Points") + `,` +
			sweepField("customfield_10043", "Sprint Goal") + `]`
	case strings.Contains(path, "/user/"), strings.HasSuffix(path, "/user"),
		strings.Contains(path, "/user/search"):
		return `[` + sweepUser("5b10a2844c20165700ede21g", "Probe") + `,` +
			sweepUser("5b10a2844c20165700ede21h", "Second Probe") + `]`
	// Before /issue/, which would otherwise answer a changelog request with an
	// issue, and before /search, which `project/search` contains.
	case strings.HasSuffix(path, "/changelog"):
		return sweepPage(`"values"`, sweepHistory("10001", "In Progress"),
			sweepHistory("10002", "Done"))
	case strings.Contains(path, "/project/search"):
		return sweepPage(`"values"`, sweepProject("ENG", "Engineering"),
			sweepProject("OPS", "Operations"))
	case strings.Contains(path, "/search"):
		return `{"startAt":0,"maxResults":50,"total":2,"isLast":true,"issues":[` +
			sweepIssueKeyed(kind, "1", "ENG-2") + `,` +
			sweepIssueKeyed(kind, "2", "ENG-1") + `]}`
	case strings.HasSuffix(path, "/comment"):
		return `{"startAt":0,"maxResults":50,"total":2,"comments":[` +
			sweepComment("10000", kind) + `,` + sweepComment("10001", kind) + `]}`
	case strings.HasSuffix(path, "/worklog"):
		return `{"startAt":0,"maxResults":50,"total":2,"worklogs":[` +
			sweepWorklog("10000", kind) + `,` + sweepWorklog("10001", kind) + `]}`
	case strings.Contains(path, "/issueLinkType"):
		return `{"issueLinkTypes":[{"id":"10000","name":"Blocks",
			"inward":"is blocked by","outward":"blocks"},
			{"id":"10001","name":"Relates","inward":"relates to",
			"outward":"relates to"}]}`
	case strings.HasSuffix(path, "/myself"):
		return `{"accountId":"5b10a2844c20165700ede21g","name":"probe",
			"displayName":"Probe","emailAddress":"probe@example.invalid",
			"active":true,"timeZone":"UTC"}`
	// The two agile listings before the single-object routes they contain.
	case strings.HasSuffix(path, "/sprint"):
		return sweepPage(`"values"`, sweepSprint(1, "future"), sweepSprint(3, "future"))
	case strings.HasSuffix(path, "/epic"):
		return sweepPage(`"values"`,
			`{"id":10,"key":"ENG-10","name":"First epic","summary":"First epic","done":false}`,
			`{"id":11,"key":"ENG-11","name":"Second epic","summary":"Second epic","done":false}`)
	case strings.HasSuffix(path, "/sprint/2"):
		// Sprint 2 is the running one. Two states, because a verb's
		// precondition is part of what the harness has to satisfy: `sprint
		// start` refuses anything but a future sprint and `sprint close`
		// refuses anything but an active one, so one state cannot drive both
		// and the flags on whichever lost would read as dead.
		return sweepSprint(2, "active")
	case strings.Contains(path, "/sprint"):
		return sweepSprint(1, "future")
	case strings.HasSuffix(path, "/board"):
		return sweepPage(`"values"`,
			`{"id":1,"name":"First board","type":"scrum"}`,
			`{"id":2,"name":"Second board","type":"kanban"}`)
	case strings.Contains(path, "/board/"):
		return `{"id":1,"name":"First board","type":"scrum"}`
	case strings.HasSuffix(path, "/transitions"):
		return `{"transitions":[{"id":"31","name":"Done","hasScreen":false,
			"to":{"name":"Done","statusCategory":{"key":"done","name":"Done"}}},
			{"id":"41","name":"Blocked","hasScreen":false,
			"to":{"name":"Blocked","statusCategory":{"key":"indeterminate","name":"In Progress"}}}]}`
	// The type list first: `meta createmeta` resolves the name it was given
	// against it before asking for that type's fields, so a fixture without a
	// type called "probe" refuses at the first request and every flag on the
	// command is measured against that refusal.
	case strings.HasSuffix(path, "/issuetypes"):
		return sweepPage(`"values"`,
			`{"id":"10001","name":"probe","subtask":false}`,
			`{"id":"10002","name":"Bug","subtask":false}`)
	case strings.Contains(path, "/issuetypes/"):
		return sweepPage(`"values"`,
			`{"fieldId":"summary","required":true,"name":"Summary","schema":{"type":"string"}}`,
			`{"fieldId":"description","required":false,"name":"Description","schema":{"type":"string"}}`)
	case strings.Contains(path, "createmeta"):
		return `{"projects":[{"key":"ENG","issuetypes":[{"id":"10001","name":"Task",
			"fields":{"summary":{"required":true,"name":"Summary","schema":{"type":"string"}},
			"description":{"required":false,"name":"Description","schema":{"type":"string"}}}}]}],
			"fields":[{"fieldId":"summary","required":true,"name":"Summary",
			"schema":{"type":"string"}},{"fieldId":"description","required":false,
			"name":"Description","schema":{"type":"string"}}]}`
	case strings.Contains(path, "/statuses"):
		return `[{"id":"10001","name":"Task","statuses":[
			{"id":"1","name":"To Do","statusCategory":{"key":"new","name":"To Do"}},
			{"id":"3","name":"Done","statusCategory":{"key":"done","name":"Done"}}]},
			{"id":"10002","name":"Bug","statuses":[
			{"id":"1","name":"To Do","statusCategory":{"key":"new","name":"To Do"}}]}]`
	case strings.Contains(path, "/versions"):
		return `[{"id":"1","name":"1.0","archived":false,"released":true},
			{"id":"2","name":"2.0","archived":false,"released":false}]`
	case strings.Contains(path, "/components"):
		return `[{"id":"1","name":"api"},{"id":"2","name":"cli"}]`
	case strings.Contains(path, "/issue/"):
		return sweepIssue(kind)
	default:
		return "{}"
	}
}

// sweepPage wraps rows in the paged envelope the agile and project endpoints
// use. The count is the row count, so a bound below it genuinely truncates.
func sweepPage(key string, rows ...string) string {
	return `{"startAt":0,"maxResults":50,"total":` + strconv.Itoa(len(rows)) +
		`,"isLast":true,` + key + `:[` + strings.Join(rows, ",") + `]}`
}

func sweepField(id, name string) string {
	return `{"id":"` + id + `","name":"` + name + `","custom":true,
		"searchable":true,"orderable":true,"navigable":true,
		"clauseNames":["` + name + `"],"schema":{"type":"number"}}`
}

func sweepUser(id, display string) string {
	return `{"accountId":"` + id + `","name":"probe","displayName":"` + display + `",
		"emailAddress":"probe@example.invalid","active":true}`
}

func sweepProject(key, name string) string {
	return `{"id":"1","key":"` + key + `","name":"` + name + `","projectTypeKey":"software",
		"style":"classic","lead":{"accountId":"1","displayName":"Probe"}}`
}

func sweepSprint(id int, state string) string {
	return `{"id":` + strconv.Itoa(id) + `,"name":"Sprint ` + strconv.Itoa(id) +
		`","state":"` + state + `","originBoardId":1,
		"startDate":"2026-01-01T00:00:00.000Z",
		"endDate":"2026-01-15T00:00:00.000Z"}`
}

func sweepHistory(id, to string) string {
	return `{"id":"` + id + `","created":"2026-01-01T00:00:00.000+0000",
		"author":{"name":"probe","accountId":"1","displayName":"Probe"},
		"items":[{"field":"status","fieldtype":"jira","fromString":"To Do",
		"toString":"` + to + `"}]}`
}

func sweepComment(id string, kind site.Kind) string {
	return `{"id":"` + id + `","body":` + sweepBody(kind) + `,
		"created":"2026-01-01T00:00:00.000+0000",
		"updated":"2026-01-01T00:00:00.000+0000",
		"author":{"name":"probe","displayName":"Probe"}}`
}

func sweepWorklog(id string, kind site.Kind) string {
	return `{"id":"` + id + `","comment":` + sweepBody(kind) + `,
		"started":"2026-01-01T00:00:00.000+0000",
		"created":"2026-01-01T00:00:00.000+0000","timeSpentSeconds":3600,
		"author":{"name":"probe","displayName":"Probe"}}`
}

// sweepIssue carries exactly the values a flag on a read command renders: a
// timestamp for --age, a description for --raw-body, and a custom field for
// --field and --no-context-fields.
//
// The comment thread is here for `issue activity`, whose bodies come from the
// projection rather than from `description` — the same flag on the same fixture
// had nothing to convert, and a flag with nothing to act on reads exactly like
// a flag that does nothing.
func sweepIssue(kind site.Kind) string { return sweepIssueKeyed(kind, "1", "ENG-1") }

// sweepIssueKeyed is the same row with an identity, because two rows on one
// page must not share a key: the keyset ordering check refuses "ENG-1 followed
// ENG-1", correctly, and a fixture that trips it measures the check rather
// than the flag under test.
func sweepIssueKeyed(kind site.Kind, id, key string) string {
	return `{"id":"` + id + `","key":"` + key + `","fields":{"summary":"probe",
		"status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},
		"created":"2026-01-01T00:00:00.000+0000",
		"updated":"2026-01-01T00:00:00.000+0000",
		"description":` + sweepBody(kind) + `,"labels":["probe"],
		"attachment":[
			{"id":"10000","filename":"first.txt","size":12,"mimeType":"text/plain",
			 "created":"2026-01-01T00:00:00.000+0000",
			 "author":{"name":"probe","displayName":"Probe"}},
			{"id":"10001","filename":"second.txt","size":34,"mimeType":"text/plain",
			 "created":"2026-01-01T00:00:00.000+0000",
			 "author":{"name":"probe","displayName":"Probe"}}],
		"issuelinks":[
			{"id":"20000","type":{"id":"10000","name":"Blocks",
			 "inward":"is blocked by","outward":"blocks"},
			 "outwardIssue":{"key":"ENG-2","fields":{"summary":"second",
			  "status":{"name":"To Do","statusCategory":{"key":"new","name":"To Do"}}}}},
			{"id":"20001","type":{"id":"10001","name":"Relates",
			 "inward":"relates to","outward":"relates to"},
			 "inwardIssue":{"key":"ENG-3","fields":{"summary":"third",
			  "status":{"name":"To Do","statusCategory":{"key":"new","name":"To Do"}}}}}],
		"comment":{"total":1,"startAt":0,"maxResults":1,"comments":[
			{"id":"1","created":"2026-01-01T00:00:00.000+0000",
			 "updated":"2026-01-01T00:00:00.000+0000",
			 "author":{"name":"ada","accountId":"1","displayName":"Ada Lovelace"},
			 "body":` + sweepBody(kind) + `}]},
		"customfield_10042":7},
		"changelog":{"startAt":0,"maxResults":2,"total":2,"histories":[` +
		sweepHistory("30001", "In Progress") + `,` +
		sweepHistory("30002", "Done") + `]}}`
}

// sweepBody is the markup each deployment sends, and the difference is the
// whole reason this sweep drives both: wiki is reported as it arrived, and
// --raw-body only has something to choose between where the body is a document.
func sweepBody(kind site.Kind) string {
	if kind == site.Cloud {
		return `{"type":"doc","version":1,"content":[{"type":"paragraph",
			"content":[{"type":"text","text":"probe"}]}]}`
	}
	return `"h1. probe"`
}

// sweepSession hands out a live client pointed at the recording transport.
//
// It is a Data Center, because that is the deployment with the most branches:
// offset paging, keyset paging, v2 endpoints. Cloud differences are covered by
// the recorded cassettes, which is where a deployment difference belongs.
type sweepSession struct {
	rt   *recordingTransport
	kind site.Kind
}

func (s sweepSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	conn, err := transport.New(transport.Options{
		BaseURL:    sweepBase,
		HTTPClient: &http.Client{Transport: s.rt},
		Retries:    -1,
	})
	version := "10.4.0"
	if s.kind == site.Cloud {
		version = "1001.0.0"
	}
	return conn, site.Info{
		Kind: s.kind, Version: version, BaseURL: sweepBase,
	}, err
}

func (s sweepSession) Metadata(ctx context.Context) (*site.Metadata, error) {
	conn, info, err := s.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &site.Metadata{Client: conn, Info: info}, nil
}

func (sweepSession) CheckWritable(string) error      { return nil }
func (sweepSession) Project() string                 { return "ENG" }
func (sweepSession) Site() string                    { return "https://sweep.invalid" }
func (sweepSession) RequireProject() (string, error) { return "ENG", nil }
func (sweepSession) Board() string                   { return "1" }
func (sweepSession) RequireBoard() (string, error)   { return "1", nil }
func (sweepSession) Idempotency() *idem.Ledger       { return nil }

// Fields is a context with a field set, because the flag that opts out of one
// is invisible against a context that has none. It is the name rather than the
// id, which is the spelling a person stores and the one that has to resolve.
func (sweepSession) Fields() []string { return []string{"Story Points"} }

// sweepBase is a reserved TLD, like every host in this suite: a plausible
// hostname in a test is how tokens got sent to somebody else's server.
const sweepBase = "https://sweep.invalid"
