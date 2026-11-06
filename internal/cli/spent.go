package cli

import (
	"fmt"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// RequestsSpentCode is the warning that says what a run with --max-requests
// spent of it.
const RequestsSpentCode = "REQUESTS_SPENT"

// reportSpent says, on stderr, how many requests a run with a budget spent.
//
// --max-requests is a number a caller had to pick, and --explain-cost is how
// they pick it, so a run under one says what it spent and the estimate can be
// checked against what happened. A run without a budget says nothing: nobody
// asked, and a line on every invocation is a cost every agent pays to read.
//
// It counts every send, retries included, because the budget does: a retry is
// another request from the server's side, and a count that left it out would
// disagree with the limit it is reported beside.
func (a *app) reportSpent(rc *registry.Command, inv *registry.Invocation) error {
	if a.maxRequests <= 0 || a.stderr == nil {
		return nil
	}
	spent, connected := sessionSpent(inv)
	if !connected {
		return nil
	}
	format, err := a.resultFormat(rc)
	if err != nil {
		return err
	}
	return render.WriteWarning(a.stderr, RequestsSpentCode,
		fmt.Sprintf("%d of %d requests spent, retries included", spent, a.maxRequests),
		format)
}

// resultFormat is the format the command's result went out in, which its
// warnings follow.
func (a *app) resultFormat(rc *registry.Command) (render.Format, error) {
	if rc.Streams() {
		return a.resolveFormat(collectionShaped)
	}
	return a.resolveFormat(&render.Doc{})
}

// spentBy is how many requests the invocation has made so far, or zero for
// one that never connected.
func spentBy(inv *registry.Invocation) int {
	spent, _ := sessionSpent(inv)
	return spent
}

// sessionSpent reads the count off the session without connecting it, so a
// command that never reached Jira is not made to now. It reads underneath the
// scope watcher, because asking how much was spent is not a read of the scope.
func sessionSpent(inv *registry.Invocation) (int, bool) {
	if inv == nil {
		return 0, false
	}
	jira := inv.Jira
	if w, ok := jira.(*registry.ScopeWatcher); ok {
		jira = w.Unwatched()
	}
	s, ok := jira.(*session)
	if !ok || s.client == nil {
		return 0, false
	}
	return s.client.Requests(), true
}
