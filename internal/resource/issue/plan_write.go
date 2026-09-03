//go:build write

// Plan and apply: a bulk edit split into a document somebody can read and a
// run that executes it.
//
// Nothing in this tool changed an arbitrary set of issues in one invocation
// before this. `epic add` and `sprint add` take up to fifty keys, but those are
// one request to an API that accepts a list; an edit is one request per row, and
// the only way to make forty of them was a shell loop. A loop discards every
// guarantee the rest of this project spent its effort establishing: when request
// twenty-three fails, the loop's exit status says something failed and nothing
// says which twenty-two succeeded, because each invocation was a separate
// process with a separate result document and the shell kept none of them.
//
// # Why the plan carries intent and never bytes
//
// `--apply` needs to read a document, and this is the first reader in the tree:
// nothing here parses XML, JSON or TSV, in any package. `jr` has only ever
// written its contract. What that reader accepts therefore decides how much
// damage a bad file can do, and the choice was between two shapes.
//
// A plan of *requests* would be the strongest guarantee that what ran is what
// was reviewed, because apply would send the recorded bytes. It also makes any
// file executable input carrying the caller's credential: a `path` from
// somewhere else reaches somebody else's server with the caller's token
// attached. registry.requestNode's comment that a path is "always relative,
// [because] an absolute path would let a server-supplied value redirect the
// request, and the credential, to another host" is that hazard already
// identified once, in the direction where the server was the untrusted party.
//
// A plan of *intent* cannot do that. Apply reads a key, a baseline, an
// idempotency key and a change set, then rebuilds every request through
// EditRequest, the same builder the interactive command uses. The worst a
// hostile file achieves is a `jr`-shaped edit to an issue whose key ParseKey
// accepted, and FuzzParseKeyProducesASafePathSegment is what makes that bound
// real rather than assumed.
//
// # Why one change set for every row
//
// A plan applies one change to many issues, which is the shape of every bulk
// job anybody actually runs: label a triage set, move a stale set forward, close
// out a column. Rows carry what differs between them, which is the key, its
// baseline and its idempotency key, and the change is written once. A plan whose
// rows each carried their own change would be a different feature with a
// different input, and it is the one `--from <file>` is for.
package issue

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// The plan kind and the kind an apply reports.
const (
	KindPlan    = "issue.plan"
	VersionPlan = 1
)

// MaxPlanRows is how many issues one plan may carry.
//
// The same fifty `epic add` and `sprint add` refuse past, and deliberately the
// same number, but not for their reason. Theirs is the agile API's own cap on a
// single request, and they refuse rather than split because two requests can
// half-succeed and the result would be neither moved nor not moved. An apply is
// already N requests and already reports each row's outcome, so a half-applied
// plan is a thing this document can describe exactly.
//
// The cap is here because a plan is meant to be read before it is run. Fifty
// rows is about as much as anybody reviews honestly, and a plan nobody read is
// the failure mode this whole surface exists to prevent, not a scale limit to
// raise later.
const MaxPlanRows = 50

func init() {
	render.RegisterSchema(KindPlan, PlanSchema())
}

