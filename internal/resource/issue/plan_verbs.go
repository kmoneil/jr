//go:build write

package issue

import (
	"context"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// The move and assign verbs through the plan machinery. The shapes mirror
// issue edit's; what differs per verb is the change set, and for move the
// per-row work: a transition is a fact about one issue's workflow, so the
// plan resolves it for every row and records the id, or the reason there is
// none, which is what makes the plan the thing read instead of finding out.

// lastArgSplit reads the argument rule move and assign share: every argument
// before the last is a key, and the last is the transition or the assignee.
func lastArgSplit(inv *registry.Invocation) (keys []string, last string) {
	n := len(inv.Args)
	if n == 0 {
		return nil, ""
	}
	return inv.Args[:n-1], strings.TrimSpace(inv.Args[n-1])
}

// validateVerbShape is validateEditShape for move and assign: the plan-flag
// conflicts, the apply mode that takes no keys and no change flags, and the
// rule that more than one key goes through a plan. lastName names the final
// argument in refusals; changeFlags are the verb's own string flags, which a
// plan carries and an apply therefore refuses.
func validateVerbShape(inv *registry.Invocation, lastName string, changeFlags []string) error {
	apply := inv.Flags.String(applyFlagName)
	planOut := inv.Flags.String(planOutFlagName)

	if apply != "" && planOut != "" {
		return errs.Usage("CONFLICTING_PLAN_FLAGS",
			"--"+planOutFlagName+" writes a plan and --"+applyFlagName+" runs one").
			WithRemedy("write it in one invocation and run it in another")
	}
	if apply != "" {
		return validateVerbApplyShape(inv, changeFlags)
	}

	if len(inv.Args) < 2 {
		return errs.Usage("NO_ISSUES",
			"expected at least one issue key followed by the %s", lastName).
			WithRemedy("every argument before the last is a key; the last is "+
				"the %s", lastName)
	}
	keys, _ := lastArgSplit(inv)
	if planOut != "" {
		if inv.Flags.Bool("dry-run") {
			return errs.Usage("CONFLICTING_PLAN_FLAGS",
				"--dry-run and --"+planOutFlagName+" both send nothing and each "+
					"produces a different document").
				WithRemedy("--dry-run to read the request, --" + planOutFlagName +
					" to write a plan you can apply")
		}
		if len(keys) > MaxPlanRows {
			return errs.Usage("TOO_MANY_ISSUES",
				"a plan carries at most %d issues, and %d were given",
				MaxPlanRows, len(keys)).
				WithRemedy("split the set; a plan longer than this is one nobody " +
					"reads, which is what planning is for")
		}
		return nil
	}
	if len(keys) > 1 {
		return errs.Usage("BULK_NEEDS_A_PLAN",
			"%d issues were given, and changing more than one goes through a plan",
			len(keys)).
			WithRemedy("add --" + planOutFlagName + " <file> to write one, read " +
				"it, then run it with --" + applyFlagName)
	}
	return nil
}

// validateVerbApplyShape refuses what an apply cannot take: keys, the
// verb's own change flags, and a baseline, because the plan carries all
// three.
func validateVerbApplyShape(inv *registry.Invocation, changeFlags []string) error {
	if len(inv.Args) > 0 {
		return errs.Usage("PLAN_TAKES_NO_KEYS",
			"--"+applyFlagName+" takes its issues from the plan, and %d were "+
				"also given on the command line", len(inv.Args)).
			WithRemedy("drop the arguments, or plan them into a new file")
	}
	for _, name := range changeFlags {
		if inv.Flags.String(name) != "" {
			return errs.Usage("PLAN_CARRIES_THE_CHANGE",
				"--%s runs the change recorded in the plan, so --%s cannot "+
					"be given as well", applyFlagName, name).
				WithRemedy("plan the change you want, read it, then apply that file")
		}
	}
	if inv.Flags.String(ifUnchangedFlag) != "" {
		return errs.Usage("PLAN_CARRIES_THE_BASELINE",
			"--"+applyFlagName+" checks the baseline recorded on each row, so "+
				"--"+ifUnchangedFlag+" cannot be given as well").
			WithRemedy("the plan already holds one baseline per issue")
	}
	return nil
}

// planKeys normalises the key arguments, after validation has refused
// anything ParseKey rejects.
func planKeys(args []string) []string {
	keys := make([]string, 0, len(args))
	for _, arg := range args {
		key, _ := ParseKey(arg)
		keys = append(keys, key.String())
	}
	return keys
}

// BuildMovePlan resolves every row's baseline and its transition.
//
// One transitions read per row, deliberately: the id valid for one issue may
// not exist for another, because workflows differ by project and issue type.
// The row's precondition is what keeps a plan-time id honest at apply time:
// an issue that moves after planning is refused as stale before the id is
// ever sent, which is the same freshness the interactive command buys by
// never caching the list.
//
// The resolution is checked against each row's own transition screen, since
// one workflow's screen can take a resolution and another's cannot. The
// change then carries the site's spelling, because apply sends exactly what
// the plan says and Jira matches the name case-sensitively. A resolution is
// one site-wide object, so the rows agree on how it is spelled.
func BuildMovePlan(
	ctx context.Context, inv *registry.Invocation, client *Client, info site.Info,
	keys []string, change MoveChange,
) (*Plan, error) {
	updated, err := versionsFor(ctx, client, keys)
	if err != nil {
		return nil, err
	}
	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return nil, err
	}

	fp := fingerprint([]string{
		"transition=" + change.Transition,
		"resolution=" + change.Resolution,
		"comment=" + change.Comment,
	})
	rows := make([]PlanRow, 0, len(keys))
	spelled := ""
	for _, key := range keys {
		row, err := baselineRow(info, updated, planVerbMove, key, fp)
		if err != nil {
			return nil, err
		}
		transitions, err := meta.Transitions(ctx, key)
		if err != nil {
			return nil, err
		}
		transition, resolution, err := resolveMove(transitions, change)
		if err != nil {
			row.Blocked = errs.Coerce(err).Message
		} else {
			row.Transition = transition.ID
			// Only a spelling some screen listed, which is one with an id:
			// every resolution Jira lists carries one. A screen that listed
			// nothing passes the input through, and that is not the site's
			// spelling.
			if spelled == "" && resolution.ID != "" {
				spelled = resolution.Name
			}
		}
		rows = append(rows, row)
	}
	if spelled != "" {
		change.Resolution = spelled
	}
	return &Plan{Verb: planVerbMove, Move: change, Rows: rows}, nil
}

