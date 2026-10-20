//go:build write

package issue

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// ifUnchangedFlag carries the token `issue get` minted.
const ifUnchangedFlag = "if-unchanged"

// preconditionMethod names how the check was performed.
//
// It is published rather than assumed because the two mechanisms are not the
// same promise. A conditional request the server evaluates is atomic; a read
// followed by a comparison followed by a write has a window one round trip
// wide, and a caller entitled to know they did not get the strong one should
// not have to infer it from the absence of a claim. Jira offers no validator on
// an issue today, so this is the only value; the attribute exists so that the
// day one appears, the difference is expressible rather than silent.
const preconditionMethod = "read-compare"

// ifUnchangedFlagDecl is declared once because edit, move, and assign all ask
// the same question and must answer it identically.
func ifUnchangedFlagDecl() registry.Flag {
	return registry.Flag{
		Name: ifUnchangedFlag, Type: registry.TypeString,
		// No backquotes. cobra reads the first backquoted run in a flag's usage
		// as the name of its argument, so "which `jr issue get` reports"
		// rendered the flag as `--if-unchanged jr issue get`. Args are not
		// affected, which is why every other backquoted usage in the tree is
		// fine and this one was not.
		Usage: "refuse the write if the issue changed since this precondition, " +
			"which " + buildinfo.App + " issue get reports",
	}
}

// preconditionChild is the record of the check, on every verb that can run one.
func preconditionChild() render.Child {
	return render.Child{Schema: &render.Schema{
		Element: "precondition",
		Attrs: []render.Field{{
			Name: "method", Type: render.TypeString,
			Enum: []string{preconditionMethod},
		}},
	}, Optional: true}
}

// ParsePrecondition reads a --if-unchanged value's own shape, and says nothing
// about which site or which issue it belongs to.
//
// Split from the site comparison for the reason ParsePageToken is: everything
// here is arithmetic on a string the caller typed, so a garbled token can be
// refused before a session exists. The deployment probe behind Connect is a
// request, and a typo answered with NETWORK at exit 9 tells a caller their
// mistake is worth retrying.
func ParsePrecondition(encoded string) (Precondition, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Precondition{}, invalidPrecondition().Wrap(err)
	}

	var p Precondition
	if err := json.Unmarshal(data, &p); err != nil {
		return Precondition{}, invalidPrecondition().Wrap(err)
	}

	// Every token this tool mints stamps all three, so one missing any of them
	// is not ours whatever site is on the other end — `{"not":"valid"}` is
	// legal base64 and legal JSON and decodes to a precondition about nothing.
	// Saying it was never issued here beats reporting it as a mismatch against
	// values it does not have.
	if p.Deployment == "" || p.Key == "" || p.Updated == "" {
		return Precondition{}, invalidPrecondition().
			WithDetail("it names no issue, deployment, or version, " +
				"and every precondition this tool mints names all three")
	}
	if _, ok := ParseKey(p.Key); !ok {
		return Precondition{}, invalidPrecondition().
			WithDetail("it names %q, which is not an issue key", p.Key)
	}
	// The version has to be in the one spelling this tool mints, and refusing
	// anything else here is what keeps a bad token from being reported as a
	// stale write. checkUnchanged compares against a canonical stamp, so a
	// token carrying `2026-08-04T11:32:07+0100` — or any other legal spelling
	// of an instant — would never match and every write would come back
	// STALE_WRITE at exit 7, telling the caller the issue changed when what
	// actually happened is that they passed something this tool did not issue.
	// Two causes with different remedies are two errors.
	if p.Precision != "" && p.Precision != PrecisionSecond &&
		p.Precision != PrecisionMillisecond {
		return Precondition{}, invalidPrecondition().
			WithDetail("it claims precision %q, which this tool does not mint",
				p.Precision)
	}
	if !isCanonicalVersion(p.Updated, p.Precision) {
		return Precondition{}, invalidPrecondition().
			WithDetail("it carries %q, which is not a version this tool records",
				p.Updated)
	}
	return p, nil
}