// PlanSchema is the shape of a plan, and therefore also the shape ParsePlan
// accepts. One declaration for both directions, so a document this tool writes
// is one it can read back by construction.
func PlanSchema() *render.Schema {
	return &render.Schema{
		Element: "plan",
		Attrs: []render.Field{
			// The command this plan is for. Apply refuses a plan built for a
			// different verb rather than reinterpreting its change set, which
			// is the same reasoning the idempotency ledger uses when it stores
			// the operation beside the key.
			{Name: "verb", Type: render.TypeString, Enum: []string{"issue.edit"}},
			{Name: "count", Type: render.TypeInt},
		},
		Children: []render.Child{
			{Schema: &render.Schema{
				Element: "change",
				Children: []render.Child{
					{Schema: render.Leaf("summary", render.TypeString), Optional: true},
					{Schema: render.Leaf("description", render.TypeString), Optional: true},
					{Schema: render.Leaf("priority", render.TypeString), Optional: true},
					{Schema: render.Leaf("assignee", render.TypeString), Optional: true},
					{Schema: render.Leaf("parent", render.TypeString), Optional: true},
					{Schema: render.ListSchema("labels", "label",
						render.Leaf("label", render.TypeString)), Optional: true},
					{Schema: render.ListSchema("add-labels", "add-label",
						render.Leaf("add-label", render.TypeString)), Optional: true},
					{Schema: render.ListSchema("remove-labels", "remove-label",
						render.Leaf("remove-label", render.TypeString)), Optional: true},
					{Schema: render.ListSchema("fields", "field", &render.Schema{
						Element: "field",
						Attrs:   []render.Field{{Name: "id", Type: render.TypeString}},
						Text:    &render.Field{Type: render.TypeString},
					}), Optional: true},
				},
			}},
			{Schema: render.ListSchema("rows", "row", &render.Schema{
				Element: "row",
				Attrs: []render.Field{
					{Name: "key", Type: render.TypeString},
					// The baseline as of the moment the plan was built. Absent
					// where Jira reported no `updated` for the issue, which is
					// the one case a row can carry no baseline; apply refuses
					// such a row rather than writing it unchecked.
					{Name: "precondition", Type: render.TypeString, Optional: true},
					{Name: "idempotency-key", Type: render.TypeString},
				},
			})},
		},
	}
}

// PlanRow is one issue in a plan.
type PlanRow struct {
	Key            string
	Precondition   string
	IdempotencyKey string
}

// Plan is a bulk edit before any of it has happened.
type Plan struct {
	Verb   string
	Change EditOptions
	Rows   []PlanRow
}

// planVerbEdit is the only verb v1 plans. It is the registry path with a dot,
// which is what the ledger records an operation as.
const planVerbEdit = "issue.edit"

// BuildPlan resolves a set of keys into a plan, spending one request rather
// than one per row.
//
// The baselines come from a search rather than from an `issue get` each,
// because `key IN (...)` returns `updated` for every row in one page and fifty
// is under the default page size. That is the same arithmetic
// `issue list --precondition` exists for, applied to a set the caller named
// instead of a set a query matched.
func BuildPlan(
	ctx context.Context, client *Client, info site.Info, change EditOptions, keys []string,
) (*Plan, error) {
	updated, err := versionsFor(ctx, client, keys)
	if err != nil {
		return nil, err
	}

	rows := make([]PlanRow, 0, len(keys))
	for _, key := range keys {
		raw, found := updated[key]
		if !found {
			// A key the search did not return is one the credential cannot see
			// or one that does not exist. Planning it would put a row in the
			// document that apply is certain to fail, and a plan is supposed to
			// be the thing you read *instead* of finding out.
			return nil, errs.NotFound("UNKNOWN_ISSUE",
				"%s is not an issue this credential can read", key).
				WithRemedy("check the key, or drop it from the set")
		}
		token, err := EncodePrecondition(info, key, raw)
		if err != nil {
			return nil, err
		}
		rows = append(rows, PlanRow{
			Key:            key,
			Precondition:   token,
			IdempotencyKey: idem.DeriveKey(planVerbEdit, key, changeFingerprint(change)),
		})
	}
	return &Plan{Verb: planVerbEdit, Change: change, Rows: rows}, nil
}

// versionsFor fetches the `updated` of every named issue in one request.
func versionsFor(
	ctx context.Context, client *Client, keys []string,
) (map[string]string, error) {
	query := jql.New().In("key", keys...).String()
	result, err := client.List(ctx, ListOptions{
		JQL:      query,
		Limit:    registry.Limit{N: len(keys)},
		PageSize: len(keys),
		Fields:   []string{"updated"},
	})
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(result.Issues))
	for _, i := range result.Issues {
		out[i.Key] = i.updatedRaw
	}
	return out, nil
}

