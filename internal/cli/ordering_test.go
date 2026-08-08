package cli_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/cli"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"

	// The rule holds for every command the binary ships, not only the
	// built-ins, so the resources are linked in here too.
	_ "github.com/kmoneil/jira-cli/internal/commands"
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

	// These four are a gap rather than a decision, and the card says so.
	// A project key does have a grammar — internal/resource/issue.validProject
	// already implements it for the project half of an issue key — and nothing
	// applies it here, so `project get ../etc` is a round trip that comes back
	// 404. url.PathEscape means it is a wasted request and not an unsafe one.
	"project.get":        "a project key has no local parser; see backlog/project-key-has-no-parser.md",
	"project.components": "a project key has no local parser; see backlog/project-key-has-no-parser.md",
	"project.versions":   "a project key has no local parser; see backlog/project-key-has-no-parser.md",
	"project.statuses":   "a project key has no local parser; see backlog/project-key-has-no-parser.md",
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
func TestAPagingFlagIsBoundedBeforeTheProbe(t *testing.T) {
	bad := map[string]string{
		// Above the server's cap. Zero is deliberately not tested here: it is
		// the sentinel for "unset" and is accepted as the default rather than
		// refused, which is a separate defect —
		// backlog/page-size-zero-is-silently-fifty.md. Adding the case here
		// would fail for a reason this test is not about.
		"page-size":  "101",
		"page-token": "not-a-token-this-tool-issued",
	}

	var checked int
	for _, c := range cli.Registry().All() {
		for _, f := range c.Flags {
			value, paging := bad[f.Name]
			if !paging {
				continue
			}
			checked++

			t.Run(c.Name()+"/"+f.Name, func(t *testing.T) {
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

	// Three commands declare --page-size and one declares --page-token, in
	// every profile: none of them is behind a tag.
	if checked != 4 {
		t.Errorf("checked %d paging flags, want 4; the declarations moved and "+
			"this test is now asserting something else", checked)
	}
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
	return &registry.Invocation{
		Jira:     unreachableSession{},
		Args:     args,
		Flags:    registry.NewFlags(),
		Limit:    registry.Limit{N: registry.DefaultLimit},
		Progress: registry.NoProgress,
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
