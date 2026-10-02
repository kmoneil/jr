//go:build write

// Applying a sequence plan: the baseline compared, every step rebuilt through
// its own command and held to the plan, then sent in order, stopping at the
// first failure.

package workflow

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
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
	"github.com/kmoneil/jr/internal/transport"
)

// The kind an apply reports.
const (
	KindSequenceApply    = "issue.sequence"
	VersionSequenceApply = 1
)

// What one step of an apply came to. Unlike a bulk apply, which attempts every
// row, a sequence stops at its first failure: its steps are one change to one
// issue, in an order the plan chose, and a step after a failed one would run
// against an issue the plan never described.
const (
	StepApplied      = "applied"
	StepSkipped      = "skipped"
	StepFailed       = "failed"
	StepNotAttempted = "not-attempted"
)

// How the baseline was honoured.
const (
	// BaselineCompared is a first run: the issue's `updated` was compared
	// with the plan's before the first step, as --if-unchanged compares.
	BaselineCompared = "compared"
	// BaselineRechecked is a resume: the plan's own completed steps moved the
	// issue, so the baseline cannot be compared, and every step still to run
	// was rebuilt and checked again instead.
	BaselineRechecked = "re-checked"
)

// sequencePlanKey is where Validate leaves the plan it read.
const sequencePlanKey = "workflow.sequence.plan"

// SequenceApplySchema is what an apply reports: the plan, how its baseline was
// honoured, and every step's outcome.
func SequenceApplySchema() *render.Schema {
	return &render.Schema{
		Element: "sequence",
		Attrs: []render.Field{
			{Name: "key", Type: render.TypeString},
			{Name: "plan-id", Type: render.TypeString},
			{
				Name: "baseline", Type: render.TypeString,
				Enum: []string{BaselineCompared, BaselineRechecked},
			},
			{Name: "requested", Type: render.TypeInt},
			{Name: "applied", Type: render.TypeInt},
			{Name: "skipped", Type: render.TypeInt},
			{Name: "failed", Type: render.TypeInt},
			{Name: "not-attempted", Type: render.TypeInt},
		},
		Children: []render.Child{
			{Schema: render.ListSchema("steps", "step", &render.Schema{
				Element: "step",
				Attrs: []render.Field{
					{Name: "number", Type: render.TypeInt},
					{Name: "command", Type: render.TypeString},
					{
						Name: "outcome", Type: render.TypeString,
						Enum: []string{StepApplied, StepSkipped, StepFailed, StepNotAttempted},
					},
					// The failed step's own code, the one its command would
					// have given at a prompt. Absent on every other outcome.
					{Name: "code", Type: render.TypeString, Optional: true},
				},
			})},
		},
	}
}

// SequencePlan is a plan read back from its file.
type SequencePlan struct {
	Key          string
	PlanID       string
	Precondition string
	Steps        []PlannedStep
}

// PlannedStep is one step of a plan: the step its argv parses to, its
// idempotency key, and the requests the plan recorded for it, which are what
// the rebuilt requests are held to and never what is sent.
type PlannedStep struct {
	SequenceStep
	IdempotencyKey string
	Recorded       []transport.Request
}

// sequencePlanDoc mirrors SequencePlanSchema for decoding.
type sequencePlanDoc struct {
	XMLName  xml.Name `xml:"result"`
	Kind     string   `xml:"kind,attr"`
	Version  int      `xml:"v,attr"`
	Sequence struct {
		Key          string `xml:"key,attr"`
		PlanID       string `xml:"plan-id,attr"`
		Precondition string `xml:"precondition,attr"`
		Steps        struct {
			Step []struct {
				Number         int    `xml:"number,attr"`
				Command        string `xml:"command,attr"`
				IdempotencyKey string `xml:"idempotency-key,attr"`
				Argv           struct {
					Arg []string `xml:"arg"`
				} `xml:"argv"`
				Requests struct {
					Request []recordedRequest `xml:"request"`
				} `xml:"requests"`
			} `xml:"step"`
		} `xml:"steps"`
	} `xml:"sequence"`
}