// changeFingerprint reduces a change set to a stable string, so the same edit
// to the same issue derives the same idempotency key on every run and a
// different edit derives a different one.
//
// Sorted and length-prefixed for the reason idem.DeriveKey length-prefixes its
// parts: two different change sets must not reduce to one string, or a re-run
// of a *different* edit would be skipped as already done.
func changeFingerprint(c EditOptions) string {
	parts := []string{
		"summary=" + c.Summary,
		"description=" + c.Description,
		"priority=" + c.Priority,
		"assignee=" + c.Assignee,
		"parent=" + c.Parent,
		fmt.Sprintf("set-assignee=%t,set-parent=%t", c.SetAssignee, c.SetParent),
		"labels=" + strings.Join(c.Labels, "\x1f"),
		"add=" + strings.Join(c.AddLabels, "\x1f"),
		"remove=" + strings.Join(c.RemoveLabels, "\x1f"),
	}
	for _, id := range slices.Sorted(mapKeys(c.Fields)) {
		parts = append(parts, fmt.Sprintf("field:%s=%v", id, c.Fields[id]))
	}

	var b strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&b, "%d:%s;", len(p), p)
	}
	return b.String()
}

func mapKeys(m map[string]any) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// PlanDoc renders a plan.
func PlanDoc(p *Plan) *render.Doc {
	rows := make([]*render.Node, 0, len(p.Rows))
	for _, r := range p.Rows {
		n := render.El("row").Attr("key", r.Key)
		n.AttrIf("precondition", r.Precondition)
		n.Attr("idempotency-key", r.IdempotencyKey)
		rows = append(rows, n)
	}

	plan := render.El("plan").
		Attr("verb", p.Verb).
		Attr("count", fmt.Sprint(len(p.Rows))).
		Child(changeNode(p.Change)).
		Child(render.ListEl("rows", "row", rows...))

	return render.Record(KindPlan, VersionPlan, plan)
}

// changeNode writes the change set once, in the same names the flags carry, so
// a reader who typed the command recognises the document.
func changeNode(c EditOptions) *render.Node {
	n := render.El("change")
	n.LeafIf("summary", c.Summary)
	n.LeafIf("description", c.Description)
	n.LeafIf("priority", c.Priority)
	if c.SetAssignee {
		n.Child(render.El("assignee").SetText(c.Assignee))
	}
	if c.SetParent {
		n.Child(render.El("parent").SetText(c.Parent))
	}
	if c.Labels != nil {
		n.Child(render.ListEl("labels", "label", leaves("label", c.Labels)...))
	}
	if len(c.AddLabels) > 0 {
		n.Child(render.ListEl("add-labels", "add-label",
			leaves("add-label", c.AddLabels)...))
	}
	if len(c.RemoveLabels) > 0 {
		n.Child(render.ListEl("remove-labels", "remove-label",
			leaves("remove-label", c.RemoveLabels)...))
	}
	if len(c.Fields) > 0 {
		fields := make([]*render.Node, 0, len(c.Fields))
		for _, id := range slices.Sorted(mapKeys(c.Fields)) {
			fields = append(fields, render.El("field").
				Attr("id", id).SetText(fmt.Sprint(c.Fields[id])))
		}
		n.Child(render.ListEl("fields", "field", fields...))
	}
	return n
}

func leaves(name string, values []string) []*render.Node {
	out := make([]*render.Node, 0, len(values))
	for _, v := range values {
		out = append(out, render.El(name).SetText(v))
	}
	return out
}

// ---------------------------------------------------------------------------
// Reading a plan back.
// ---------------------------------------------------------------------------

