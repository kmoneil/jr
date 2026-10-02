//go:build write

package cli_test

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// modeFlags change what a command does rather than what it acts on: preview
// instead of send, plan instead of write, write only if unchanged, print raw
// bytes. Two of them on one command is a combination somebody has to have
// decided about, because the failure that shipped as 0.18.0's --apply with
// --dry-run was one flag silently overruling the other.
var modeFlags = []string{
	"apply", "description-file", "dry-run", "force", "idempotency-key",
	"if-unchanged", "no-body", "no-context-fields", "output", "plan-out",
	"raw-field", "remove", "subtasks", "yes",
}

// modePair is what a pair of mode flags on one command comes to. Exactly one
// of refused, test and open is set.
type modePair struct {
	// refused is the code the pair is refused with, which this sweep runs
	// and checks, along with each flag alone not producing it.
	refused string
	// test is the test that holds what the pair means, named so that a
	// combination nobody asserted cannot sit here looking decided.
	test string
	// open is a pair whose meaning is wrong or unasserted today, by the card
	// that tracks it. These are a ledger: maxOpenModePairs only falls.
	open string
	// means says what the pair does, for every entry that is not refused.
	means string
}

// modePairs is every pair of mode flags on a command, by "command --a --b"
// with the flags in order.
var modePairs = map[string]modePair{
	"issue.assign --apply --dry-run":      {refused: "CONFLICTING_PLAN_FLAGS"},
	"issue.assign --apply --if-unchanged": {refused: "PLAN_CARRIES_THE_BASELINE"},
	"issue.assign --apply --plan-out":     {refused: "CONFLICTING_PLAN_FLAGS"},
	"issue.assign --dry-run --if-unchanged": {
		test:  "TestADryRunComparesTheBaseline",
		means: "the preview compares, and a stale one is refused as the write would be",
	},
	"issue.assign --dry-run --plan-out": {refused: "CONFLICTING_PLAN_FLAGS"},
	"issue.assign --if-unchanged --plan-out": {
		test:  "TestAPlanComparesTheCallersBaseline",
		means: "the token is compared before the plan is built, against any issue it holds",
	},

	"issue.attachment.download --force --output": {
		test: "TestDownloadRefusesToOverwrite", means: "--force lets --output replace a file",
	},

	"issue.clone --dry-run --idempotency-key": {
		open:  "the-tests-could-not-fail-the-way-the-code-did",
		means: "a preview does not claim the key; true by the code, asserted only for move",
	},
	"issue.create --dry-run --idempotency-key": {
		open:  "the-tests-could-not-fail-the-way-the-code-did",
		means: "a preview does not claim the key; true by the code, asserted only for move",
	},

	"issue.comment.delete --dry-run --yes": {
		test: "TestADryRunNeedsNoConfirmation", means: "a preview needs no confirmation",
	},
	"issue.delete --dry-run --subtasks": {
		test:  "TestEveryFlagChangesWhatTheCommandDoes",
		means: "--subtasks still changes the request a preview prints",
	},
	"issue.delete --dry-run --yes": {
		test: "TestADryRunNeedsNoConfirmation", means: "a preview needs no confirmation",
	},
	"issue.delete --subtasks --yes": {
		test:  "TestEveryFlagChangesWhatTheCommandDoes",
		means: "--subtasks still changes the request with --yes given",
	},

	"issue.edit --apply --description-file": {refused: "PLAN_CARRIES_THE_CHANGE"},
	"issue.edit --apply --dry-run":          {refused: "CONFLICTING_PLAN_FLAGS"},
	"issue.edit --apply --if-unchanged":     {refused: "PLAN_CARRIES_THE_BASELINE"},
	"issue.edit --apply --plan-out":         {refused: "CONFLICTING_PLAN_FLAGS"},
	"issue.edit --description-file --dry-run": {
		open:  "the-tests-could-not-fail-the-way-the-code-did",
		means: "the preview carries the file's bytes; the flag sweep passes it only on a bad path",
	},
	"issue.edit --description-file --if-unchanged": {
		open:  "the-tests-could-not-fail-the-way-the-code-did",
		means: "the file's bytes are written only if the issue is unchanged",
	},
	"issue.edit --description-file --plan-out": {
		open:  "the-tests-could-not-fail-the-way-the-code-did",
		means: "the plan carries the file's bytes as the description",
	},
	"issue.edit --dry-run --if-unchanged": {
		test:  "TestADryRunComparesTheBaseline",
		means: "the preview compares, and a stale one is refused as the write would be",
	},
	"issue.edit --dry-run --plan-out": {refused: "CONFLICTING_PLAN_FLAGS"},
	"issue.edit --if-unchanged --plan-out": {
		test:  "TestAPlanComparesTheCallersBaseline",
		means: "the token is compared before the plan is built, against any issue it holds",
	},

	"issue.get --no-context-fields --raw-field": {refused: "RAW_FIELD_ALONE"},

	"issue.link.remove --dry-run --yes": {
		test: "TestADryRunNeedsNoConfirmation", means: "a preview needs no confirmation",
	},

	"issue.move --apply --dry-run":         {refused: "CONFLICTING_PLAN_FLAGS"},
	"issue.move --apply --idempotency-key": {refused: "PLAN_CARRIES_THE_CHANGE"},
	"issue.move --apply --if-unchanged":    {refused: "PLAN_CARRIES_THE_BASELINE"},
	"issue.move --apply --plan-out":        {refused: "CONFLICTING_PLAN_FLAGS"},
	"issue.move --dry-run --idempotency-key": {
		test: "TestADryRunDoesNotConsumeTheKey", means: "a preview does not claim the key",
	},
	"issue.move --dry-run --if-unchanged": {
		test:  "TestADryRunComparesTheBaseline",
		means: "the preview compares, and a stale one is refused as the move would be",
	},
	"issue.move --dry-run --plan-out": {refused: "CONFLICTING_PLAN_FLAGS"},
	// --plan-out already dry-runs every step, so a --dry-run beside it would
	// name a second preview of the same nothing.
	"issue.sequence --dry-run --plan-out": {refused: "CONFLICTING_PLAN_FLAGS"},
	"issue.sequence --apply --plan-out":   {refused: "CONFLICTING_PLAN_FLAGS"},
	// The opposite of the bulk verbs, deliberately: a sequence's apply
	// rebuilds every step, so its preview is a real one, built once here.
	"issue.sequence --apply --dry-run": {
		test:  "TestADryRunOfAnApplySendsNothing",
		means: "every request the apply would send, rebuilt and checked, and nothing sent",
	},
	"issue.move --idempotency-key --if-unchanged": {
		open:  "the-tests-could-not-fail-the-way-the-code-did",
		means: "claimed, compared, then sent; a stale refusal frees the key",
	},
	"issue.move --idempotency-key --plan-out": {refused: "CONFLICTING_PLAN_FLAGS"},
	"issue.move --if-unchanged --plan-out": {
		test:  "TestAPlanComparesTheCallersBaseline",
		means: "the token is compared before the plan is built, against any issue it holds",
	},

	"issue.watch --dry-run --remove": {
		test:  "TestEveryFlagChangesWhatTheCommandDoes",
		means: "--remove still changes the request a preview prints",
	},
	"issue.worklog.delete --dry-run --yes": {
		test: "TestADryRunNeedsNoConfirmation", means: "a preview needs no confirmation",
	},
	"sprint.close --dry-run --yes": {
		test: "TestADryRunNeedsNoConfirmation", means: "a preview needs no confirmation",
	},
}