type recordedRequest struct {
	Method string `xml:"method,attr"`
	Path   string `xml:"path,attr"`
	Query  *struct {
		Param []struct {
			Name  string `xml:"name,attr"`
			Value string `xml:",chardata"`
		} `xml:"param"`
	} `xml:"query"`
	Body *struct {
		ContentType string `xml:"content-type,attr"`
		Text        string `xml:",chardata"`
	} `xml:"body"`
}

func (r recordedRequest) request() transport.Request {
	out := transport.Request{Method: r.Method, Path: r.Path}
	if r.Query != nil {
		out.Query = url.Values{}
		for _, p := range r.Query.Param {
			out.Query.Add(p.Name, p.Value)
		}
	}
	if r.Body != nil {
		out.Body = []byte(r.Body.Text)
		out.Header = map[string][]string{"Content-Type": {r.Body.ContentType}}
	}
	return out
}

func invalidPlan(format string, args ...any) *errs.Error {
	return errs.Usage("INVALID_PLAN", format, args...).
		WithRemedy("pass the file `" + buildinfo.App + " issue sequence --plan-out` wrote")
}

// ParseSequencePlan reads a plan as strictly as it was written. The file is
// input somebody could have edited, so everything in it is held to what
// --plan-out would have produced: the kind and version, the key, a key per
// step derived from the plan id, and every step's argv through every rule a
// typed sequence meets. The recorded requests are read only to be compared.
func ParseSequencePlan(r io.Reader) (*SequencePlan, error) {
	var doc sequencePlanDoc
	if err := xml.NewDecoder(r).Decode(&doc); err != nil {
		return nil, invalidPlan("this is not a document this tool wrote").WithDetail("%v", err)
	}
	key, err := planHeader(doc)
	if err != nil {
		return nil, err
	}
	argvs, err := planArgvs(doc, key)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(argvs)
	if err != nil {
		return nil, errs.Runtime("ENCODE_FAILED", "cannot re-read the plan's steps").Wrap(err)
	}
	steps, err := parseStepsReadingNoStdin(raw, key)
	if err != nil {
		return nil, err
	}
	return plannedSteps(doc, key, steps)
}

// planHeader holds a plan's envelope to what --plan-out writes, and returns
// its issue.
func planHeader(doc sequencePlanDoc) (string, error) {
	switch {
	case doc.Kind != KindSequencePlan:
		return "", invalidPlan("this is a %s document, not a sequence plan", doc.Kind)
	case doc.Version != VersionSequencePlan:
		return "", invalidPlan("this plan is version %d and this build reads version %d",
			doc.Version, VersionSequencePlan)
	}
	seq := doc.Sequence
	key, ok := issue.ParseKey(seq.Key)
	switch {
	case !ok:
		return "", invalidPlan("%q is not an issue key", seq.Key)
	case seq.PlanID == "":
		return "", invalidPlan("this plan has no plan id")
	case seq.Precondition == "":
		return "", errs.Usage("NO_BASELINE", "this plan carries no baseline for %s", key).
			WithRemedy("plan it again")
	}
	return key.String(), nil
}

// planArgvs holds each step's number, key and recording to what --plan-out
// writes, and returns the steps' argvs for the rules a typed sequence meets.
func planArgvs(doc sequencePlanDoc, key string) ([][]string, error) {
	seq := doc.Sequence
	argvs := make([][]string, 0, len(seq.Steps.Step))
	for i, s := range seq.Steps.Step {
		switch {
		case s.Number != i+1:
			return nil, invalidPlan("step %d is numbered %d", i+1, s.Number)
		case s.IdempotencyKey != StepKey(key, seq.PlanID, s.Number):
			return nil, invalidPlan("step %d carries an idempotency key that is not this plan's",
				s.Number)
		case len(s.Requests.Request) == 0:
			return nil, invalidPlan("step %d records no request", s.Number)
		}
		argvs = append(argvs, s.Argv.Arg)
	}
	return argvs, nil
}