// planDoc mirrors PlanSchema for encoding/xml. It is a separate declaration
// from the schema on purpose: the schema is what this tool writes and what
// `jr contract` publishes, and this is what it accepts. A parser that shared
// its definition with the writer would accept exactly what the writer emits
// today and say nothing about what it emitted last release, which is the whole
// question a versioned document raises.
type planDoc struct {
	XMLName xml.Name `xml:"result"`
	Kind    string   `xml:"kind,attr"`
	Version int      `xml:"v,attr"`
	Plan    struct {
		Verb   string `xml:"verb,attr"`
		Change struct {
			Summary     *string `xml:"summary"`
			Description *string `xml:"description"`
			Priority    *string `xml:"priority"`
			Assignee    *string `xml:"assignee"`
			Parent      *string `xml:"parent"`
			Labels      *struct {
				Label []string `xml:"label"`
			} `xml:"labels"`
			AddLabels *struct {
				Label []string `xml:"add-label"`
			} `xml:"add-labels"`
			RemoveLabels *struct {
				Label []string `xml:"remove-label"`
			} `xml:"remove-labels"`
			Fields *struct {
				Field []struct {
					ID    string `xml:"id,attr"`
					Value string `xml:",chardata"`
				} `xml:"field"`
			} `xml:"fields"`
		} `xml:"change"`
		Rows struct {
			Row []struct {
				Key            string `xml:"key,attr"`
				Precondition   string `xml:"precondition,attr"`
				IdempotencyKey string `xml:"idempotency-key,attr"`
			} `xml:"row"`
		} `xml:"rows"`
	} `xml:"plan"`
}

// ParsePlan reads a plan document.
//
// Every refusal here is a usage error, because the caller named the file. The
// checks are ordered from the cheapest and most likely mistake to the most
// specific, so somebody who pointed at the wrong file is told that rather than
// being told a row is malformed.
func ParsePlan(r io.Reader) (*Plan, error) {
	var doc planDoc
	if err := xml.NewDecoder(r).Decode(&doc); err != nil {
		return nil, planError("this is not a document this tool wrote").
			WithDetail("%v", err).
			WithRemedy("pass the file `" + buildinfo.App +
				" issue edit --plan-out` produced")
	}
	if doc.Kind != KindPlan {
		return nil, planError("this is a %s document, not a plan", displayKind(doc.Kind)).
			WithRemedy("pass the file `" + buildinfo.App +
				" issue edit --plan-out` produced")
	}
	if doc.Version != VersionPlan {
		// A version this build does not know is refused rather than read
		// leniently. The document is a contract and its version is how a
		// consumer is told the shape moved; guessing at an older or newer one
		// is exactly the silent approximation this tool exists not to do.
		return nil, planError("this plan is version %d and this build reads version %d",
			doc.Version, VersionPlan).
			WithRemedy("rebuild the plan with this version of " + buildinfo.App)
	}
	if doc.Plan.Verb != planVerbEdit {
		return nil, planError("this plan is for %s, not for issue edit",
			displayKind(doc.Plan.Verb)).
			WithRemedy("apply it with the command it was planned for")
	}

	rows := make([]PlanRow, 0, len(doc.Plan.Rows.Row))
	seen := make(map[string]bool, len(doc.Plan.Rows.Row))
	for _, r := range doc.Plan.Rows.Row {
		key, ok := ParseKey(r.Key)
		if !ok {
			return nil, planError("%q is not an issue key", r.Key).
				WithDetail("an issue key looks like ENG-123")
		}
		if seen[key.String()] {
			// Two rows for one issue would claim the ledger twice for the same
			// derived key: the second is reported as already done without
			// anything having decided that, and the plan's count stops
			// describing the run.
			return nil, planError("%s appears twice in this plan", key)
		}
		seen[key.String()] = true

		if err := idem.ValidateKey(r.IdempotencyKey); err != nil {
			return nil, err
		}
		rows = append(rows, PlanRow{
			Key:            key.String(),
			Precondition:   r.Precondition,
			IdempotencyKey: r.IdempotencyKey,
		})
	}
	if len(rows) == 0 {
		return nil, planError("this plan has no rows").
			WithRemedy("plan at least one issue")
	}
	if len(rows) > MaxPlanRows {
		return nil, planError("this plan has %d rows and at most %d may be applied",
			len(rows), MaxPlanRows)
	}

	return &Plan{Verb: doc.Plan.Verb, Change: changeFrom(doc), Rows: rows}, nil
}

