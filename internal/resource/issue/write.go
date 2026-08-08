//go:build write

// The mutating verbs are compiled only into a build that declares the write
// tag. A reader binary does not contain this file, so it cannot change an issue
// even if something asked it to — the guarantee is the linker's, not a runtime
// check that could be bypassed.

package issue

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kinds the mutating verbs emit.
const (
	KindCreate    = "issue.create"
	VersionCreate = 1
	KindEdit      = "issue.edit"
	VersionEdit   = 2
	KindMove      = "issue.move"
	VersionMove   = 2
)

func init() {
	registry.Register(createCommand())
	registry.Register(editCommand())
	registry.Register(moveCommand())
}

// echoMode is bodyMode for the report a write makes about what it just did.
//
// A caller who asked for the document still gets the document. A caller who
// did not gets markdown where it converts and the document where it does not,
// because the write has already happened and refusing to describe it would
// send them to do it again.
//
// It lives here rather than beside bodyMode in comment.go because only the
// write verbs echo, and a file with no tag compiles into every profile: a
// reader binary carried this function and could never reach it.
func echoMode(mode BodyMode) BodyMode {
	if mode == ModeRaw {
		return ModeRaw
	}
	return ModeEcho
}

// writeExits are the statuses every mutating verb can end with. Blocked is what
// read-only mode and a missing confirmation produce; Conflict is a precondition
// that failed, including a reused idempotency key.
func writeExits() []exitcode.Code {
	return []exitcode.Code{
		// Usage covers a malformed key, a bad duration, and a body
		// --body-format cannot read. Every write verb can produce one.
		exitcode.Usage,
		exitcode.Blocked, exitcode.Auth, exitcode.NotFound, exitcode.Permission,
		exitcode.Conflict, exitcode.RateLimit, exitcode.Remote,
	}
}

// dryRunFlag is declared on every mutating verb rather than added implicitly,
// because the contract test reads the declaration — and a flag that exists only
// at runtime is one `jr schema` cannot describe.
func dryRunFlag() registry.Flag {
	return registry.Flag{
		Name: "dry-run", Type: registry.TypeBool,
		Usage: "print the request that would be sent, and send nothing",
	}
}

func createCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "create"},
		Summary: "Create an issue",
		Description: strings.TrimSpace(`
Creates one issue in the resolved project.

--type is resolved against the project's create screen, so a name that does not
exist is refused with the types that do rather than sent for Jira to reject.

--idempotency-key makes a retry safe. The key is recorded before the request is
sent, and a second run with the same key returns the original issue instead of
making another one. Without a key nothing is blocked, but an identical create
that succeeded in the last minute produces a warning on stderr — two deliberate
identical issues are a legitimate thing to want.

--dry-run prints the exact request, body included, and sends nothing.

--description is sent as plain text by default. No markup is interpreted:
**bold** reaches Jira as six characters. On Cloud the text is wrapped in the
document structure the API requires, because containing text is exact where
interpreting it is not.

--body-format markdown converts it instead, refusing by name anything the
subset cannot hold rather than approximating it. --body-format adf takes an
Atlassian Document Format document as JSON and sends it as given. Both are
Cloud only: Data Center stores wiki markup, and there is no converter to it.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue create --type Bug --summary 'Retry drops the last error'",
			buildinfo.App + " issue create --type Task --summary Ship --idempotency-key deploy-42",
			buildinfo.App + " issue create --type Bug --summary Probe --dry-run",
		}, "\n"),
		Flags: []registry.Flag{
			{
				Name: "type", Short: "t", Type: registry.TypeString, Required: true,
				Usage: "issue type, by name or id, e.g. Bug",
			},
			{
				Name: "summary", Type: registry.TypeString, Required: true,
				Usage: "the issue summary",
			},
			{
				Name: "description", Type: registry.TypeString,
				Usage: "the issue description, as plain text",
			},
			{
				Name: "priority", Type: registry.TypeString,
				Usage: "priority name, e.g. High",
			},
			{
				Name: "label", Type: registry.TypeString, Repeatable: true,
				Usage: "label to set; repeat for several",
			},
			{
				Name: "assignee", Short: "a", Type: registry.TypeString,
				Usage: "assignee, by display name, email, or id; " +
					"the word unassigned leaves it unset",
			},
			{
				Name: "parent", Type: registry.TypeString,
				Usage: "parent issue key, for a subtask",
			},
			{
				Name: "idempotency-key", Type: registry.TypeString,
				Usage: "make a retry safe: the same key returns the original issue",
			},
			bodyFormatFlag(),
			dryRunFlag(),
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindCreate, Version: VersionCreate},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateCreate,
		Run:       runCreate,
	}
}

func validateCreate(ctx context.Context, inv *registry.Invocation) error {
	if err := validateAssignee(ctx, inv, inv.Flags.String("assignee")); err != nil {
		return err
	}
	if key := inv.Flags.String("idempotency-key"); key != "" {
		if err := idem.ValidateKey(key); err != nil {
			return err
		}
	}
	if strings.TrimSpace(inv.Flags.String("summary")) == "" {
		return errs.Usage("EMPTY_SUMMARY", "--summary cannot be blank").
			WithRemedy("an issue with no summary is unfindable; give it one")
	}
	if parent := inv.Flags.String("parent"); parent != "" {
		if _, ok := ParseKey(parent); !ok {
			return errs.Usage("INVALID_KEY", "%q is not an issue key", parent).
				WithDetail("an issue key looks like ENG-123").
				WithRemedy("--parent takes the key of the issue this belongs under")
		}
	}
	return nil
}

// CreateOptions is one `issue create` request, before it becomes JSON.
type CreateOptions struct {
	Project     string
	Type        string
	Summary     string
	Description string
	Priority    string
	Labels      []string
	Assignee    string
	Parent      string
}

// CreateRequest builds the request without sending it.
//
// It is separate from Create so --dry-run prints the very bytes that would have
// gone out. A dry run that rendered a description of the request instead would
// be a second implementation of it, and the two would drift.
func (c *Client) CreateRequest(opt CreateOptions) (transport.Request, error) {
	fields := map[string]any{
		"project":   map[string]any{"key": opt.Project},
		"issuetype": map[string]any{"name": opt.Type},
		"summary":   opt.Summary,
	}

	if opt.Description != "" {
		// How the text is read is --body-format's answer, and every format
		// ends in a document on Cloud and a string on Data Center. See
		// bodyValue.
		value, err := bodyValue(c.Site.Kind, opt.Description, c.BodyFormat)
		if err != nil {
			return transport.Request{}, err
		}
		fields["description"] = value
	}
	if opt.Priority != "" {
		fields["priority"] = map[string]any{"name": opt.Priority}
	}
	if len(opt.Labels) > 0 {
		fields["labels"] = opt.Labels
	}
	if opt.Parent != "" {
		fields["parent"] = map[string]any{"key": opt.Parent}
	}
	if opt.Assignee != "" {
		fields["assignee"] = c.assigneeValue(opt.Assignee)
	}

	body, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the create request").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPost,
		Path:   c.Site.APIBase() + "/issue",
		Header: jsonHeader(),
		Body:   body,
	}, nil
}

// assigneeValue names a user the way this deployment expects.
//
// Cloud identifies a user by accountId and Data Center by name. The two are not
// interchangeable, and sending the wrong one is a 400 that says nothing useful
// about which field was wrong.
func (c *Client) assigneeValue(assignee string) map[string]any {
	if strings.EqualFold(assignee, "unassigned") || strings.EqualFold(assignee, "empty") {
		// An explicit null is how Jira is told to clear the field. Omitting it
		// would leave the current assignee in place, which is the opposite of
		// what was asked.
		return nil
	}
	if c.Site.Kind == site.Cloud {
		return map[string]any{"accountId": assignee}
	}
	return map[string]any{"name": assignee}
}

func jsonHeader() map[string][]string {
	return map[string][]string{"Content-Type": {"application/json"}}
}

// Created is what a create returned.
type Created struct {
	ID  string `json:"id"`
	Key string `json:"key"`
	// Replayed reports that this came from the ledger rather than from Jira.
	Replayed bool `json:"-"`
}

// Create sends the request and reports what it made.
func (c *Client) Create(ctx context.Context, req transport.Request) (Created, error) {
	resp, err := c.Transport.Do(ctx, req)
	if err != nil {
		return Created{}, err
	}
	if err := transport.Err(resp); err != nil {
		return Created{}, err
	}

	var made Created
	if err := json.Unmarshal(resp.Body, &made); err != nil || made.Key == "" {
		return Created{}, errs.Remote("MALFORMED_CREATE_RESPONSE",
			"%s did not report the issue it created", req.Path).
			WithRequestID(resp.RequestID).
			WithDetail("the issue may exist; check the project before retrying").
			Wrap(err)
	}
	return made, nil
}

func runCreate(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "issue create has no connection to Jira")
	}
	project, err := inv.Jira.RequireProject()
	if err != nil {
		return nil, err
	}

	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	client := &Client{Transport: conn, Site: info}

	req, err := client.CreateRequest(CreateOptions{
		Project:     project,
		Type:        inv.Flags.String("type"),
		Summary:     inv.Flags.String("summary"),
		Description: inv.Flags.String("description"),
		Priority:    inv.Flags.String("priority"),
		Labels:      inv.Flags.StringSlice("label"),
		Assignee:    resolvedAssignee(inv, inv.Flags.String("assignee")),
		Parent:      inv.Flags.String("parent"),
	})
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.create", req), nil
	}

	made, err := createWithLedger(ctx, inv, client, req)
	if err != nil {
		return nil, err
	}
	return CreateDoc(made), nil
}

// CreateDoc renders what a create produced.
func CreateDoc(made Created) *render.Doc {
	n := render.El("issue").
		Attr("key", made.Key).
		Attr("id", made.ID)
	if made.Replayed {
		// The replay is marked rather than being silently identical, because a
		// caller has to be able to tell "I made this" from "this already
		// existed" — the second is not a failure, but it is not the same event.
		n.Attr("replayed", "true")
	}
	return render.Record(KindCreate, VersionCreate, n)
}

// createWithLedger runs the create under the idempotency ledger.
//
// The order is the whole point: claim, then send, then record. Sending first
// and recording afterwards would leave a crash between them indistinguishable
// from a request that never happened, and the next attempt would make a second
// issue.
func createWithLedger(
	ctx context.Context, inv *registry.Invocation, client *Client, req transport.Request,
) (Created, error) {
	ledger := inv.Jira.Idempotency()
	key := inv.Flags.String("idempotency-key")
	siteURL := client.Site.BaseURL

	if key == "" {
		// No key means no protection, per §6.3 — but an identical create that
		// just succeeded is worth saying out loud. It is a warning and not a
		// refusal: two deliberate identical issues are a legitimate thing to
		// want, and a caller who did not ask for idempotency does not silently
		// get it.
		derived := idem.DeriveKey(KindCreate, siteURL, string(req.Body))
		warnIfRecent(inv, ledger, siteURL, derived)

		made, err := client.Create(ctx, req)
		if err != nil {
			return Created{}, err
		}
		// Recorded, not claimed. Without this the warning above could never
		// fire, because nothing would ever have written the entry it looks for.
		_ = ledger.Note(siteURL, derived, encodeCreated(made))
		return made, nil
	}

	out, err := ledger.Claim(siteURL, key, KindCreate)
	if err != nil {
		return Created{}, err
	}
	switch {
	case out.Replayed:
		made, err := decodeCreated(out.Entry.Result)
		if err != nil {
			return Created{}, err
		}
		made.Replayed = true
		return made, nil
	case out.InFlight:
		return Created{}, errs.New(exitcode.Conflict, "IDEMPOTENT_IN_FLIGHT",
			"another run holds idempotency key %q and has not finished", key).
			WithDetail("it may already have created the issue").
			WithRemedy("wait for it, or check the project before retrying")
	}
	if out.Reclaimed {
		// The claim was handed over from an attempt that never finished. Saying
		// so is the honest move: that attempt may have created an issue, and
		// this one is about to create another.
		warn(inv, "IDEMPOTENT_RECLAIMED",
			"an earlier run held key "+key+" and never finished; it may have "+
				"created an issue already")
	}

	// A key means the caller holds an idempotency token, which is what makes a
	// POST safe to replay after an upstream error.
	req.Replayable = true

	made, err := client.Create(ctx, req)
	if err != nil {
		// A failure that happened before the request left this process proves
		// it was not applied, so the key is freed. Anything else keeps the
		// claim: a 503 can arrive after Jira has already done the work, and a
		// retry then would duplicate. Holding a key for ten minutes because a
		// site URL had a typo buys nothing; holding it after an ambiguous
		// failure is the entire point.
		if transport.NeverSent(err) {
			_ = ledger.Release(siteURL, key)
		}
		return Created{}, err
	}
	if err := ledger.Complete(siteURL, key, encodeCreated(made)); err != nil {
		// The issue exists. Failing the command now would report a failure for
		// something that succeeded, so this is a warning: the only cost is that
		// a retry with this key would create a second one.
		warn(inv, "LEDGER_NOT_RECORDED",
			"created "+made.Key+" but could not record the idempotency key; "+
				"a retry with it would create another issue")
	}
	return made, nil
}

// warnIfRecent implements the unkeyed check from §6.3.
func warnIfRecent(inv *registry.Invocation, ledger *idem.Ledger, siteURL, derived string) {
	entry, found, err := ledger.Recent(siteURL, derived, idem.RecentWindow)
	if err != nil || !found {
		return
	}
	made, err := decodeCreated(entry.Result)
	if err != nil {
		return
	}
	warn(inv, "POSSIBLE_DUPLICATE",
		"an identical create produced "+made.Key+" moments ago")
}

// warn emits a structured diagnostic on stderr. stdout carries the result and
// nothing else, so this can never reach it.
func warn(inv *registry.Invocation, code, message string) {
	if inv.Stderr == nil {
		return
	}
	_ = render.WriteWarning(inv.Stderr, code, message, inv.Format)
}

func editCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "edit"},
		Summary: "Change fields on an issue",
		Description: strings.TrimSpace(`
Sets fields on an existing issue. Only what you name is sent, so a field you do
not mention is left alone.

--label replaces the whole set. --add-label and --remove-label modify it
instead, and combining the two forms is refused rather than resolved to
whichever the implementation happened to apply last.

--assignee unassigned clears the assignee, by sending an explicit null. Omitting
the flag leaves it as it was, which is a different thing.

--if-unchanged refuses the write if somebody else edited the issue since you
read it. Pass the precondition attribute from issue get; a stale one exits 7
and sends nothing. Without it the last write wins and the earlier one is lost
silently, which is the ordinary outcome of two callers editing one issue.

Jira offers no conditional request on an issue, so the check is a read, a
comparison, and then the write, and the window between the read and the write
is one round trip wide. The result says method="read-compare" rather than
letting the word precondition imply an atomic one.

--dry-run prints the exact request, body included, and sends nothing.

--description and --body-format work exactly as on issue create.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue edit ENG-101 --summary 'A better summary'",
			buildinfo.App + " issue edit ENG-101 --add-label retry --remove-label wontfix",
			buildinfo.App + " issue edit ENG-101 --assignee unassigned --dry-run",
			buildinfo.App + " issue edit ENG-101 --priority High --if-unchanged eyJkIjo",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Flags: []registry.Flag{
			{Name: "summary", Type: registry.TypeString, Usage: "replace the summary"},
			{
				Name: "description", Type: registry.TypeString,
				Usage: "replace the description, as plain text",
			},
			{Name: "priority", Type: registry.TypeString, Usage: "set the priority by name"},
			{
				Name: "label", Type: registry.TypeString, Repeatable: true,
				Usage: "replace the whole label set; repeat for several",
			},
			{
				Name: "add-label", Type: registry.TypeString, Repeatable: true,
				Usage: "add a label, leaving the others; repeat for several",
			},
			{
				Name: "remove-label", Type: registry.TypeString, Repeatable: true,
				Usage: "remove a label, leaving the others; repeat for several",
			},
			{
				Name: "assignee", Short: "a", Type: registry.TypeString,
				Usage: "set the assignee, by display name, email, or id; " +
					"the word unassigned clears it",
			},
			bodyFormatFlag(),
			ifUnchangedFlagDecl(),
			dryRunFlag(),
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindEdit, Version: VersionEdit},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateEdit,
		Run:       runEdit,
	}
}