func invalidPrecondition() *errs.Error {
	return errs.Usage("INVALID_PRECONDITION",
		"--"+ifUnchangedFlag+" is not a precondition this tool issued").
		WithRemedy("pass the precondition attribute from `" + buildinfo.App +
			" issue get`, or omit the flag to write unconditionally")
}

// validatePrecondition is the half of the check that needs no server.
//
// In Command.Validate, with the rest of the local refusals, so a bad token
// costs no round trip and cannot arrive after a write has gone out.
func validatePrecondition(inv *registry.Invocation, keys []string) error {
	encoded := inv.Flags.String(ifUnchangedFlag)
	if encoded == "" {
		// An empty value and an absent flag are the same string at this layer
		// and are opposite requests: one asked for a guarantee and has nothing
		// to check it against, the other asked for no guarantee at all. Writing
		// unconditionally for both is the worse half of that in every case,
		// because the caller who typed the flag believes a check ran.
		//
		// It became reachable without typing it when `issue list
		// --precondition` shipped. A row for an issue Jira reports no `updated`
		// for carries no token, so the cell is empty, and a loop reading that
		// column hands the empty cell straight to this flag. That loop is in
		// docs/recipes.md; the refusal is what makes the example honest.
		if inv.Flags.WasSet(ifUnchangedFlag) {
			return errs.Usage("INVALID_PRECONDITION",
				"--"+ifUnchangedFlag+" was given an empty value").
				WithDetail("an empty precondition is not a baseline, and this " +
					"write would otherwise be sent with no check at all").
				WithRemedy("a row with no `precondition` had no version to " +
					"mint one from: read it with `" + buildinfo.App +
					" issue get`, or omit the flag to write unconditionally")
		}
		return nil
	}
	p, err := ParsePrecondition(encoded)
	if err != nil {
		return err
	}

	// Through ParseKey rather than compared as text, so ENG-1 and eng-1 are
	// the one issue they are. A plan writes several, and a token describing
	// any of them is one the plan can compare before it is built.
	got, _ := ParseKey(p.Key)
	var writing []string
	for _, k := range keys {
		want, ok := ParseKey(k)
		if !ok {
			// The verb's own key check reports this better than a
			// precondition error would; leave it to say so.
			return nil
		}
		if got.String() == want.String() {
			return nil
		}
		writing = append(writing, want.String())
	}
	if len(writing) == 0 {
		return nil
	}
	return invalidPrecondition().
		WithDetail("it describes %s, and this command is writing to %s",
			got, strings.Join(writing, ", ")).
		WithRemedy("read the issue you are about to write: `%s issue get %s`",
			buildinfo.App, writing[0])
}

// checkPlanBaseline compares --if-unchanged before a plan is built, against
// the issue the token names, which validatePrecondition has held to one of
// the plan's keys.
//
// It was accepted and never compared until 2026-10-01, so a plan was written
// over a change the caller never saw, and the apply that followed wrote over
// it: a plan's rows take their baselines from its own search, which reads
// each issue as it is now rather than as the caller read it.
func checkPlanBaseline(ctx context.Context, inv *registry.Invocation, c *Client) error {
	encoded := inv.Flags.String(ifUnchangedFlag)
	if encoded == "" {
		return nil
	}
	p, err := ParsePrecondition(encoded)
	if err != nil {
		return err
	}
	return compareUnchanged(ctx, c, p.Key, encoded)
}

// checkUnchanged reads the issue and refuses the write if it has moved.
//
// Returns whether a check ran, so the acknowledgement can record it rather than
// leaving a caller to infer from a bare success that their flag did anything.
//
// The read is one request and only when the flag is passed. It asks for the one
// field it compares, so it is the cheapest response Jira will serve for an
// issue, and it goes through Client.Get so that a key Jira does not have is the
// same NOT_FOUND here as it is everywhere else.
func checkUnchanged(
	ctx context.Context, inv *registry.Invocation, c *Client, key string,
) (bool, error) {
	encoded := inv.Flags.String(ifUnchangedFlag)
	if encoded == "" {
		return false, nil
	}
	if err := compareUnchanged(ctx, c, key, encoded); err != nil {
		return false, err
	}
	return true, nil
}

