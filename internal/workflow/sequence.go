//go:build write

// A sequence is several changes to one issue, planned together and applied in
// order: GitHub issue 157. Closing a ticket the way a team expects is a
// comment, a field and a transition, three calls today, and a failure at the
// second leaves the issue half done with the repair falling to whoever
// notices.
//
// It lives here and not in internal/resource/issue because one of its steps,
// `sprint add`, lives here: a resource may not import this package, so the
// sequence comes to it.
//
// A step is written as the command it would have been, as argv, and parsed by
// that command's own declaration through registry.ParseArgv, the parser the
// command line uses. Nothing in the step format mirrors a flag, so a step
// accepts exactly what the command accepts.

package workflow

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
)

// SequenceStep is one step: its number, counted from one, the command it
// names, and its argv as that command parsed it.
type SequenceStep struct {
	Number  int
	Command *registry.Command
	// Argv is what was written after the command's path.
	Argv  []string
	Flags registry.Flags
	Args  []string
}

// MaxSequenceSteps bounds a sequence. It is reviewed once and then runs
// unattended, and a list longer than this is one nobody reads, which is the
// reason MaxPlanRows bounds a bulk plan.
const MaxSequenceSteps = 20

// stepKeys says which positional arguments of each v1 step command are the
// issues it acts on. The set is the commands a step may be, and nothing else:
// a destructive command never, because `--yes` confirms one command at a time
// and a sequence runs unattended once it has been read.
var stepKeys = map[string]func(args []string) []string{
	"issue.edit":        func(args []string) []string { return args },
	"issue.assign":      allButLast,
	"issue.comment.add": func(args []string) []string { return args[:1] },
	"issue.link.add":    linkEnds,
	"sprint.add":        func(args []string) []string { return args[1:] },
	"issue.move":        allButLast,
}

// allButLast is every argument before the last, which is the transition or
// the assignee. One argument alone is the last, and names no issue.
func allButLast(args []string) []string {
	if len(args) < 2 {
		return nil
	}
	return args[:len(args)-1]
}

// linkEnds is both issues a link names. One of them has to be the sequence's;
// the other is the issue it links to, and is the one key a step may name that
// is not the sequence's.
func linkEnds(args []string) []string { return []string{args[0], args[2]} }

// stepPlanFlags are flags a step command declares that a step cannot carry,
// because the sequence decides them for every step at once: it is planned and
// applied as one, it carries one baseline, and it derives each step's
// idempotency key.
var stepPlanFlags = []string{"dry-run", "plan-out", "apply", "if-unchanged", "idempotency-key"}

// StepCommands names the commands a step may be, in the order a refusal lists
// them.
func StepCommands() []string {
	names := make([]string, 0, len(stepKeys))
	for name := range stepKeys {
		names = append(names, strings.ReplaceAll(name, ".", " "))
	}
	slices.Sort(names)
	return names
}