func validateEdit(ctx context.Context, inv *registry.Invocation) error {
	if err := validateAssignee(ctx, inv, inv.Flags.String("assignee")); err != nil {
		return err
	}
	if _, ok := ParseKey(inv.Args[0]); !ok {
		return errs.Usage("INVALID_KEY", "%q is not an issue key", inv.Args[0]).
			WithDetail("an issue key looks like ENG-123").
			WithRemedy("pass a key, not an id or a summary")
	}
	if err := validatePrecondition(inv); err != nil {
		return err
	}

	replace := len(inv.Flags.StringSlice("label")) > 0
	modify := len(inv.Flags.StringSlice("add-label")) > 0 ||
		len(inv.Flags.StringSlice("remove-label")) > 0
	if replace && modify {
		// Both at once has no single right answer, and picking one would make
		// the result depend on an implementation detail nobody can see.
		return errs.Usage("CONFLICTING_LABEL_FLAGS",
			"--label replaces the whole set, so it cannot be combined with "+
				"--add-label or --remove-label").
			WithRemedy("use --label alone to replace, or the add/remove pair to modify")
	}

	if !editTouchesAnything(inv) {
		// An edit that changes nothing is a request that cannot do what it
		// says. Sending it would be a round trip to achieve nothing.
		return errs.Usage("NOTHING_TO_EDIT", "issue edit was given nothing to change").
			WithRemedy("name at least one field, e.g. --summary")
	}
	return nil
}