// CompareUnchanged is compareUnchanged for a caller outside this package: the
// apply of an `issue sequence` plan, which lives in internal/workflow and
// compares its one baseline before its first step. The same comparison, for
// the reason given below.
func CompareUnchanged(ctx context.Context, c *Client, key, encoded string) error {
	return compareUnchanged(ctx, c, key, encoded)
}

// compareUnchanged is the check itself, with the token supplied rather than
// read from a flag.
//
// Split out for `--apply`, whose token comes from a row of a plan and not from
// the command line. Both callers must run the identical comparison: an apply
// that checked a baseline any less sharply than `--if-unchanged` does would
// give the weaker guarantee to the caller who asked for the stronger one, and
// nothing in the output would say which had run.
func compareUnchanged(ctx context.Context, c *Client, key, encoded string) error {
	p, err := ParsePrecondition(encoded)
	if err != nil {
		return err
	}
	if p.Deployment != c.Site.Kind {
		// Two sites' timestamps have nothing to say to each other, and
		// comparing them would refuse the write with "the issue changed",
		// which is a statement about this issue that nobody checked.
		return invalidPrecondition().
			WithDetail("it was issued for a %s site and this is %s",
				deploymentName(p.Deployment), deploymentName(c.Site.Kind))
	}

	current, err := c.Get(ctx, key, []string{"updated"})
	if err != nil {
		return err
	}
	// Read back at the token's own precision, not at this endpoint's.
	//
	// The issue endpoint serves the millisecond and a search-sourced baseline
	// holds only the second, so comparing what arrives here against a token
	// minted from a listing compared `.679` with `.000` and refused every write
	// on Data Center. The token says how sharp it is; the comparison is that
	// sharp and no sharper.
	stamp, err := preconditionStamp(current.updatedRaw, p.Precision)
	if err != nil {
		return err
	}
	if stamp != p.Updated {
		return errs.Conflict("STALE_WRITE",
			"%s changed after the precondition was taken, so this write was not sent", key).
			WithDetail("the precondition describes %s and the issue now reads %s",
				p.Updated, stamp).
			WithRemedy("re-read it with `%s issue get %s`, decide again against "+
				"what it says now, and retry with the precondition from that read",
				buildinfo.App, key)
	}
	return nil
}

// readBackFlagDecl asks a write for the issue's precondition after it.
//
// The name is `issue list --precondition`'s and so is the meaning, "put the
// token in the output", so a caller who knows one knows the other. A bool, so a
// token typed after it by somebody who meant --if-unchanged is left as a
// positional, and validateReadBack names that mistake before the verb's own
// argument checks report a different one.
//
// Opt-in, because it costs a request: edit, move and assign answer 204 with no
// body on both deployments, so the new `updated` has to be read. §2.4 is why a
// write that did not ask does not pay for it.
func readBackFlagDecl() registry.Flag {
	return registry.Flag{
		Name: preconditionFlagName, Type: registry.TypeBool,
		Usage: "read the issue back after the write and report its precondition, " +
			"for the next --" + ifUnchangedFlag + "; one more request",
	}
}