// BuildAssignPlan resolves every row's baseline. The assignee arrives
// already resolved to the id this deployment uses, or a sentinel word, so
// the same plan means the same person on every day it is applied.
func BuildAssignPlan(
	ctx context.Context, client *Client, info site.Info, keys []string, assignee string,
) (*Plan, error) {
	updated, err := versionsFor(ctx, client, keys)
	if err != nil {
		return nil, err
	}
	fp := fingerprint([]string{"assignee=" + assignee})
	rows := make([]PlanRow, 0, len(keys))
	for _, key := range keys {
		row, err := baselineRow(info, updated, planVerbAssign, key, fp)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return &Plan{Verb: planVerbAssign, Assignee: assignee, Rows: rows}, nil
}

// baselineRow is one planned row before any verb-specific work: the baseline
// from the one search every plan spends, and the idempotency key derived
// from the verb, the key, and the change fingerprint.
func baselineRow(
	info site.Info, updated map[string]string, verb, key, fp string,
) (PlanRow, error) {
	raw, found := updated[key]
	if !found {
		// A key the search did not return is one the credential cannot see
		// or one that does not exist. Planning it would put a row in the
		// document that apply is certain to fail.
		return PlanRow{}, errs.NotFound("UNKNOWN_ISSUE",
			"%s is not an issue this credential can read", key).
			WithRemedy("check the key, or drop it from the set")
	}
	// Second, because the baseline came from a search: on Data Center the
	// index keeps only the second, and a plan whose baselines claimed the
	// millisecond failed every row it had.
	token, err := EncodePrecondition(info, key, raw, PrecisionSecond)
	if err != nil {
		return PlanRow{}, err
	}
	return PlanRow{
		Key:            key,
		Precondition:   token,
		IdempotencyKey: idem.DeriveKey(verb, key, fp),
	}, nil
}

// runMovePlanOut writes a move plan for the keys, resolving nothing it can
// avoid and sending nothing at all.
func runMovePlanOut(
	ctx context.Context, inv *registry.Invocation, client *Client, info site.Info,
	path string,
) (*render.Doc, error) {
	args, transition := lastArgSplit(inv)
	change := MoveChange{
		Transition: transition,
		Resolution: inv.Flags.String("resolution"),
		Comment:    inv.Flags.String("comment"),
	}
	plan, err := BuildMovePlan(ctx, inv, client, info, planKeys(args), change)
	if err != nil {
		return nil, err
	}
	doc := PlanDoc(plan)
	if err := writePlan(doc, path); err != nil {
		return nil, err
	}
	return doc, nil
}

// runAssignPlanOut writes an assign plan for the keys.
func runAssignPlanOut(
	ctx context.Context, inv *registry.Invocation, client *Client, info site.Info,
	path string,
) (*render.Doc, error) {
	args, last := lastArgSplit(inv)
	plan, err := BuildAssignPlan(ctx, client, info, planKeys(args),
		resolvedAssignee(inv, last))
	if err != nil {
		return nil, err
	}
	doc := PlanDoc(plan)
	if err := writePlan(doc, path); err != nil {
		return nil, err
	}
	return doc, nil
}
