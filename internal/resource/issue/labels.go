package issue

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/kmoneil/jr/internal/registry"
)

// warnUnknownLabels says so when a label filter names a label no issue carries.
//
// `--label retyr` returned a header, no rows, complete="true", and exit 0, and
// so did `--label retry` on a day nothing carried it. Nothing distinguished a
// typo from a fact, which is the shape `--reporter` had before it resolved its
// user: an answer indistinguishable from an honest one.
//
// It warns rather than refusing, and the difference from `--reporter` is what
// the value is. A person has a canonical identity to resolve to, so a display
// name that matches nobody is a mistake by construction. A label is its own
// identity and has no definition step, since writing one creates it, so asking
// for a label nothing carries is a legal question with a correct answer and
// refusing it would refuse something reasonable. What was not reasonable was
// answering it in a way that looks the same as every other answer.
//
// Both directions are checked. An unknown `--not-label` excludes nothing, so
// the mistake widens the result set instead of emptying it, which is the more
// expensive way to be wrong and the harder one to notice.
func warnUnknownLabels(ctx context.Context, inv *registry.Invocation) {
	// Concat rather than append: Flags.StringSlice hands back the flag's own
	// slice, and appending to one with spare capacity writes into the array
	// the query builder reads its labels from.
	want := slices.Concat(
		inv.Flags.StringSlice("label"),
		inv.Flags.StringSlice("not-label"),
	)
	if len(want) == 0 || inv.Jira == nil {
		// No label filter means no request. The common invocation does not pay
		// for a check it did not ask for, which is the rule --field follows.
		return
	}
	if inv.Stderr == nil || inv.Stderr == io.Discard {
		// Nobody can read the answer, so it is not worth a request. An MCP
		// tool call discards stderr deliberately, because a diagnostic there
		// has nowhere to go but the protocol stream and must not reach it, and
		// spending a lookup per label to write into io.Discard is the cost of
		// this check with none of its value.
		return
	}

	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return
	}
	unknown, err := meta.UnknownLabels(ctx, want)
	if err != nil {
		// A diagnostic never breaks the command it decorates. Anything that
		// failed here (the route, the network, the request budget) is about to
		// be reported properly by the query itself if it matters, and
		// turning a failed check into a failed listing would make a filter
		// that has always worked start depending on an endpoint that is not
		// needed to answer it.
		return
	}
	for _, label := range unknown {
		// "on this site" is the whole claim and is narrower than it looks. The
		// query may be scoped to a project, and a label alive in another one
		// is not reported here. So this catches a label nothing anywhere
		// carries, and never promises that one that exists will match.
		warn(inv, "UNKNOWN_LABEL",
			fmt.Sprintf("no issue on this site carries the label %q", label))
	}
}