// plannedSteps pairs each parsed step with its key and recorded requests.
func plannedSteps(doc sequencePlanDoc, key string, steps []SequenceStep) (*SequencePlan, error) {
	seq := doc.Sequence
	plan := &SequencePlan{Key: key, PlanID: seq.PlanID, Precondition: seq.Precondition}
	for i, s := range seq.Steps.Step {
		if steps[i].Command.Name() != s.Command {
			return nil, invalidPlan("step %d says it is %s and its argv is %s",
				s.Number, s.Command, steps[i].Command.Name())
		}
		recorded := make([]transport.Request, 0, len(s.Requests.Request))
		for _, r := range s.Requests.Request {
			recorded = append(recorded, r.request())
		}
		plan.Steps = append(plan.Steps, PlannedStep{
			SequenceStep: steps[i], IdempotencyKey: s.IdempotencyKey, Recorded: recorded,
		})
	}
	return plan, nil
}

// validateSequenceApply refuses what an apply cannot take, and reads the plan.
func validateSequenceApply(inv *registry.Invocation) error {
	switch {
	case inv.Flags.String(seqPlanOutFlag) != "":
		return errs.Usage("CONFLICTING_PLAN_FLAGS",
			"--%s writes a plan and --%s runs one", seqPlanOutFlag, seqApplyFlag).
			WithRemedy("write it in one invocation and run it in another")
	case len(inv.Args) > 0:
		return errs.Usage("PLAN_TAKES_NO_KEYS",
			"--%s takes its issue from the plan, and %d were also given", seqApplyFlag,
			len(inv.Args)).
			WithRemedy("drop the arguments")
	case inv.Flags.String(stepsFlag) != "" || inv.Flags.String(stepsFileFlag) != "":
		return errs.Usage("PLAN_CARRIES_THE_CHANGE",
			"--%s runs the steps recorded in the plan, so steps cannot be given as well",
			seqApplyFlag).
			WithRemedy("plan the steps you want, read the plan, then apply that file")
	}
	path := inv.Flags.String(seqApplyFlag)
	f, err := os.Open(path) //nolint:gosec // the caller named the file.
	if err != nil {
		return errs.Usage("PLAN_NOT_READ", "cannot read the plan at %s", path).Wrap(err)
	}
	defer func() { _ = f.Close() }()
	plan, err := ParseSequencePlan(f)
	if err != nil {
		return err
	}
	inv.SetValue(sequencePlanKey, plan)
	return nil
}

// stepOutcome is one step's result, before it becomes a document.
type stepOutcome struct {
	step    PlannedStep
	outcome string
	cause   error
}

// runSequenceApply runs a plan.
func runSequenceApply(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	plan, _ := inv.Value(sequencePlanKey).(*SequencePlan)
	if inv.Jira == nil || plan == nil {
		return nil, errs.Runtime("NO_SESSION", "issue sequence has no connection to Jira")
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	ledger := inv.Jira.Idempotency()
	done, err := completedSteps(ledger, info.BaseURL, plan)
	if err != nil {
		return nil, err
	}

	// A first run compares the baseline, before anything is rebuilt, so an
	// issue that moved since planning is refused as cheaply as it can be. A
	// resume cannot compare it: the plan's own steps moved it.
	baseline := BaselineCompared
	if len(done) == 0 {
		client := &issue.Client{Transport: conn, Site: info}
		if err := issue.CompareUnchanged(ctx, client, plan.Key, plan.Precondition); err != nil {
			return nil, err
		}
	} else {
		baseline = BaselineRechecked
	}

	rebuilt, err := rebuildSteps(ctx, inv, &preflight{conn: conn, info: info, key: plan.Key}, plan, done)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return previewDoc(rebuilt), nil
	}

	outcomes := sendSteps(ctx, inv, conn, ledger, info.BaseURL, plan, done, rebuilt)
	doc := SequenceApplyDoc(plan, baseline, outcomes)
	for _, o := range outcomes {
		if o.outcome == StepFailed {
			return nil, &registry.PartiallyApplied{Doc: doc, Cause: o.cause}
		}
	}
	return doc, nil
}