// ParseSequenceSteps reads the steps of a sequence on key, as JSON: an array
// of steps, each an array of strings, the command line it would have been
// without the binary's name.
//
// Everything here is decided from the text and the declarations, with no
// request, so a sequence that could never run is refused before anything
// reads the issue. Each refusal names the step by number.
func ParseSequenceSteps(reg *registry.Registry, raw []byte, key string) ([]SequenceStep, error) {
	var argvs [][]string
	if err := json.Unmarshal(raw, &argvs); err != nil {
		return nil, errs.Usage("INVALID_STEPS",
			"the steps are not a JSON array of command lines").
			WithDetail("%s", err.Error()).
			WithRemedy(`write one array of strings per step, e.g. ` +
				`[["issue", "comment", "add", "ENG-1", "Shipped."]]`)
	}
	switch {
	case len(argvs) == 0:
		return nil, errs.Usage("INVALID_STEPS", "the sequence has no steps").
			WithRemedy("give at least one step")
	case len(argvs) > MaxSequenceSteps:
		return nil, errs.Usage("TOO_MANY_STEPS",
			"a sequence carries at most %d steps, and %d were given",
			MaxSequenceSteps, len(argvs)).
			WithRemedy("split it; a sequence longer than this is one nobody reads")
	}

	steps := make([]SequenceStep, 0, len(argvs))
	for i, argv := range argvs {
		step, err := parseStep(reg, i+1, argv, key)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, refuseMisplacedMoves(steps)
}

// parseStep reads one step: its command, its argv through that command's
// declaration, and the issues it names.
func parseStep(reg *registry.Registry, n int, argv []string, key string) (SequenceStep, error) {
	cmd, rest, err := stepCommand(reg, n, argv)
	if err != nil {
		return SequenceStep{}, err
	}
	flags, args, err := cmd.ParseArgv(rest)
	if err != nil {
		return SequenceStep{}, atStep(n, err)
	}
	for _, name := range stepPlanFlags {
		if _, declared := cmd.Flag(name); declared && flags.WasSet(name) {
			return SequenceStep{}, errs.Usage("STEP_TAKES_NO_PLAN_FLAG",
				"step %d: --%s is the sequence's to decide, not a step's", n, name).
				WithDetail("a sequence is planned and applied as one, carries one "+
					"baseline, and derives each step's idempotency key").
				WithRemedy("drop --%s from the step", name)
		}
	}
	if err := refuseOtherIssues(n, cmd, args, key); err != nil {
		return SequenceStep{}, err
	}
	return SequenceStep{Number: n, Command: cmd, Argv: rest, Flags: flags, Args: args}, nil
}

// stepCommand finds the command a step names: the longest run of its leading
// words that is a registered command a step may be.
func stepCommand(reg *registry.Registry, n int, argv []string) (*registry.Command, []string, error) {
	for words := min(len(argv), 3); words > 0; words-- {
		name := strings.Join(argv[:words], ".")
		cmd, ok := reg.Lookup(name)
		if !ok {
			continue
		}
		if _, allowed := stepKeys[name]; !allowed {
			return nil, nil, errs.Usage("STEP_NOT_ALLOWED",
				"step %d: %s cannot be a step", n, cmd.UseLine()).
				WithDetail("a step is one of: %s", strings.Join(StepCommands(), ", ")).
				WithRemedy("run %s on its own, before or after the sequence", cmd.UseLine())
		}
		return cmd, argv[words:], nil
	}
	return nil, nil, errs.Usage("STEP_NOT_ALLOWED",
		"step %d does not start with a command a step may be", n).
		WithDetail("a step is one of: %s; it was %q", strings.Join(StepCommands(), ", "),
			strings.Join(argv, " ")).
		WithRemedy("start the step with the command's words, without the binary's name")
}

// refuseOtherIssues holds a step to the sequence's issue. It may name that one
// issue and no other, except the far end of a link.
func refuseOtherIssues(n int, cmd *registry.Command, args []string, key string) error {
	named := stepKeys[cmd.Name()](args)
	if len(named) == 0 {
		return errs.Usage("NO_ISSUES", "step %d names no issue", n).
			WithRemedy("start the step's arguments with %s, as %s on its own would", key,
				cmd.UseLine())
	}
	if cmd.Name() == "issue.link.add" {
		if !slices.ContainsFunc(named, func(k string) bool { return sameKey(k, key) }) {
			return errs.Usage("STEP_NAMES_ANOTHER_ISSUE",
				"step %d links %s and %s, and neither is %s", n, named[0], named[1], key).
				WithRemedy("a sequence changes one issue; plan a sequence on the other " +
					"issue for its own changes")
		}
		return nil
	}
	if len(named) != 1 || !sameKey(named[0], key) {
		return errs.Usage("STEP_NAMES_ANOTHER_ISSUE",
			"step %d names %s, and the sequence is on %s", n,
			strings.Join(named, " "), key).
			WithRemedy("a sequence changes one issue; plan each issue's changes as " +
				"its own sequence, or several issues' as a bulk plan")
	}
	return nil
}

// sameKey compares two keys the way the issue resource reads them, so case
// and surrounding space do not make the sequence's own issue a stranger.
func sameKey(a, b string) bool {
	ka, okA := issue.ParseKey(a)
	kb, okB := issue.ParseKey(b)
	return okA && okB && ka == kb
}

// refuseMisplacedMoves allows one transition, as the last step.
//
// A transition is resolved against the issue's current status, so a second
// one cannot be resolved when the sequence is planned: the issue is not yet in
// the status it would start from. And every check runs against the issue as
// it is now, so a step after a transition would run against a status the
// checks never saw. The refusal names the split.
func refuseMisplacedMoves(steps []SequenceStep) error {
	var moves []int
	for _, s := range steps {
		if s.Command.Name() == "issue.move" {
			moves = append(moves, s.Number)
		}
	}
	if len(moves) == 0 {
		return nil
	}
	if len(moves) > 1 || moves[0] != len(steps) {
		return errs.Usage("TRANSITION_NOT_LAST",
			"a sequence carries one issue move, as its last step, and this one "+
				"moves at step %s of %d", joinInts(moves), len(steps)).
			WithDetail("a transition is resolved against the status the issue is in " +
				"when the sequence is planned, and a step after one would run " +
				"against a status nothing checked").
			WithRemedy("plan the steps up to the first move, apply them, then plan the rest")
	}
	return nil
}

// atStep says which step a command's own refusal is about, and keeps
// everything else it said: the code, the exit, the detail and the remedy.
func atStep(n int, err error) error {
	e := *errs.Coerce(err)
	e.Message = "step " + strconv.Itoa(n) + ": " + e.Message
	return &e
}

func joinInts(ns []int) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, " and ")
}