// changeFrom rebuilds the change set. Nothing here is validated against the
// site, and it does not need to be: every value becomes an argument to
// EditRequest, which builds the same request the interactive command builds,
// and the server refuses a field value it does not accept exactly as it would
// have then.
func changeFrom(doc planDoc) EditOptions {
	c := doc.Plan.Change
	opt := EditOptions{}
	if c.Summary != nil {
		opt.Summary = *c.Summary
	}
	if c.Description != nil {
		opt.Description = *c.Description
	}
	if c.Priority != nil {
		opt.Priority = *c.Priority
	}
	if c.Assignee != nil {
		opt.Assignee, opt.SetAssignee = *c.Assignee, true
	}
	if c.Parent != nil {
		opt.Parent, opt.SetParent = *c.Parent, true
	}
	if c.Labels != nil {
		// Non-nil and empty are different requests: nil leaves the labels
		// alone, empty clears them. The element being present is what says
		// "replace", so the slice has to be non-nil even with no children.
		opt.Labels = append([]string{}, c.Labels.Label...)
	}
	if c.AddLabels != nil {
		opt.AddLabels = c.AddLabels.Label
	}
	if c.RemoveLabels != nil {
		opt.RemoveLabels = c.RemoveLabels.Label
	}
	if c.Fields != nil && len(c.Fields.Field) > 0 {
		opt.Fields = make(map[string]any, len(c.Fields.Field))
		for _, f := range c.Fields.Field {
			opt.Fields[f.ID] = f.Value
		}
	}
	return opt
}

// displayKind keeps an arbitrary string out of an error message unquoted. A
// plan file is caller-supplied and its `kind` could be anything at all.
func displayKind(kind string) string {
	if kind == "" {
		return "an unnamed"
	}
	return fmt.Sprintf("%q", kind)
}

func planError(format string, args ...any) *errs.Error {
	return errs.Usage("INVALID_PLAN", format, args...)
}

// ---------------------------------------------------------------------------
// The command surface.
// ---------------------------------------------------------------------------

const (
	planOutFlagName = "plan-out"
	applyFlagName   = "apply"
)

func planOutFlag() registry.Flag {
	return registry.Flag{
		Name: planOutFlagName, Type: registry.TypeString,
		Usage: "write a plan for these issues to this file and send nothing; " +
			"apply it later with --apply",
	}
}

func applyFlag() registry.Flag {
	return registry.Flag{
		Name: applyFlagName, Type: registry.TypeString,
		Usage: "run a plan written by --plan-out; takes no issue keys and no " +
			"field flags, because the plan carries both",
	}
}

// validateEditShape decides whether the number of keys and the pair of plan
// flags describe a request this command can honour.
//
// It is one function rather than checks spread through validateEdit because the
// three modes are mutually exclusive and the useful error names the mode the
// caller was probably in. "issue edit takes one key" would be true and useless
// to somebody who meant to plan forty.
func validateEditShape(inv *registry.Invocation) error {
	apply := inv.Flags.String(applyFlagName)
	planOut := inv.Flags.String(planOutFlagName)

	if apply != "" && planOut != "" {
		return errs.Usage("CONFLICTING_PLAN_FLAGS",
			"--"+planOutFlagName+" writes a plan and --"+applyFlagName+" runs one").
			WithRemedy("write it in one invocation and run it in another")
	}
	if apply != "" {
		return validateApplyShape(inv)
	}
	if len(inv.Args) == 0 {
		return errs.Usage("NO_ISSUES", "at least one issue key is required").
			WithRemedy("e.g. `%s issue edit ENG-101 --summary ...`", buildinfo.App)
	}
	if planOut != "" {
		return validatePlanOutShape(inv)
	}
	if len(inv.Args) > 1 {
		// The only path to a bulk write is through a plan, deliberately. A
		// direct multi-key edit would be the convenient spelling and would give
		// up the reviewable document that is the whole point.
		return errs.Usage("BULK_NEEDS_A_PLAN",
			"%d issues were given, and editing more than one goes through a plan",
			len(inv.Args)).
			WithRemedy("add --" + planOutFlagName + " <file> to write one, read " +
				"it, then run it with --" + applyFlagName)
	}
	return nil
}