// completedSteps is the steps of this plan the ledger says are done, read
// without claiming anything, so a resume knows before it compares a baseline
// its own earlier run moved.
func completedSteps(ledger *idem.Ledger, site string, plan *SequencePlan) (map[int]bool, error) {
	entries, err := ledger.Entries()
	if err != nil {
		return nil, err
	}
	done := map[int]bool{}
	for _, s := range plan.Steps {
		if slices.ContainsFunc(entries, func(e idem.Entry) bool {
			return e.Site == site && e.Key == s.IdempotencyKey && e.Status == idem.Done
		}) {
			done[s.Number] = true
		}
	}
	return done, nil
}

// rebuildSteps runs every step still to run through its own command again,
// checks it again, and holds what it would send to what the plan recorded.
// Nothing from the file is sent: these rebuilt requests are, and only once
// they are known to be the ones the reader saw.
func rebuildSteps(
	ctx context.Context, inv *registry.Invocation, pf *preflight,
	plan *SequencePlan, done map[int]bool,
) (map[int]stepPlan, error) {
	var todo []stepPlan
	for _, s := range plan.Steps {
		if !done[s.Number] {
			todo = append(todo, dryRun(ctx, inv, s.SequenceStep))
		}
	}
	if err := pf.check(ctx, todo); err != nil {
		return nil, err
	}
	if err := refuseBlocked(todo); err != nil {
		return nil, err
	}
	rebuilt := make(map[int]stepPlan, len(todo))
	for _, p := range todo {
		recorded := plan.Steps[p.step.Number-1].Recorded
		if err := refuseDrift(p, recorded); err != nil {
			return nil, err
		}
		rebuilt[p.step.Number] = p
	}
	return rebuilt, nil
}

// refuseDrift holds a rebuilt step to the requests its plan recorded. A
// difference means something the baseline does not see has moved since
// planning (a field's id, the link type, the assignee's account), and the
// change that would run is not the one somebody read.
func refuseDrift(p stepPlan, recorded []transport.Request) error {
	same := len(p.reqs) == len(recorded)
	for i := 0; same && i < len(recorded); i++ {
		a, b := p.reqs[i], recorded[i]
		same = a.Method == b.Method && a.Path == b.Path &&
			a.Query.Encode() == b.Query.Encode() && string(a.Body) == string(b.Body)
	}
	if same {
		return nil
	}
	detail := "the step now builds " + describeRequests(p.reqs) +
		", and the plan recorded " + describeRequests(recorded)
	return errs.New(exitcode.Conflict, "PLAN_DRIFTED",
		"step %d would send something other than the plan recorded", p.step.Number).
		WithDetail("%s", detail).
		WithRemedy("plan it again, read the new plan, and apply that")
}