func editTouchesAnything(inv *registry.Invocation) bool {
	for _, name := range []string{"summary", "description", "priority", "assignee"} {
		if inv.Flags.String(name) != "" {
			return true
		}
	}
	for _, name := range []string{"label", "add-label", "remove-label"} {
		if len(inv.Flags.StringSlice(name)) > 0 {
			return true
		}
	}
	return false
}

// EditOptions is one `issue edit` request. A nil slice and an empty one mean
// different things here: nil leaves the labels alone, empty clears them.
type EditOptions struct {
	Key          string
	Summary      string
	Description  string
	Priority     string
	Labels       []string
	AddLabels    []string
	RemoveLabels []string
	Assignee     string
	// SetAssignee distinguishes "leave it alone" from "clear it", which an
	// empty string cannot.
	SetAssignee bool
}

// EditRequest builds the request without sending it.
func (c *Client) EditRequest(opt EditOptions) (transport.Request, error) {
	parsed, ok := ParseKey(opt.Key)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY",
			"%q is not an issue key", opt.Key)
	}

	fields := map[string]any{}
	update := map[string]any{}

	if opt.Summary != "" {
		fields["summary"] = opt.Summary
	}
	if opt.Description != "" {
		value, err := bodyValue(c.Site.Kind, opt.Description, c.BodyFormat)
		if err != nil {
			return transport.Request{}, err
		}
		fields["description"] = value
	}
	if opt.Priority != "" {
		fields["priority"] = map[string]any{"name": opt.Priority}
	}
	if opt.Labels != nil {
		fields["labels"] = opt.Labels
	}
	if opt.SetAssignee {
		fields["assignee"] = c.assigneeValue(opt.Assignee)
	}

	// add and remove go through `update` rather than `fields`, because that is
	// the only shape Jira applies incrementally — setting `fields.labels` would
	// replace the set, which is what --label means and not what these do.
	var ops []any
	for _, l := range opt.AddLabels {
		ops = append(ops, map[string]any{"add": l})
	}
	for _, l := range opt.RemoveLabels {
		ops = append(ops, map[string]any{"remove": l})
	}
	if len(ops) > 0 {
		update["labels"] = ops
	}

	payload := map[string]any{}
	if len(fields) > 0 {
		payload["fields"] = fields
	}
	if len(update) > 0 {
		payload["update"] = update
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the edit request").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPut,
		Path:   c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()),
		Header: jsonHeader(),
		Body:   body,
	}, nil
}