// validateApplyShape refuses the flags an apply cannot honour.
//
// All three refusals are the same shape: the plan already carries this, and
// accepting both raises a question with no good answer. Whether the flag
// overrides the plan or the plan wins, the document somebody reviewed stops
// being the change that runs, which is the one property this surface exists to
// provide.
func validateApplyShape(inv *registry.Invocation) error {
	if len(inv.Args) > 0 {
		return errs.Usage("PLAN_TAKES_NO_KEYS",
			"--"+applyFlagName+" takes its issues from the plan, and %d were "+
				"also given on the command line", len(inv.Args)).
			WithRemedy("drop the keys, or plan them into a new file")
	}
	if editTouchesAnything(inv) {
		return errs.Usage("PLAN_CARRIES_THE_CHANGE",
			"--"+applyFlagName+" runs the change recorded in the plan, so "+
				"the field flags cannot be given as well").
			WithRemedy("plan the change you want, read it, then apply that file")
	}
	if inv.Flags.String(ifUnchangedFlag) != "" {
		return errs.Usage("PLAN_CARRIES_THE_BASELINE",
			"--"+applyFlagName+" checks the baseline recorded on each row, so "+
				"--"+ifUnchangedFlag+" cannot be given as well").
			WithRemedy("the plan already holds one baseline per issue")
	}
	return nil
}

// validatePlanOutShape refuses what a plan cannot describe.
func validatePlanOutShape(inv *registry.Invocation) error {
	if inv.Flags.Bool("dry-run") {
		// Both mean "send nothing", and each names a different document. A
		// command emits one.
		return errs.Usage("CONFLICTING_PLAN_FLAGS",
			"--dry-run and --"+planOutFlagName+" both send nothing and each "+
				"produces a different document").
			WithRemedy("--dry-run to read the requests, --" + planOutFlagName +
				" to write a plan you can apply")
	}
	if len(inv.Args) > MaxPlanRows {
		return errs.Usage("TOO_MANY_ISSUES",
			"a plan carries at most %d issues, and %d were given",
			MaxPlanRows, len(inv.Args)).
			WithRemedy("split the set; a plan longer than this is one nobody " +
				"reads, which is what planning is for")
	}
	return nil
}

// runPlanOut builds a plan, writes it to the named file, and emits it.
//
// The document goes to stdout as well as to the file, because every command in
// this tool emits one and a caller over MCP has no shell to redirect with. The
// file exists because `--apply` reads a file: one derivation, two destinations,
// so the plan you read is the plan you run by construction.
func runPlanOut(
	ctx context.Context, inv *registry.Invocation, client *Client, info site.Info,
	change EditOptions, path string,
) (*render.Doc, error) {
	keys := make([]string, 0, len(inv.Args))
	for _, arg := range inv.Args {
		key, _ := ParseKey(arg) // validateEdit refused anything ParseKey rejects.
		keys = append(keys, key.String())
	}

	plan, err := BuildPlan(ctx, client, info, change, keys)
	if err != nil {
		return nil, err
	}
	doc := PlanDoc(plan)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, errs.Runtime("PLAN_NOT_WRITTEN",
			"cannot write the plan to %s", path).Wrap(err)
	}
	defer func() { _ = f.Close() }()

	// XML, whatever --format says, because this file is written for this tool
	// to read back and one format is one parser. --format still decides what
	// reaches stdout, so a caller who wants JSON gets JSON where they read it.
	if err := render.Write(f, doc, render.XML); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, errs.Runtime("PLAN_NOT_WRITTEN",
			"cannot finish writing the plan to %s", path).Wrap(err)
	}
	return doc, nil
}

// ---------------------------------------------------------------------------
// Applying a plan.
// ---------------------------------------------------------------------------