func describeRequests(reqs []transport.Request) string {
	parts := make([]string, 0, len(reqs))
	for _, r := range reqs {
		part := r.Method + " " + r.Path
		if q := r.Query.Encode(); q != "" {
			part += "?" + q
		}
		if len(r.Body) > 0 {
			part += " " + string(r.Body)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

// previewDoc is --dry-run beside --apply: every request the apply would send,
// rebuilt and checked, in step order, each named by its own command.
func previewDoc(rebuilt map[int]stepPlan) *render.Doc {
	numbers := slices.Sorted(func(yield func(int) bool) {
		for n := range rebuilt {
			if !yield(n) {
				return
			}
		}
	})
	var items []*render.Node
	for _, n := range numbers {
		items = append(items, rebuilt[n].requests.Children...)
	}
	return render.Record(registry.KindDryRun, registry.VersionDryRun,
		render.ListEl("requests", "request", items...))
}

// sendSteps sends the steps in order, stopping at the first failure.
func sendSteps(
	ctx context.Context, inv *registry.Invocation, conn *transport.Client,
	ledger *idem.Ledger, site string, plan *SequencePlan,
	done map[int]bool, rebuilt map[int]stepPlan,
) []stepOutcome {
	outcomes := make([]stepOutcome, 0, len(plan.Steps))
	stopped := false
	for _, s := range plan.Steps {
		switch {
		case stopped:
			outcomes = append(outcomes, stepOutcome{step: s, outcome: StepNotAttempted})
		case done[s.Number]:
			outcomes = append(outcomes, stepOutcome{step: s, outcome: StepSkipped})
		default:
			o := sendStep(ctx, inv, conn, ledger, site, s, rebuilt[s.Number].reqs)
			outcomes = append(outcomes, o)
			stopped = o.outcome == StepFailed
		}
	}
	return outcomes
}

// sendStep claims a step's key, sends its rebuilt requests, and records the
// outcome.
//
// A claim is released when the failure proves the step was not applied:
// nothing reached Jira, or Jira answered and refused it with a 4xx. Anything
// ambiguous keeps it, because a 503 can arrive after Jira did the work, and a
// resume would then do it twice. The 4xx half is sharper than the other
// writers here, which keep the claim after any answer: a sequence is fixed
// and re-applied as one file, and a refused step that held its claim for ten
// minutes would turn "fix it and apply again" into IDEMPOTENT_IN_FLIGHT.
func sendStep(
	ctx context.Context, inv *registry.Invocation, conn *transport.Client,
	ledger *idem.Ledger, site string, s PlannedStep, reqs []transport.Request,
) stepOutcome {
	failed := func(err error) stepOutcome {
		return stepOutcome{step: s, outcome: StepFailed, cause: atStep(s.Number, err)}
	}
	claim, err := ledger.Claim(site, s.IdempotencyKey, sequenceOperation)
	if err != nil {
		return failed(err)
	}
	switch {
	case claim.Replayed:
		return stepOutcome{step: s, outcome: StepSkipped}
	case claim.InFlight:
		return failed(errs.New(exitcode.Conflict, "IDEMPOTENT_IN_FLIGHT",
			"another run holds the claim for this step and has not finished").
			WithRemedy("wait for it, or check the issue before retrying"))
	case claim.Reclaimed && inv.Stderr != nil:
		_ = render.WriteWarning(inv.Stderr, "IDEMPOTENT_RECLAIMED",
			"an earlier run held the claim for step "+strconv.Itoa(s.Number)+
				" and never finished; it may already have applied it", inv.Format)
	}
	for i, r := range reqs {
		resp, err := conn.Do(ctx, r)
		if err == nil {
			err = transport.Err(resp)
		}
		if err != nil {
			if i == 0 && (transport.NeverSent(err) || refusedOutright(resp)) {
				_ = ledger.Release(site, s.IdempotencyKey)
			}
			return failed(err)
		}
	}
	if err := ledger.Complete(site, s.IdempotencyKey, s.Command.Name()); err != nil {
		return failed(err)
	}
	return stepOutcome{step: s, outcome: StepApplied}
}

// refusedOutright reports a response in which Jira answered and declined: a
// 4xx. Jira validates, authorises and rate-limits before it changes anything,
// so such an answer is a statement that nothing was applied, where a 5xx or a
// broken connection says nothing either way.
func refusedOutright(resp *transport.Response) bool {
	return resp != nil && resp.Status >= 400 && resp.Status < 500
}

// SequenceApplyDoc renders what an apply did.
func SequenceApplyDoc(plan *SequencePlan, baseline string, outcomes []stepOutcome) *render.Doc {
	counts := map[string]int{}
	items := make([]*render.Node, 0, len(outcomes))
	for _, o := range outcomes {
		counts[o.outcome]++
		n := render.El("step").
			Attr("number", strconv.Itoa(o.step.Number)).
			Attr("command", o.step.Command.Name()).
			Attr("outcome", o.outcome)
		if o.cause != nil {
			n.Attr("code", errs.Coerce(o.cause).Code)
		}
		items = append(items, n)
	}
	return render.Record(KindSequenceApply, VersionSequenceApply, render.El("sequence").
		Attr("key", plan.Key).
		Attr("plan-id", plan.PlanID).
		Attr("baseline", baseline).
		Attr("requested", strconv.Itoa(len(outcomes))).
		Attr("applied", strconv.Itoa(counts[StepApplied])).
		Attr("skipped", strconv.Itoa(counts[StepSkipped])).
		Attr("failed", strconv.Itoa(counts[StepFailed])).
		Attr("not-attempted", strconv.Itoa(counts[StepNotAttempted])).
		Child(render.ListEl("steps", "step", items...)))
}