// validateReadBack refuses --precondition where there is no one write to read
// back, and a token passed as an argument beside it.
//
// First in each verb's Validate, ahead of the argument checks: `issue edit
// ENG-1 --precondition <token>` is otherwise two keys and BULK_NEEDS_A_PLAN,
// which tells a caller who mixed up two flags that they asked for a bulk edit.
// Only beside the flag, so no invocation that worked before it existed answers
// differently now.
func validateReadBack(inv *registry.Invocation) error {
	if !inv.Flags.Bool(preconditionFlagName) {
		return nil
	}
	for _, arg := range inv.Args {
		if _, err := ParsePrecondition(arg); err == nil {
			return errs.Usage("PRECONDITION_TAKES_NO_VALUE",
				"--%s takes no value, and an argument here is a precondition",
				preconditionFlagName).
				WithDetail("--%s reports the issue's precondition after the "+
					"write; --%s is the flag that checks one",
					preconditionFlagName, ifUnchangedFlag).
				WithRemedy("pass the token as --%s <precondition>, and keep "+
					"--%s to be given the next one", ifUnchangedFlag, preconditionFlagName)
		}
	}
	var mode string
	switch {
	case inv.Flags.Bool("dry-run"):
		mode = "--dry-run sends nothing"
	case inv.Flags.String(planOutFlagName) != "":
		mode = "--" + planOutFlagName + " sends nothing"
	case inv.Flags.String(applyFlagName) != "":
		mode = "--" + applyFlagName + " writes a plan's rows, and reports them " +
			"in a document that carries no precondition"
	default:
		return nil
	}
	return errs.Usage("PRECONDITION_NEEDS_A_WRITE",
		"--%s reads back the one issue a write changed, and %s",
		preconditionFlagName, mode).
		WithRemedy("drop --%s; `%s issue get` reads a baseline when one is needed",
			preconditionFlagName, buildinfo.App)
}

// readBackPrecondition puts the issue's precondition on a write's document,
// read after the write, when --precondition asked for it.
//
// It is the version read after the write and not the version the write made.
// Jira says nothing about the second: the write answered 204. A change landing
// between the two, most often an automation rule the write itself fired, is
// inside the token, which is the window `issue get` after the write has too.
// The contract says so rather than letting "after the write" imply more.
//
// The write has landed by the time this runs, so a failed read is a partial
// write and not a failed one: the document reaches stdout without the token,
// and the exit is the read's. The cause says the write is done and is never
// retryable, because a caller retrying a move on exit 9 sends the transition
// again.
func readBackPrecondition(
	ctx context.Context, inv *registry.Invocation, c *Client,
	key, action string, doc *render.Doc,
) (*render.Doc, error) {
	if !inv.Flags.Bool(preconditionFlagName) || doc == nil || doc.Record == nil {
		return doc, nil
	}
	token, err := readBack(ctx, c, key)
	if err != nil {
		e := *errs.Coerce(err)
		e.Message = key + " was " + action + ", and reading its precondition " +
			"back failed: " + e.Message
		e.Remedy = "the write is done, so do not send it again; read the " +
			"precondition with `" + buildinfo.App + " issue get " + key + "`"
		e.Retryable = false
		return nil, &registry.PartiallyApplied{Doc: doc, Cause: &e}
	}
	// Absent rather than empty for an issue Jira reports no `updated` for, as
	// on `issue get`: an empty token handed to --if-unchanged is refused, and
	// a missing attribute says why before the caller gets that far.
	if token != "" {
		doc.Record.Attr(nextPreconditionAttr, token)
	}
	return doc, nil
}

// nextPreconditionAttr is where a write puts the token --precondition read
// back.
//
// Not `precondition`, which is the attribute's name on `issue.get` and
// `issue.list`: a write already has a precondition child, the record of the
// check, and JSON and YAML flatten attributes and children into one object, so
// the renderer refuses a node that uses one name for both. "next" because it is
// the token the next --if-unchanged takes.
const nextPreconditionAttr = "next-precondition"

// readBack mints the token from one read of the issue's `updated`, the read
// compareUnchanged makes, at the millisecond the issue endpoint serves.
func readBack(ctx context.Context, c *Client, key string) (string, error) {
	current, err := c.Get(ctx, key, []string{"updated"})
	if err != nil {
		return "", err
	}
	// The key Jira answered with, so a lower-case argument mints the token
	// `issue get` would have.
	if current.Key != "" {
		key = current.Key
	}
	return EncodePrecondition(c.Site, key, current.updatedRaw, PrecisionMillisecond)
}

// stampPrecondition records that a check ran, on the document a verb returns.
//
// Nothing is added when no check ran: an element saying a precondition was not
// checked would appear on every write in the tree to describe the absence of a
// flag.
func stampPrecondition(doc *render.Doc, checked bool) *render.Doc {
	if checked && doc != nil && doc.Record != nil {
		doc.Record.Child(render.El("precondition").
			Attr("method", preconditionMethod))
	}
	return doc
}