// The kind an apply reports.
const (
	KindApply    = "issue.apply"
	VersionApply = 1
)

// What one row of a plan came to.
//
// There is no "not-attempted", which `epic add` has and this deliberately does
// not: a run continues past a failure, so every row is attempted and reporting
// otherwise would be false. That difference between the two multi-row writes is
// a decision rather than a drift. `epic add` moves a set into an epic, which is
// one coherent operation whose remainder is not worth attempting once part of
// it failed; a plan is a batch of independent edits that happen to share a
// change, and one stale ticket is no reason to abandon thirty-nine others.
const (
	OutcomeApplied = "applied"
	OutcomeSkipped = "skipped"
	OutcomeFailed  = "failed"
)

func init() {
	render.RegisterSchema(KindApply, ApplySchema())
}

// ApplySchema is what an apply reports: every row, and what became of it.
func ApplySchema() *render.Schema {
	return &render.Schema{
		Element: "apply",
		Attrs: []render.Field{
			{Name: "verb", Type: render.TypeString, Enum: []string{planVerbEdit}},
			{Name: "requested", Type: render.TypeInt},
			{Name: "applied", Type: render.TypeInt},
			{Name: "skipped", Type: render.TypeInt},
			{Name: "failed", Type: render.TypeInt},
		},
		Children: []render.Child{
			{Schema: render.ListSchema("rows", "row", &render.Schema{
				Element: "row",
				Attrs: []render.Field{
					{Name: "key", Type: render.TypeString},
					{
						Name: "outcome", Type: render.TypeString,
						Enum: []string{OutcomeApplied, OutcomeSkipped, OutcomeFailed},
					},
					// The failing row's own error code, so a caller branches on
					// the same string it would have got from a single-issue
					// edit. Absent on a row that did not fail.
					{Name: "code", Type: render.TypeString, Optional: true},
				},
			})},
		},
	}
}

// rowOutcome is one row's result, before it becomes a document.
type rowOutcome struct {
	key     string
	outcome string
	code    string
	cause   error
}

// runApply reads a plan and executes it, one row at a time.
func runApply(
	ctx context.Context, inv *registry.Invocation, client *Client, path string,
) (*render.Doc, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errs.Usage("PLAN_NOT_READ", "cannot read the plan at %s", path).
			Wrap(err).
			WithRemedy("pass the file `" + buildinfo.App +
				" issue edit --plan-out` produced")
	}
	defer func() { _ = f.Close() }()

	plan, err := ParsePlan(f)
	if err != nil {
		return nil, err
	}

	outcomes := make([]rowOutcome, 0, len(plan.Rows))
	for _, row := range plan.Rows {
		outcomes = append(outcomes, applyRow(ctx, inv, client, plan.Change, row))
	}

	doc := ApplyDoc(plan.Verb, outcomes)
	if cause := reportableCause(outcomes); cause != nil {
		// Both halves reach the caller: the document says which rows landed and
		// goes to stdout, the cause decides the exit. Neither is sufficient
		// alone, which is what registry.PartiallyApplied exists for.
		return nil, &registry.PartiallyApplied{Doc: doc, Cause: cause}
	}
	return doc, nil
}