func runEdit(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "issue edit has no connection to Jira")
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	client := &Client{Transport: conn, Site: info}

	opt := EditOptions{
		Key:          inv.Args[0],
		Summary:      inv.Flags.String("summary"),
		Description:  inv.Flags.String("description"),
		Priority:     inv.Flags.String("priority"),
		AddLabels:    inv.Flags.StringSlice("add-label"),
		RemoveLabels: inv.Flags.StringSlice("remove-label"),
	}
	if labels := inv.Flags.StringSlice("label"); labels != nil {
		opt.Labels = labels
	}
	if assignee := inv.Flags.String("assignee"); assignee != "" {
		opt.Assignee, opt.SetAssignee = resolvedAssignee(inv, assignee), true
	}

	req, err := client.EditRequest(opt)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.edit", req), nil
	}
	// Before the write and after the request is built, so a request this tool
	// would have refused to build never costs the extra read.
	checked, err := checkUnchanged(ctx, inv, client, opt.Key)
	if err != nil {
		return nil, err
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}
	return stampPrecondition(
		EditedDoc(KindEdit, VersionEdit, opt.Key, "edited"), checked,
	), nil
}

// send performs a request whose success carries no body worth parsing. Jira
// answers an edit and a transition with 204.
func (c *Client) send(ctx context.Context, req transport.Request) error {
	resp, err := c.Transport.Do(ctx, req)
	if err != nil {
		return err
	}
	return transport.Err(resp)
}

