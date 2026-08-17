package cli_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"

	// The rule holds for every command the binary ships, not only the
	// built-ins, so the resources are linked in here too.
	_ "github.com/kmoneil/jr/internal/commands"
)

// malformedIdentifier is a value no identifier grammar in this tool accepts.
//
// It is not a key, not a number, and not a path segment: `validProject` refuses
// the dots, `digits` refuses all of it, and it is the traversal shape the
// parsers were tightened for. A command that reaches Jira holding this has
// spent a round trip on a value it could have refused for free.
const malformedIdentifier = "../../not-an-id"

// argIsNotAnIdentifier names the commands whose first positional has no local
// grammar, and says why each cannot be swept.
//
// The list is the point of the test, like jqlReportsRatherThanRefuses above. A
// new command taking an identifier fails here and cannot be quieted except by
// writing down why it is exempt.
var argIsNotAnIdentifier = map[string]string{
	"user.list": "a search query is free text; any string is a legitimate " +
		"thing to search for, including one shaped like a path",
	"user.get": "a user id is an opaque accountId on Cloud and a username on " +
		"Data Center, and neither has a grammar this tool can check",
	"sprint.create": "a sprint name is free text and never reaches a URL path; " +
		"Jira accepts a sprint called ../../not-an-id, and refusing one here " +
		"would be this tool deciding what a team may call its iteration. The " +
		"id it hands back is what gets validated, on every verb that takes one",
}

// TestALocalRefusalOutranksTheDeploymentProbe holds every command that takes an
// identifier to the rule the README states: a malformed one is rejected without
// a round trip.
//
// That sentence was true in the state its author was in and false on a cold
// cache. `issue get foo` parsed the key inside the client, past Connect, and
// the deployment probe behind Connect is a request — so an unreachable site
// answered a typo with NETWORK at exit 9, which advertises a refusal as
// retryable. `board get foo` was the same shape, under a ValidateID whose own
// comment promised the round trip was not spent.
//
// The session here fails to connect, exactly as a cold cache against an
// unreachable site does. A command that validates first never notices; a
// command that connects first returns the network error and fails this test.
// Asserting the value appears in the refusal is what keeps a command from
// passing on a different argument's error.
func TestALocalRefusalOutranksTheDeploymentProbe(t *testing.T) {
	var swept int
	for _, c := range cli.Registry().All() {
		if !c.NeedsJira || len(c.Args) == 0 || argIsNotAnIdentifier[c.Name()] != "" {
			continue
		}
		if c.Validate == nil {
			// Nothing else can refuse before the body runs, so there is no
			// invocation to try: the absence is the finding. This is how
			// board.get was shipped — the check existed, in Client.Get.
			t.Errorf("%s takes %q and declares no Validate, so nothing can "+
				"refuse it before the session is built", c.Name(), c.Args[0].Name)
			continue
		}
		swept++

		t.Run(c.Name(), func(t *testing.T) {
			err := c.Validate(t.Context(), invocationWith(c, malformedIdentifier))
			if err == nil {
				t.Fatalf("%s accepted %q as its %s",
					c.Name(), malformedIdentifier, c.Args[0].Name)
			}
			if got := errs.ExitOf(err); got != exitcode.Usage {
				t.Errorf("%s refused %q with exit %v (%s), want %v: a value that "+
					"needs no network must not queue behind the deployment probe",
					c.Name(), malformedIdentifier, got, errs.Coerce(err).Code,
					exitcode.Usage)
			}
			if e := errs.Coerce(err); !strings.Contains(e.Message, malformedIdentifier) &&
				!strings.Contains(e.Detail, malformedIdentifier) {
				t.Errorf("%s refused something, but not %q: %s (%s). An error "+
					"that does not name what it refused may be about another "+
					"argument, which would make this test pass for the wrong reason",
					c.Name(), malformedIdentifier, e.Code, e.Message)
			}
		})
	}

	// A sweep that ran nothing is worse than no sweep, and this one is run
	// under every profile. Ten is what the ci profile holds, which is the
	// smallest tag set and therefore the binding number; the full build sweeps
	// 28. It is a floor against a sweep that quietly stopped selecting
	// anything, not a coverage claim — and adding an exemption drops the count
	// past it, which is the intended friction.
	if swept < 10 {
		t.Errorf("only %d commands were swept; this build cannot show the rule "+
			"holding across the surface", swept)
	}
}