// applyRow does one row and never returns an error, because a failing row is a
// result rather than the end of the run.
func applyRow(
	ctx context.Context, inv *registry.Invocation, client *Client,
	change EditOptions, row PlanRow,
) rowOutcome {
	failed := func(err error) rowOutcome {
		return rowOutcome{
			key: row.Key, outcome: OutcomeFailed,
			code: errs.Coerce(err).Code, cause: err,
		}
	}

	if row.Precondition == "" {
		// The plan could not record a baseline for this issue, which happens
		// when Jira reported no `updated` for it. Writing it anyway would be
		// the unchecked write --if-unchanged exists to refuse, and doing that
		// silently inside a batch is worse than doing it one issue at a time.
		return failed(errs.Usage("NO_BASELINE",
			"%s was planned with no baseline, so it cannot be written safely", row.Key).
			WithRemedy("re-plan it: Jira reported no `updated` for it when the " +
				"plan was built"))
	}

	change.Key = row.Key
	req, err := client.EditRequest(change)
	if err != nil {
		return failed(err)
	}

	ledger := inv.Jira.Idempotency()
	siteURL := client.Site.BaseURL
	out, err := ledger.Claim(siteURL, row.IdempotencyKey, planVerbEdit)
	if err != nil {
		return failed(err)
	}
	switch {
	case out.Replayed:
		// This row already landed, on this plan, in an earlier run. Re-running
		// an apply is therefore a resume and needs no flag to say so: an
		// idempotency key means "do this at most once", and honouring that only
		// when asked would make the default the duplicate.
		return rowOutcome{key: row.Key, outcome: OutcomeSkipped}
	case out.InFlight:
		return failed(errs.New(exitcode.Conflict, "IDEMPOTENT_IN_FLIGHT",
			"another run holds the claim for %s and has not finished", row.Key).
			WithRemedy("wait for it, or check the issue before retrying"))
	}
	if out.Reclaimed {
		warn(inv, "IDEMPOTENT_RECLAIMED",
			"an earlier run held the claim for "+row.Key+" and never finished; "+
				"it may already have applied this row")
	}

	// The same comparison `--if-unchanged` runs, against the baseline the plan
	// recorded rather than one the caller typed. A row that moved since the
	// plan was read is refused with nothing sent, which is why the claim is
	// released: the request never left this process.
	if err := compareUnchanged(ctx, client, row.Key, row.Precondition); err != nil {
		_ = ledger.Release(siteURL, row.IdempotencyKey)
		return failed(err)
	}

	// The caller holds an idempotency key, which is what makes this safe to
	// replay after an upstream error.
	req.Replayable = true
	if err := client.send(ctx, req); err != nil {
		// Released only when the failure proves the request never arrived.
		// Anything ambiguous keeps the claim, because a 503 can land after Jira
		// has done the work and a resume would then apply this row twice.
		if transport.NeverSent(err) {
			_ = ledger.Release(siteURL, row.IdempotencyKey)
		}
		return failed(err)
	}
	if err := ledger.Complete(siteURL, row.IdempotencyKey, row.Key); err != nil {
		return failed(err)
	}
	return rowOutcome{key: row.Key, outcome: OutcomeApplied}
}

// reportableCause picks the failure that decides the exit.
//
// The first that is not a stale write, and the first otherwise. A stale row is
// the expected outcome of two people touching one ticket and says nothing about
// the run; a permission error, a rate limit or a 5xx is systemic and makes every
// row after it suspect. Reporting the expected one over the systemic one would
// be a tidy rule preferred to a true answer.
func reportableCause(outcomes []rowOutcome) error {
	var first error
	for _, o := range outcomes {
		if o.cause == nil {
			continue
		}
		if first == nil {
			first = o.cause
		}
		if o.code != "STALE_WRITE" {
			return o.cause
		}
	}
	return first
}

// ApplyDoc renders what an apply came to.
func ApplyDoc(verb string, outcomes []rowOutcome) *render.Doc {
	rows := make([]*render.Node, 0, len(outcomes))
	var applied, skipped, failed int
	for _, o := range outcomes {
		switch o.outcome {
		case OutcomeApplied:
			applied++
		case OutcomeSkipped:
			skipped++
		case OutcomeFailed:
			failed++
		}
		n := render.El("row").Attr("key", o.key).Attr("outcome", o.outcome)
		n.AttrIf("code", o.code)
		rows = append(rows, n)
	}

	return render.Record(KindApply, VersionApply, render.El("apply").
		Attr("verb", verb).
		Attr("requested", strconv.Itoa(len(outcomes))).
		Attr("applied", strconv.Itoa(applied)).
		Attr("skipped", strconv.Itoa(skipped)).
		Attr("failed", strconv.Itoa(failed)).
		Child(render.ListEl("rows", "row", rows...)))
}