// maxOpenModePairs is the ledger's ceiling. It only falls: a pair decided or
// fixed lowers it, and a new open pair has to be argued for in review rather
// than added by raising a number nobody reads.
const maxOpenModePairs = 6

// TestEveryModePairIsDecided holds every pair of mode flags on every command to
// a decision: refused, asserted by a named test, or open on a card.
//
// It exists because `issue edit --apply plan.xml --dry-run` applied the plan
// for real, a combination no test had ever set, and probing all of them the
// same day (_plans/probes/mode-pairs-2026-10-01.txt) found two more of the
// same shape at once: --if-unchanged is accepted and never compared beside
// --plan-out or --dry-run. A new mode flag, or a new command with two of them,
// fails here until somebody has decided what each pair means.
func TestEveryModePairIsDecided(t *testing.T) {
	for _, pair := range undecidedModePairs(cli.Registry().All()) {
		t.Errorf("%s is a pair of mode flags nobody has decided about; add it "+
			"to modePairs as refused, asserted by a named test, or open on a card",
			pair)
	}

	open := 0
	for key, p := range modePairs {
		name, _, _ := strings.Cut(key, " ")
		c, built := registry.Lookup(name)
		if !built {
			continue // Behind a tag this build does not have.
		}
		if !slices.Contains(modePairsOf(c), key) {
			t.Errorf("%s is in modePairs and is not a pair of mode flags this "+
				"command has; the declaration moved, delete the entry", key)
			continue
		}
		set := 0
		for _, v := range []string{p.refused, p.test, p.open} {
			if v != "" {
				set++
			}
		}
		switch {
		case set != 1:
			t.Errorf("%s must be exactly one of refused, test or open", key)
		case p.refused == "" && p.means == "":
			t.Errorf("%s does not say what the pair means", key)
		case p.test != "" && !testExists(t, p.test):
			t.Errorf("%s names %s, and no test has that name", key, p.test)
		case p.open != "":
			open++
		case p.refused != "":
			t.Run(key, func(t *testing.T) { requireRefusedPair(t, c, key, p.refused) })
		}
	}
	switch {
	case open > maxOpenModePairs:
		t.Errorf("%d mode pairs are open and the ledger allows %d; decide one "+
			"rather than raise the ceiling", open, maxOpenModePairs)
	case open < maxOpenModePairs:
		t.Errorf("%d mode pairs are open and the ceiling is %d; lower "+
			"maxOpenModePairs so the ledger cannot grow back", open, maxOpenModePairs)
	}
}