// TestAPagingFlagIsBoundedBeforeTheProbe is the same rule one layer over, for
// the flags whose bounds are arithmetic.
//
// --page-size was resolved inside the request loop and --page-token was decoded
// against the site's deployment, both past Connect, so a value that could never
// work came back as NETWORK at exit 9 on a cold cache. Only the token's
// deployment half genuinely needs a connection, and it stays where it was.
//
// Every command declaring the flag is driven, because the three that declare
// --page-size resolved it in three places and moving one would have left two.
//
// Both ends of --page-size's range are driven, and the low end is the one that
// was wrong. 101 was refused with an argument about not clamping what the
// server cannot honour; 0 was answered by silently doing 50, because it is the
// sentinel for "unset" and nothing at the flag layer could tell an explicit
// zero from an absent flag. This test used to carry that gap as a comment
// saying the case would fail for a reason it was not about, which is the shape
// of an exemption: a value nobody drives is a value nobody checks.
func TestAPagingFlagIsBoundedBeforeTheProbe(t *testing.T) {
	bad := map[string][]string{
		"page-size":  {"101", "0", "-1"},
		"page-token": {"not-a-token-this-tool-issued"},
	}

	var checked int
	for _, c := range cli.Registry().All() {
		for _, f := range c.Flags {
			values, paging := bad[f.Name]
			if !paging {
				continue
			}
			checked++

			for _, value := range values {
				t.Run(c.Name()+"/"+f.Name+"/"+value, func(t *testing.T) {
					if c.Validate == nil {
						t.Fatalf("%s declares --%s and no Validate, so a value it "+
							"can never honour reaches the network first",
							c.Name(), f.Name)
					}
					inv := invocationWith(c, "ENG-1")
					switch f.Type {
					case registry.TypeInt:
						n, err := strconv.Atoi(value)
						if err != nil {
							t.Fatalf("bad test value %q: %v", value, err)
						}
						inv.Flags.SetInt(f.Name, n)
					default:
						inv.Flags.SetString(f.Name, value)
					}

					err := c.Validate(t.Context(), inv)
					if err == nil {
						t.Fatalf("%s accepted --%s %s", c.Name(), f.Name, value)
					}
					if got := errs.ExitOf(err); got != exitcode.Usage {
						t.Errorf("%s refused --%s %s with exit %v (%s), want %v",
							c.Name(), f.Name, value, got, errs.Coerce(err).Code,
							exitcode.Usage)
					}
				})
			}
		}
	}

	// Six commands declare --page-size and one declares --page-token, in
	// every profile: none of them is behind a tag.
	if checked != 7 {
		t.Errorf("checked %d paging flags, want 7; the declarations moved and "+
			"this test is now asserting something else", checked)
	}

	requireEveryIntFlagIsDriven(t, bad)
}

// zeroIsNotAnInput names the int flags whose zero needs no refusal, and why.
//
// Empty today, and that is the finding rather than an oversight: --page-size is
// the only registry.TypeInt flag in the tree. The two int flags on the root —
// --max-requests and --retries — are bound straight onto the app with IntVar
// and never reach registry.Flags, and both document zero as a meaning rather
// than an absence ("0 means no cap").
var zeroIsNotAnInput = map[string]string{}

// requireEveryIntFlagIsDriven closes the general form of this card.
//
// `bad` is a list of flag names somebody remembered to write, and the set of
// int flags is a list the registry knows: two lists with no reason to agree.
// That is the shape constrainingFlags and QueryOptions.Constrained had, where
// the filter missing from the second turned a bounded query into a full-instance
// sweep. An int flag added later gets the same zero-versus-absent collision
// --page-size had, and would sit outside this sweep without anything saying so.
func requireEveryIntFlagIsDriven(t *testing.T, bad map[string][]string) {
	t.Helper()

	for _, c := range cli.Registry().All() {
		for _, f := range c.Flags {
			if f.Type != registry.TypeInt {
				continue
			}
			if _, driven := bad[f.Name]; driven {
				continue
			}
			if why := zeroIsNotAnInput[f.Name]; why != "" {
				continue
			}
			t.Errorf("%s declares --%s as an int and nothing above drives a "+
				"value it must refuse. At this layer a flag's zero and its "+
				"absence are the same value, which is how --page-size 0 came "+
				"to mean 50: either add it to the table, or write down in "+
				"zeroIsNotAnInput why its zero needs no refusal",
				c.Name(), f.Name)
		}
	}
}

