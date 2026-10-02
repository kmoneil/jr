//go:build write

// Planning a sequence: every step dry-run as its own command, the checks the
// commands do not make, and a document holding exactly what each step would
// send.

package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/resource/sprint"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// The plan kind.
const (
	KindSequencePlan    = "issue.sequence.plan"
	VersionSequencePlan = 1
)

const (
	stepsFlag      = "steps"
	stepsFileFlag  = "steps-file"
	seqPlanOutFlag = "plan-out"
	seqApplyFlag   = "apply"
	// sequenceStepsKey is where Validate leaves the parsed steps for the body.
	sequenceStepsKey = "workflow.sequence.steps"
	// sequenceOperation names the sequence to the idempotency ledger, beside
	// the issue and the step, so a step's key is its own and no other
	// command's.
	sequenceOperation = "issue.sequence"
)

func init() {
	registry.Register(sequenceCommand())
	render.RegisterSchema(KindSequencePlan, SequencePlanSchema())
	render.RegisterSchema(KindSequenceApply, SequenceApplySchema())
}

// SequencePlanSchema is the shape of a sequence plan.
//
// Each step carries its argv as written, what was checked before it was
// written down, and the requests its command's own dry run produced, in the
// dry-run kind's shape: the change resolved, the transition id, the field ids,
// the assignee's account.
//
// **The requests are evidence, never instructions.** A plan of requests that
// an apply sent as written would make any file executable input carrying the
// caller's credential, which is why `issue.plan` carries intent only. So the
// argv is the intent, and an apply rebuilds every request through the step's
// own command, under the same checks, and refuses a plan whose rebuilt
// requests are not the ones recorded here. The plan a person reads is the
// change that runs, and a file somebody else wrote cannot make this tool send
// a request none of the six commands would build.
func SequencePlanSchema() *render.Schema {
	return &render.Schema{
		Element: "sequence",
		Attrs: []render.Field{
			{Name: "key", Type: render.TypeString},
			// Minted when the plan is written, and part of every step's
			// idempotency key: this file is the unit of at-most-once, so
			// re-applying it resumes and a new plan of the same steps runs.
			{Name: "plan-id", Type: render.TypeString},
			// The issue's `updated` when the plan was written, as the token
			// --if-unchanged takes. An apply compares it before the first step.
			{Name: "precondition", Type: render.TypeString},
		},
		Children: []render.Child{
			{Schema: render.ListSchema("steps", "step", &render.Schema{
				Element: "step",
				Attrs: []render.Field{
					{Name: "number", Type: render.TypeInt},
					{Name: "command", Type: render.TypeString},
					{Name: "idempotency-key", Type: render.TypeString},
				},
				Children: []render.Child{
					{Schema: render.ListSchema("argv", "arg", render.Leaf("arg", render.TypeString))},
					{Schema: render.ListSchema("checks", "check", render.Leaf("check", render.TypeString))},
					{Schema: registry.DryRunSchema()},
				},
			})},
		},
	}
}

func sequenceCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "sequence"},
		Summary: "Plan several changes to one issue, to apply in order",
		Description: strings.TrimSpace(`
Plans several changes to one issue as one document: a comment, a field and a
transition, which is how a team closes a ticket, or a sprint, a link and a
move. Each step is written as the command it would have been, as JSON, one
array of words per step, and is read by that command's own flags:

  [["issue", "comment", "add", "ENG-101", "Shipped in 1.4."],
   ["issue", "edit", "ENG-101", "--field", "Story Points=1"],
   ["issue", "move", "ENG-101", "Done", "--resolution", "Done"]]

A step is issue edit, issue assign, issue comment add, issue link add,
sprint add, or issue move, on this issue and no other (a link's far end
excepted). At most one move, as the last step: a transition is resolved
against the status the issue is in now, and a step after one would run against
a status nothing checked.

--plan-out writes the plan and sends nothing. Every step is dry-run as its own
command, which resolves what that command resolves, and then checked for what
the commands leave to Jira: the permission each step needs, every edited field
on the issue's edit screen, the assignee assignable here, a link's other issue
readable, the sprint open. A plan with a step that cannot run is refused, with
nothing written, naming every such step. The plan shows the exact request each
step resolved to, as evidence for the reader: nothing is ever sent from the
file as written.

--apply runs a plan. It sends nothing it reads from the file: every step is
rebuilt through its own command and checked again, and a plan whose rebuilt
requests are not the ones it recorded is refused as PLAN_DRIFTED before the
first step. The issue's baseline is compared first, so an issue changed since
planning is refused as STALE_WRITE with nothing sent. Steps then run in order,
and the first that fails stops the run: the steps before it are reported
applied, it is reported failed with its own code, and the rest not-attempted,
and the exit is its code. Re-running the same file resumes, sending only the
steps not yet done. --dry-run beside --apply prints every request the apply
would send, after the same checks, and sends nothing.

--steps takes the JSON inline, for a caller with no file to write;
--steps-file reads it from a file, or from stdin given -. A step carries no
global flag, no --dry-run, --plan-out, --if-unchanged or --idempotency-key:
the sequence runs in one context, against one site, under one baseline.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue sequence ENG-101 --steps-file close.json --plan-out close.xml",
			buildinfo.App + ` issue sequence ENG-101 --steps '[["issue","comment","add","ENG-101","Done."]]' --plan-out p.xml`,
			buildinfo.App + " issue sequence --apply close.xml --dry-run",
			buildinfo.App + " issue sequence --apply close.xml",
		}, "\n"),
		Args: []registry.Arg{
			{Name: "key", Usage: "the issue every step changes, e.g. ENG-101; --apply takes none"},
		},
		Flags: []registry.Flag{
			{
				Name: stepsFlag, Type: registry.TypeString,
				Usage: "the steps as JSON: an array of steps, each an array of the words " +
					"of a command line without the binary's name",
			},
			{
				Name: stepsFileFlag, Type: registry.TypeString,
				Usage: "read the steps from this file, or from stdin given -",
			},
			{
				Name: seqPlanOutFlag, Type: registry.TypeString,
				Usage: "check every step, write the plan to this file, and send nothing",
			},
			{
				Name: seqApplyFlag, Type: registry.TypeString,
				Usage: "run the plan in this file: every step rebuilt, checked and compared " +
					"with the plan before the first is sent; takes no key and no steps",
			},
			{
				Name: "dry-run", Type: registry.TypeBool,
				Usage: "with --apply, print every request the plan would send, and send nothing",
			},
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindSequencePlan, Version: VersionSequencePlan},
			{Kind: KindSequenceApply, Version: VersionSequenceApply, When: "--apply is given"},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateSequence,
		Run:       runSequence,
	}
}

// validateSequence refuses everything the text can decide, and leaves the
// parsed steps for the body.
//
// The key comes first, because a malformed identifier is refused by name
// before anything else is said about the invocation.
func validateSequence(_ context.Context, inv *registry.Invocation) error {
	if inv.Flags.String(seqApplyFlag) != "" {
		return validateSequenceApply(inv)
	}
	if len(inv.Args) != 1 {
		return errs.Usage("NO_ISSUES", "a sequence is on exactly one issue, and %d were given",
			len(inv.Args)).
			WithRemedy("name the issue every step changes")
	}
	key, ok := issue.ParseKey(inv.Args[0])
	if !ok {
		return errs.Usage("INVALID_KEY", "%q is not an issue key", inv.Args[0])
	}
	if inv.Flags.String(seqPlanOutFlag) == "" {
		return errs.Usage("SEQUENCE_NEEDS_A_PLAN",
			"issue sequence writes a plan with --%s, and sends nothing without one",
			seqPlanOutFlag).
			WithRemedy("add --%s <file>, read the plan, then apply it", seqPlanOutFlag)
	}
	if inv.Flags.Bool("dry-run") {
		return errs.Usage("CONFLICTING_PLAN_FLAGS",
			"--dry-run and --%s both send nothing and each produces a different document",
			seqPlanOutFlag).
			WithRemedy("--%s already sends nothing: it dry-runs every step", seqPlanOutFlag)
	}
	raw, err := readSteps(inv)
	if err != nil {
		return err
	}
	steps, err := parseStepsReadingNoStdin(raw, key.String())
	if err != nil {
		return err
	}
	inv.SetValue(sequenceStepsKey, steps)
	return nil
}

// parseStepsReadingNoStdin is ParseSequenceSteps, and the one refusal that
// depends on where the steps run rather than on what they say: a step cannot
// read stdin, which the sequence may already be reading for its steps, and an
// apply has none to read.
func parseStepsReadingNoStdin(raw []byte, key string) ([]SequenceStep, error) {
	steps, err := ParseSequenceSteps(registry.Default, raw, key)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		if _, has := s.Command.Flag("description-file"); has && s.Flags.String("description-file") == "-" {
			return nil, errs.Usage("STEP_READS_STDIN",
				"step %d reads its description from stdin, which a step cannot", s.Number).
				WithRemedy("name a file for --description-file, or give --description")
		}
	}
	return steps, nil
}

// readSteps is the JSON from --steps or --steps-file, exactly one of them.
func readSteps(inv *registry.Invocation) ([]byte, error) {
	inline, path := inv.Flags.String(stepsFlag), inv.Flags.String(stepsFileFlag)
	switch {
	case inline != "" && path != "":
		return nil, errs.Usage("STEPS_AND_STEPS_FILE",
			"--%s and --%s both give the steps", stepsFlag, stepsFileFlag).
			WithRemedy("give one of them")
	case inline != "":
		return []byte(inline), nil
	case path == "":
		return nil, errs.Usage("NO_STEPS", "a sequence needs its steps").
			WithRemedy("give --%s <json> or --%s <file>", stepsFlag, stepsFileFlag)
	case path == "-":
		if inv.Stdin == nil {
			return nil, errs.Usage("NO_STDIN",
				"--%s - reads stdin, and there is none here", stepsFileFlag).
				WithRemedy("give the steps inline with --%s", stepsFlag)
		}
		raw, err := io.ReadAll(inv.Stdin)
		if err != nil {
			return nil, errs.Runtime("STEPS_NOT_READ", "cannot read the steps from stdin").Wrap(err)
		}
		return raw, nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the caller named the file.
	if err != nil {
		return nil, errs.Usage("STEPS_NOT_READ", "cannot read the steps from %s", path).
			WithDetail("%v", err)
	}
	return raw, nil
}

// stepPlan is one step after the pre-flight: the requests its dry run
// produced and what was checked, or why it cannot run.
type stepPlan struct {
	step     SequenceStep
	requests *render.Node
	reqs     []transport.Request
	checks   []string
	blocked  error
}

func (p *stepPlan) block(err error) {
	if p.blocked == nil {
		p.blocked = atStep(p.step.Number, err)
	}
}

// runSequence writes the plan: the issue read for its baseline, every step
// dry-run and checked, and a refusal if any step cannot run.
func runSequence(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	if inv.Flags.String(seqApplyFlag) != "" {
		return runSequenceApply(ctx, inv)
	}
	steps, _ := inv.Value(sequenceStepsKey).([]SequenceStep)
	if inv.Jira == nil || len(steps) == 0 {
		return nil, errs.Runtime("NO_SESSION", "issue sequence has no connection to Jira")
	}
	key, _ := issue.ParseKey(inv.Args[0])
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	pf := &preflight{conn: conn, info: info, key: key.String()}

	// The issue first: a key that is not there is one answer, not one per step.
	precondition, err := pf.baseline(ctx)
	if err != nil {
		return nil, err
	}
	plans := make([]stepPlan, len(steps))
	for i, s := range steps {
		plans[i] = dryRun(ctx, inv, s)
	}
	if err := pf.check(ctx, plans); err != nil {
		return nil, err
	}
	if err := refuseBlocked(plans); err != nil {
		return nil, err
	}

	planID, err := newPlanID()
	if err != nil {
		return nil, err
	}
	doc := SequencePlanDoc(key.String(), planID, precondition, plans)
	if err := writeSequencePlan(doc, inv.Flags.String(seqPlanOutFlag)); err != nil {
		return nil, err
	}
	return doc, nil
}

// dryRun runs a step as its own command with --dry-run, under the sequence's
// session, and keeps the requests it would have sent. Its Validate runs first,
// as it would at a prompt, so the step resolves exactly what the command
// resolves and refuses what it refuses.
func dryRun(ctx context.Context, inv *registry.Invocation, s SequenceStep) stepPlan {
	p := stepPlan{step: s, checks: []string{"dry run"}}
	s.Flags.SetBool("dry-run", true)
	stepInv := &registry.Invocation{
		Jira: inv.Jira, Args: s.Args, Flags: s.Flags, Limit: inv.Limit,
		Format: inv.Format, Stderr: inv.Stderr, Progress: inv.Progress,
	}
	// The gate every caller of a command runs, so read-only refuses a step as
	// it refuses the command, whoever calls it.
	if err := registry.Gate(s.Command, stepInv); err != nil {
		p.block(err)
		return p
	}
	if s.Command.Validate != nil {
		if err := s.Command.Validate(ctx, stepInv); err != nil {
			p.block(err)
			return p
		}
	}
	doc, err := s.Command.Run(ctx, stepInv)
	if err != nil {
		p.block(err)
		return p
	}
	reqs, err := RequestsOf(doc)
	if err != nil {
		p.block(err)
		return p
	}
	p.requests, p.reqs = doc.Record, reqs
	return p
}

// refuseBlocked refuses the plan when any step cannot run. The code and exit
// are the first blocked step's own, the ones the command would have given on
// its own, and the detail names every blocked step, so one plan-and-fix cycle
// finds them all.
func refuseBlocked(plans []stepPlan) error {
	var blocked []stepPlan
	for _, p := range plans {
		if p.blocked != nil {
			blocked = append(blocked, p)
		}
	}
	if len(blocked) == 0 {
		return nil
	}
	first := *errs.Coerce(blocked[0].blocked)
	if len(blocked) > 1 {
		names := make([]string, 0, len(blocked))
		for _, p := range blocked {
			names = append(names, "step "+strconv.Itoa(p.step.Number)+" ("+errs.Coerce(p.blocked).Code+")")
		}
		first.Detail = strings.TrimSpace(first.Detail + "; blocked: " + strings.Join(names, ", "))
		first.Detail = strings.TrimPrefix(first.Detail, "; ")
	}
	return &first
}

// preflight is the reading the checks share: one site, one issue.
type preflight struct {
	conn *transport.Client
	info site.Info
	key  string
}

// get reads one path on the site as JSON.
func (pf *preflight) get(ctx context.Context, path string, query url.Values, into any) error {
	resp, err := pf.conn.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path, Query: query,
	})
	if err != nil {
		return err
	}
	if err := transport.Err(resp); err != nil {
		return err
	}
	if err := json.Unmarshal(resp.Body, into); err != nil {
		return errs.Remote("MALFORMED_RESPONSE", "%s answered with something that is not JSON", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	return nil
}

func (pf *preflight) issuePath(key string) string {
	return pf.info.APIBase() + "/issue/" + url.PathEscape(key)
}

// baseline reads the issue's `updated`, as the token an apply compares
// before its first step.
func (pf *preflight) baseline(ctx context.Context) (string, error) {
	var got struct {
		Fields struct {
			Updated string `json:"updated"`
		} `json:"fields"`
	}
	if err := pf.get(ctx, pf.issuePath(pf.key), url.Values{"fields": {"updated"}}, &got); err != nil {
		return "", err
	}
	token, err := issue.EncodePrecondition(pf.info, pf.key, got.Fields.Updated, issue.PrecisionMillisecond)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", errs.Usage("NO_BASELINE",
			"%s reported no updated time, so a plan for it has nothing to compare", pf.key).
			WithRemedy("run the steps as separate commands")
	}
	return token, nil
}

// stepPermissions is the permission each step command needs, by the names
// both deployments answer `mypermissions` with.
var stepPermissions = map[string]string{
	"issue.edit":        "EDIT_ISSUES",
	"issue.assign":      "ASSIGN_ISSUES",
	"issue.comment.add": "ADD_COMMENTS",
	"issue.link.add":    "LINK_ISSUES",
	"sprint.add":        "SCHEDULE_ISSUES",
	"issue.move":        "TRANSITION_ISSUES",
}

// check runs the checks the commands do not make, on every step whose dry run
// succeeded. A read that fails is the plan's failure, not a step's: it says
// nothing about whether the step could run.
func (pf *preflight) check(ctx context.Context, plans []stepPlan) error {
	perms, err := pf.permissions(ctx, plans)
	if err != nil {
		return err
	}
	var screen map[string]bool
	for i := range plans {
		p := &plans[i]
		if p.blocked != nil {
			continue
		}
		pf.checkPermissions(p, perms)
		if p.blocked != nil {
			continue
		}
		if err := pf.checkStep(ctx, p, &screen); err != nil {
			return err
		}
	}
	return nil
}

// checkStep runs the one check a step's command needs beyond its permission.
// The edit screen is read once, by the first edit that needs it.
func (pf *preflight) checkStep(ctx context.Context, p *stepPlan, screen *map[string]bool) error {
	switch p.step.Command.Name() {
	case "issue.edit":
		if *screen == nil {
			read, err := pf.editScreen(ctx)
			if err != nil {
				return err
			}
			*screen = read
		}
		checkOnScreen(p, *screen)
	case "issue.assign":
		return pf.checkAssignable(ctx, p)
	case "issue.link.add":
		pf.checkOtherEnd(ctx, p)
	case "sprint.add":
		pf.checkSprintOpen(ctx, p)
	}
	return nil
}

// permissions reads every permission the steps need in one request. Cloud
// requires `permissions=` and Data Center ignores it and answers with all of
// them, so it is sent always.
func (pf *preflight) permissions(ctx context.Context, plans []stepPlan) (map[string]bool, error) {
	wanted := []string{}
	for _, p := range plans {
		wanted = append(wanted, stepPermissions[p.step.Command.Name()])
		if p.step.Command.Name() == "issue.move" && p.step.Flags.String("resolution") != "" {
			wanted = append(wanted, "RESOLVE_ISSUES")
		}
	}
	slices.Sort(wanted)
	wanted = slices.Compact(wanted)

	var got struct {
		Permissions map[string]struct {
			HavePermission bool `json:"havePermission"`
		} `json:"permissions"`
	}
	query := url.Values{"issueKey": {pf.key}, "permissions": {strings.Join(wanted, ",")}}
	if err := pf.get(ctx, pf.info.APIBase()+"/mypermissions", query, &got); err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(wanted))
	for _, name := range wanted {
		perm, reported := got.Permissions[name]
		if !reported {
			return nil, errs.Remote("PERMISSION_NOT_REPORTED",
				"Jira did not say whether this account has %s on %s", name, pf.key).
				WithRemedy("run the steps as separate commands")
		}
		have[name] = perm.HavePermission
	}
	return have, nil
}

func (pf *preflight) checkPermissions(p *stepPlan, have map[string]bool) {
	needed := []string{stepPermissions[p.step.Command.Name()]}
	if p.step.Command.Name() == "issue.move" && p.step.Flags.String("resolution") != "" {
		needed = append(needed, "RESOLVE_ISSUES")
	}
	for _, name := range needed {
		if !have[name] {
			p.block(errs.Permission("PERMISSION_DENIED",
				"%s needs %s on %s, and this account does not have it",
				p.step.Command.UseLine(), name, pf.key).
				WithRemedy("ask a project administrator, or drop the step"))
			return
		}
		p.checks = append(p.checks, name)
	}
}

// editScreen is the set of field ids the issue's edit screen holds.
func (pf *preflight) editScreen(ctx context.Context) (map[string]bool, error) {
	var got struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := pf.get(ctx, pf.issuePath(pf.key)+"/editmeta", nil, &got); err != nil {
		return nil, err
	}
	screen := make(map[string]bool, len(got.Fields))
	for id := range got.Fields {
		screen[id] = true
	}
	return screen, nil
}

// checkOnScreen holds every field an edit step sets to the edit screen. Jira
// refuses a field that is not on it, and a sequence that found out at step
// two would leave step one applied.
func checkOnScreen(p *stepPlan, screen map[string]bool) {
	ids, err := editedFields(p.reqs)
	if err != nil {
		p.block(err)
		return
	}
	var off []string
	for _, id := range ids {
		if !screen[id] {
			off = append(off, id)
		}
	}
	if len(off) > 0 {
		p.block(errs.Usage("FIELD_NOT_ON_SCREEN",
			"%s sets %s, which the issue's edit screen does not hold",
			p.step.Command.UseLine(), strings.Join(off, ", ")).
			WithDetail("Jira refuses a field that is not on the edit screen, and a sequence " +
				"that found out mid-run would leave the steps before it applied").
			WithRemedy("drop the field, or ask an administrator to add it to the screen"))
		return
	}
	p.checks = append(p.checks, "on the edit screen: "+strings.Join(ids, ", "))
}

// editedFields is every field id an edit's requests set or update, sorted.
func editedFields(reqs []transport.Request) ([]string, error) {
	seen := map[string]bool{}
	for _, r := range reqs {
		var body struct {
			Fields map[string]json.RawMessage `json:"fields"`
			Update map[string]json.RawMessage `json:"update"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			return nil, errs.Runtime("MALFORMED_DRY_RUN", "the edit's dry run is not JSON").Wrap(err)
		}
		for id := range body.Fields {
			seen[id] = true
		}
		for id := range body.Update {
			seen[id] = true
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

// checkAssignable holds an assign step's account to the issue's assignable
// users. Data Center's `username` is a prefix search, so the answer is matched
// exactly, never trusted for being non-empty.
func (pf *preflight) checkAssignable(ctx context.Context, p *stepPlan) error {
	var body map[string]any
	if err := json.Unmarshal(p.reqs[0].Body, &body); err != nil {
		p.block(errs.Runtime("MALFORMED_DRY_RUN", "the assignment's dry run is not JSON").Wrap(err))
		return nil
	}
	param, field := "username", "name"
	if pf.info.Kind == site.Cloud {
		param, field = "accountId", "accountId"
	}
	who, _ := body[field].(string)
	if who == "" || who == "-1" {
		// Unassigning, or the project default: nobody to check.
		p.checks = append(p.checks, "nobody to check as assignable")
		return nil
	}
	var users []map[string]any
	query := url.Values{"issueKey": {pf.key}, param: {who}}
	if err := pf.get(ctx, pf.info.APIBase()+"/user/assignable/search", query, &users); err != nil {
		return err
	}
	for _, u := range users {
		if got, _ := u[field].(string); got == who {
			p.checks = append(p.checks, "assignable: "+who)
			return nil
		}
	}
	p.block(errs.Usage("USER_NOT_ASSIGNABLE", "%s cannot be assigned %s", who, pf.key).
		WithRemedy("assign somebody the project lets hold issues, or drop the step"))
	return nil
}

// checkOtherEnd reads a link's far issue, so a link to an issue the credential
// cannot see is found before the steps around it run.
func (pf *preflight) checkOtherEnd(ctx context.Context, p *stepPlan) {
	other := p.step.Args[2]
	if sameKey(other, pf.key) {
		other = p.step.Args[0]
	}
	var got struct {
		Key string `json:"key"`
	}
	if err := pf.get(ctx, pf.issuePath(other), url.Values{"fields": {"status"}}, &got); err != nil {
		if errs.ExitOf(err) == exitcode.NotFound {
			p.block(errs.NotFound("UNKNOWN_ISSUE", "%s is not an issue this credential can read", other).
				WithRemedy("check the key in the link"))
			return
		}
		p.block(err)
		return
	}
	p.checks = append(p.checks, "readable: "+got.Key)
}

// checkSprintOpen reads the sprint a sprint add step names. A closed sprint
// takes no more issues; that one command explains the refusal after the
// fact, and in a sequence the fact would be a half-applied issue.
func (pf *preflight) checkSprintOpen(ctx context.Context, p *stepPlan) {
	id := p.step.Args[0]
	current, err := (&sprint.Client{Transport: pf.conn, Site: pf.info}).Get(ctx, id)
	if err != nil {
		p.block(err)
		return
	}
	if current.State == sprint.StateClosed {
		p.block(errs.New(exitcode.Conflict, "SPRINT_CLOSED",
			"sprint %s is closed, and a closed sprint takes no more issues", current.ID).
			WithDetail("%q", current.Name).
			WithRemedy("name an open sprint; `%s sprint list --state active "+
				"--state future` lists them", buildinfo.App))
		return
	}
	p.checks = append(p.checks, "sprint "+current.ID+" is "+current.State)
}

// SequencePlanDoc renders a plan.
func SequencePlanDoc(key, planID, precondition string, plans []stepPlan) *render.Doc {
	items := make([]*render.Node, 0, len(plans))
	for _, p := range plans {
		argv := make([]*render.Node, 0, len(p.step.Argv))
		for _, word := range append(strings.Split(p.step.Command.UseLine(), " "), p.step.Argv...) {
			argv = append(argv, render.El("arg").SetText(word))
		}
		checks := make([]*render.Node, 0, len(p.checks))
		for _, c := range p.checks {
			checks = append(checks, render.El("check").SetText(c))
		}
		items = append(items, render.El("step").
			Attr("number", strconv.Itoa(p.step.Number)).
			Attr("command", p.step.Command.Name()).
			Attr("idempotency-key", StepKey(key, planID, p.step.Number)).
			Child(render.ListEl("argv", "arg", argv...)).
			Child(render.ListEl("checks", "check", checks...)).
			Child(p.requests))
	}
	return render.Record(KindSequencePlan, VersionSequencePlan, render.El("sequence").
		Attr("key", key).
		Attr("plan-id", planID).
		Attr("precondition", precondition).
		Child(render.ListEl("steps", "step", items...)))
}

// StepKey is a step's idempotency key: the sequence, the issue, the plan and
// the step. The plan id is what makes a re-run of the same file a resume and
// a new plan of the same steps a new run.
func StepKey(key, planID string, number int) string {
	return idem.DeriveKey(sequenceOperation, key, planID, strconv.Itoa(number))
}

// newPlanID mints a plan's id.
func newPlanID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", errs.Runtime("NO_RANDOMNESS", "cannot mint a plan id").Wrap(err)
	}
	return hex.EncodeToString(b), nil
}

// writeSequencePlan writes the file an apply will read back: XML whatever
// --format says, as a bulk plan is, because this file is written for this
// tool to read and one format is one parser.
func writeSequencePlan(doc *render.Doc, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the caller named the file.
	if err != nil {
		return errs.Runtime("PLAN_NOT_WRITTEN", "cannot write the plan to %s", path).Wrap(err)
	}
	defer func() { _ = f.Close() }()
	if err := render.Write(f, doc, render.XML); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return errs.Runtime("PLAN_NOT_WRITTEN", "cannot finish writing the plan to %s", path).Wrap(err)
	}
	return nil
}