// TestTheModePairSweepCanFail is the negative control: a command with two mode
// flags that modePairs does not name has to be reported.
func TestTheModePairSweepCanFail(t *testing.T) {
	fake := &registry.Command{
		Path: []string{"probe", "modes"},
		Flags: []registry.Flag{
			{Name: "dry-run", Type: registry.TypeBool},
			{Name: "apply", Type: registry.TypeString},
		},
	}
	got := undecidedModePairs([]*registry.Command{fake})
	if !slices.Equal(got, []string{"probe.modes --apply --dry-run"}) {
		t.Errorf("an undecided pair was reported as %v, so the sweep cannot fail", got)
	}
}

// modePairsOf lists a command's pairs of mode flags as modePairs keys.
func modePairsOf(c *registry.Command) []string {
	var mine []string
	for _, f := range c.AllFlags() {
		if slices.Contains(modeFlags, f.Name) {
			mine = append(mine, f.Name)
		}
	}
	slices.Sort(mine)
	var pairs []string
	for i := range mine {
		for j := i + 1; j < len(mine); j++ {
			pairs = append(pairs, c.Name()+" --"+mine[i]+" --"+mine[j])
		}
	}
	return pairs
}

func undecidedModePairs(cmds []*registry.Command) []string {
	var out []string
	for _, c := range cmds {
		for _, key := range modePairsOf(c) {
			if _, decided := modePairs[key]; !decided {
				out = append(out, key)
			}
		}
	}
	return out
}

// requireRefusedPair runs the pair and requires the refusal it is listed with,
// and runs each flag alone to require that neither produces it: a pair
// "refused" because one of its flags is refused on its own has decided
// nothing.
func requireRefusedPair(t *testing.T, c *registry.Command, key, want string) {
	t.Helper()
	fields := strings.Fields(key)
	a, b := strings.TrimPrefix(fields[1], "--"), strings.TrimPrefix(fields[2], "--")
	if got := modeOutcome(t, c, a, b); got != want {
		t.Errorf("%s answered %q, want the refusal %q", key, got, want)
	}
	for _, alone := range []string{a, b} {
		if got := modeOutcome(t, c, alone); got == want {
			t.Errorf("--%s alone is refused %q too, so the pair %s proves nothing",
				alone, want, key)
		}
	}
}

// modeOutcome runs a command with the named mode flags set and returns the
// error code it ended with, or "" when it succeeded. An --apply takes no keys,
// so a run that sets one is given none.
func modeOutcome(t *testing.T, c *registry.Command, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	desc := filepath.Join(dir, "description.txt")
	if err := os.WriteFile(desc, []byte("from a file\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	token, err := issue.EncodePrecondition(
		site.Info{Kind: site.DataCenter, BaseURL: sweepBase},
		"ENG-1", "2026-08-04T11:32:07.000+0000", issue.PrecisionSecond)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	value := map[string]string{
		"apply": filepath.Join(dir, "plan.xml"), "plan-out": filepath.Join(dir, "out.xml"),
		"if-unchanged": token, "idempotency-key": "probe-1",
		"description-file": desc, "raw-field": "summary",
		"output": filepath.Join(dir, "download"),
	}

	flags := registry.NewFlags()
	for _, f := range c.AllFlags() {
		if f.Required {
			if v, _, ok := probePair(f); ok {
				setProbe(flags, f, v)
			}
		}
	}
	for _, name := range names {
		f, _ := c.Flag(name)
		if f.Type == registry.TypeBool {
			flags.SetBool(name, true)
		} else {
			flags.SetString(name, value[name])
		}
	}
	var args []string
	if !slices.Contains(names, "apply") {
		for _, a := range c.Args {
			if v := argProbe(c, a); v != "" {
				args = append(args, v)
			}
		}
	}
	rt := &recordingTransport{kind: site.DataCenter}
	inv := &registry.Invocation{
		Jira: sweepSession{rt: rt, kind: site.DataCenter}, Args: args, Flags: flags,
		Limit: sweepLimit(t, c, flags), Format: render.XML,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	var out strings.Builder
	if err := runFor(t.Context(), c, inv, &out); err != nil {
		return errs.Coerce(err).Code
	}
	return ""
}

// testFunc is a test's declaration line.
var testFunc = regexp.MustCompile(`(?m)^func (Test\w+)\(`)

// testExists reports whether any test in the module has the name.
func testExists(t *testing.T, name string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return err
		}
		if d.IsDir() && (d.Name() == "_plans" || d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path) //nolint:gosec // a path the walk found under the module.
		if err != nil {
			return err
		}
		for _, m := range testFunc.FindAllStringSubmatch(string(src), -1) {
			if m[1] == name {
				found = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return found
}