// TestAnOmittedPageSizeStillPagesAtTheDefault is the other half of the fix, and
// the half a refusal test cannot cover.
//
// Refusing an explicit zero is only correct if an absent flag is still the
// default: the harvested value is zero either way, so a check that read the
// number alone would have refused every invocation that did not pass the flag.
// This drives the same Validate with nothing set at all.
func TestAnOmittedPageSizeStillPagesAtTheDefault(t *testing.T) {
	var checked int
	for _, c := range cli.Registry().All() {
		if !declaresFlag(c, "page-size") {
			continue
		}
		checked++
		t.Run(c.Name(), func(t *testing.T) {
			inv := invocationWith(c, "ENG-1")
			if err := c.Validate(t.Context(), inv); err != nil {
				t.Errorf("%s refused an invocation that never mentioned "+
					"--page-size: %v", c.Name(), err)
			}
		})
	}
	if checked != 6 {
		t.Errorf("checked %d commands, want the 6 that declare --page-size", checked)
	}
}

func declaresFlag(c *registry.Command, name string) bool {
	for _, f := range c.Flags {
		if f.Name == name {
			return true
		}
	}
	return false
}

// invocationWith builds an invocation carrying a malformed first argument and
// something plausible for every other required one, so a command refuses on the
// argument under test rather than on a missing one.
func invocationWith(c *registry.Command, bad string) *registry.Invocation {
	// A command with no positional at all is legitimate here: the paging test
	// drives issue list, which takes none.
	var args []string
	if len(c.Args) > 0 {
		args = append(args, bad)
		for _, a := range c.Args[1:] {
			if !a.Required {
				break
			}
			args = append(args, "1")
		}
	}
	inv := &registry.Invocation{
		Jira:     unreachableSession{},
		Args:     args,
		Flags:    registry.NewFlags(),
		Limit:    registry.Limit{N: registry.DefaultLimit},
		Progress: registry.NoProgress,
	}
	fillRequiredFlags(c, inv.Flags)
	return inv
}

// fillRequiredFlags supplies a plausible value for every flag a command
// declares as required.
//
// Cobra enforces those before a command body ever runs, so a real invocation
// cannot reach Validate without them. A harness that drives Validate directly
// can, and then every test built on it measures the missing-flag refusal
// instead of the thing it was written to measure: `issue activity --since` is
// required, and without this the paging test, the reachable-exits test and the
// raw-JQL sweep all reported that command refusing for a reason none of them
// was asking about.
func fillRequiredFlags(c *registry.Command, flags registry.Flags) {
	for _, f := range c.Flags {
		if !f.Required {
			continue
		}
		switch f.Type {
		case registry.TypeInt:
			flags.SetInt(f.Name, 1)
		case registry.TypeBool:
			flags.SetBool(f.Name, true)
		default:
			// A relative date, which is the shape of every required string
			// flag in the tree today and is harmless to any that is not: this
			// only has to get past "you did not pass it".
			flags.SetString(f.Name, "-1d")
		}
	}
}

// unreachableSession is a cold cache against a site that does not answer.
//
// Connect fails the way the deployment probe fails, so any command that reaches
// for a connection before parsing what it was handed returns exit 9 and is
// visible. Everything that does not need the network is answered normally,
// because a session that refused every question would fail commands for
// reasons this test is not about.
type unreachableSession struct{}

func (unreachableSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return nil, site.Info{}, errs.Remote("NETWORK", "the site did not answer")
}

func (unreachableSession) Metadata(context.Context) (*site.Metadata, error) {
	return nil, errs.Remote("NETWORK", "the site did not answer")
}

func (unreachableSession) Project() string                 { return "ENG" }
func (unreachableSession) RequireProject() (string, error) { return "ENG", nil }
func (unreachableSession) Board() string                   { return "1" }
func (unreachableSession) RequireBoard() (string, error)   { return "1", nil }
func (unreachableSession) Fields() []string                { return nil }
func (unreachableSession) CheckWritable(string) error      { return nil }
func (unreachableSession) Idempotency() *idem.Ledger       { return nil }
func (unreachableSession) Site() string                    { return "https://unreachable.invalid" }