// EditedDoc renders the acknowledgement of a change.
//
// Jira returns no body, so this reports what was asked for rather than
// re-reading the issue. Fetching it again would be a second request whose
// answer could differ for reasons unrelated to this command.
func EditedDoc(kind string, version int, key, action string) *render.Doc {
	return render.Record(kind, version, render.El("issue").
		Attr("key", key).
		Attr("action", action))
}

func moveCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "move"},
		Summary: "Transition an issue to another status",
		Description: strings.TrimSpace(`
Moves an issue through its workflow.

The transition is named the way a person names it — "Close Issue" — and resolved
against what the issue can actually do right now. A name that is not available
is refused with the whole available set, because a missing transition is far
more often blocked from the current status than misspelled. A name matching two
transitions is refused with both, since they lead to different statuses.

That list is fetched fresh every time and never cached: it depends on where the
issue is now, and acting on a stale copy sends an id the workflow no longer
offers.

--if-unchanged refuses the transition if the issue changed since you read it,
exactly as on issue edit. Resolving the transition already guards against a
status that moved underneath you; this guards against everything else, which
matters most when the transition sets a resolution or adds a comment.

--dry-run prints the exact request, body included, and sends nothing.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue move ENG-101 'Start Progress'",
			buildinfo.App + " issue move ENG-101 'Close Issue' --resolution Fixed",
			buildinfo.App + " issue move ENG-101 11 --dry-run",
			buildinfo.App + " issue move ENG-101 Done --if-unchanged eyJkIjo",
		}, "\n"),
		Args: []registry.Arg{
			{Name: "key", Usage: "issue key, e.g. ENG-101", Required: true},
			{
				Name: "transition", Required: true,
				Usage: "transition name or id; see `jr meta transitions <key>`",
			},
		},
		Flags: []registry.Flag{
			{
				Name: "resolution", Type: registry.TypeString,
				Usage: "resolution to set, for a transition that asks for one",
			},
			{
				Name: "comment", Type: registry.TypeString,
				Usage: "comment to add with the transition",
			},
			ifUnchangedFlagDecl(),
			dryRunFlag(),
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindMove, Version: VersionMove},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateMove,
		Run:       runMove,
	}
}

func validateMove(_ context.Context, inv *registry.Invocation) error {
	if _, ok := ParseKey(inv.Args[0]); !ok {
		return errs.Usage("INVALID_KEY", "%q is not an issue key", inv.Args[0]).
			WithDetail("an issue key looks like ENG-123").
			WithRemedy("pass a key, not an id or a summary")
	}
	if err := validatePrecondition(inv); err != nil {
		return err
	}
	// The transition itself is resolved in the command body rather than here,
	// because resolving it needs the issue's current status — and fetching that
	// during validation would make a dry run cost the same round trip twice.
	return nil
}

// MoveRequest builds the transition request without sending it.
func (c *Client) MoveRequest(key, transitionID, resolution, comment string) (transport.Request, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}

	payload := map[string]any{
		"transition": map[string]any{"id": transitionID},
	}
	if resolution != "" {
		payload["fields"] = map[string]any{
			"resolution": map[string]any{"name": resolution},
		}
	}
	if comment != "" {
		payload["update"] = map[string]any{
			"comment": []any{map[string]any{"add": map[string]any{"body": comment}}},
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the transition request").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPost,
		Path: c.Site.APIBase() + "/issue/" +
			url.PathEscape(parsed.String()) + "/transitions",
		Header: jsonHeader(),
		Body:   body,
	}, nil
}

func runMove(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "issue move has no connection to Jira")
	}

	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	transitions, err := meta.Transitions(ctx, inv.Args[0])
	if err != nil {
		return nil, err
	}
	// Resolved before anything is sent, so a name that is not available costs
	// one read and no write.
	transition, err := transitions.Resolve(inv.Args[1])
	if err != nil {
		return nil, err
	}

	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	client := &Client{Transport: conn, Site: info}

	req, err := client.MoveRequest(inv.Args[0], transition.ID,
		inv.Flags.String("resolution"), inv.Flags.String("comment"))
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.move", req), nil
	}
	checked, err := checkUnchanged(ctx, inv, client, inv.Args[0])
	if err != nil {
		return nil, err
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	return stampPrecondition(render.Record(KindMove, VersionMove, render.El("issue").
		Attr("key", inv.Args[0]).
		Attr("action", "moved").
		Attr("transition", transition.ID).
		Child(render.El("to").
			Attr("id", transition.To.ID).
			Attr("category", transition.To.Category).
			SetText(transition.To.Name))), checked), nil
}

// encodeCreated and decodeCreated carry a create's result through the ledger.
//
// Both the key and the id go in, so a replay reproduces the original record
// exactly rather than a version of it missing a field. "The replay is marked in
// the output, not silently identical" cuts both ways: it must be identical
// apart from the marker, or a consumer diffing two runs sees a difference that
// means nothing.
func encodeCreated(made Created) string {
	// Errors are impossible for two strings, and a ledger entry is not worth a
	// second error path on the success side of a create that already happened.
	data, _ := json.Marshal(Created{ID: made.ID, Key: made.Key})
	return string(data)
}

func decodeCreated(stored string) (Created, error) {
	var made Created
	if err := json.Unmarshal([]byte(stored), &made); err == nil && made.Key != "" {
		return made, nil
	}
	// An entry written before this shape existed, or one edited by hand, holds
	// a bare key. Reading it as one is better than failing a replay over a
	// format detail — the key is the part a caller acts on.
	if stored == "" {
		return Created{}, errs.Runtime("LEDGER_ENTRY_EMPTY",
			"the ledger recorded no result for this key").
			WithRemedy("check the project; the issue may exist")
	}
	return Created{Key: stored}, nil
}
