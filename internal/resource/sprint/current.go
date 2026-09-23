package sprint

import (
	"context"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

func init() {
	registry.Register(currentCommand())
}

func currentCommand() *registry.Command {
	return &registry.Command{
		Path:     []string{"sprint", "current"},
		ScopedBy: []string{registry.GlobalBoard},
		Summary:  "Resolve the board's one active sprint",
		Description: strings.TrimSpace(`
Answers with the board's single active sprint, exactly as ` +
			"`" + buildinfo.App + " sprint get`" + ` reports one.

"The current sprint" is a question about a board, so this reads the board the
way ` + "`" + buildinfo.App + " sprint list`" + ` does: --board, JIRA_BOARD, or
the context. A board with no active sprint is NO_ACTIVE_SPRINT at exit 5, and
a board running more than one is AMBIGUOUS_SPRINT at exit 2 naming every
candidate, because handing a script one of several sprints without saying so
would be a guess dressed as an answer.

The id this reports goes stale the moment the sprint closes. Re-derive it near
the write that uses it rather than remembering it across a session; a reused
stale id is refused by ` + "`" + buildinfo.App + " sprint add`" + ` as
SPRINT_CLOSED either way.`),
		Example: strings.Join([]string{
			buildinfo.App + " sprint current",
			buildinfo.App + " --board 3 sprint current --format json",
		}, "\n"),
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindGet, Version: VersionGet}},
		ExitCodes: []exitcode.Code{
			exitcode.Auth, exitcode.NotFound, exitcode.Permission,
			exitcode.RateLimit, exitcode.Remote,
		},
		// The board is checked before the body for the reason validateList
		// checks it: the refusal should name the three sources before any
		// request is spent.
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			_, err := inv.Jira.RequireBoard()
			return err
		},
		Run: runCurrent,
	}
}

// runCurrent asks the board for its active sprints and answers only when the
// answer is singular.
//
// Zero and several are both refusals rather than empty or arbitrary answers.
// This command exists to hand a script an id it can act on without checking,
// so the two states in which no such id exists have to be told apart from
// each other and from success: nothing to act on is not the same repair as
// too many to choose from.
func runCurrent(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "sprint current")
	if err != nil {
		return nil, err
	}
	boardID, err := inv.Jira.RequireBoard()
	if err != nil {
		return nil, err
	}

	sprints, err := client.List(ctx, boardID, []string{"active"})
	if err != nil {
		return nil, err
	}

	switch len(sprints) {
	case 1:
		return render.Record(KindGet, VersionGet, sprints[0].Node()), nil
	case 0:
		return nil, errs.NotFound("NO_ACTIVE_SPRINT",
			"board %s has no active sprint", boardID).
			WithRemedy("`%s sprint list --state future` shows what could be "+
				"started, and `%s sprint start` starts it", buildinfo.App, buildinfo.App)
	default:
		lines := make([]string, 0, len(sprints))
		for _, s := range sprints {
			lines = append(lines, s.ID+" "+s.Name)
		}
		return nil, errs.Usage("AMBIGUOUS_SPRINT",
			"board %s is running %d active sprints, so no single one is current",
			boardID, len(sprints)).
			WithDetail("%s", strings.Join(lines, "; ")).
			WithRemedy("address one by id; `%s sprint list --state active` "+
				"reports them", buildinfo.App)
	}
}
