//go:build write

package cli_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// TestATruncationSaysWhatItWasCutFrom holds every paginated command to the
// contract's rule for a cut result: the warning says how many rows the whole
// answer held wherever the command can know, and totalUnknown says why for
// each command that cannot.
//
// "100 rows, truncated" is two different answers. One more page finishes 100
// of 104, and 100 of 5,000 needs a narrower question, so a caller who cannot
// tell them apart reports the first hundred as the answer. That is issue 217,
// where a summary written from a cut list described a window it had not
// covered. The warning has carried `total` since `issue activity` learned to
// hold its whole set, and it was given one there and nowhere else: `issue
// list` dropped the count Data Center's search sends on every page, and twelve
// commands that read their whole set and cut it to --limit dropped the length
// of a slice they were holding.
//
// Each command runs twice against the sweep's fixtures, which hold two rows of
// everything: with --limit all to count the answer, and with --limit 1 to cut
// it.
func TestATruncationSaysWhatItWasCutFrom(t *testing.T) {
	var cut int
	for _, c := range cli.Registry().All() {
		// TestPagingIsInvisible accounts for every paginated command this
		// harness cannot drive, with where it is tested instead.
		if !c.Paginated || commandNotDriven(c) != "" {
			continue
		}
		for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
			key := c.Name() + " " + string(kind)
			whole, _, err := runLimited(t, c, kind, "all")
			if err != nil {
				t.Errorf("%s fails against the sweep fixtures (%s), so this sweep "+
					"cannot say anything about its total", key, errCode(err))
				continue
			}
			if whole < 2 {
				t.Errorf("%s answers the sweep fixtures with %d rows, so --limit 1 "+
					"leaves nothing out; give its fixture a second row", key, whole)
				continue
			}
			_, result, err := runLimited(t, c, kind, "1")
			if err != nil {
				t.Errorf("%s fails at --limit 1 (%s)", key, errCode(err))
				continue
			}
			cut++
			finding, silent := cutFinding(whole, result)
			reason, unknown := totalUnknown[key]
			switch {
			case finding == "" && unknown:
				t.Errorf("%s says what it was cut from now and is still in "+
					"totalUnknown (%s); delete the entry", key, reason)
			case silent && !unknown:
				t.Errorf("%s %s; set StreamResult.Total, or name it in "+
					"totalUnknown with why it cannot know", key, finding)
			case finding != "" && !silent:
				t.Errorf("%s %s", key, finding)
			}
		}
	}
	if cut == 0 {
		t.Fatal("cut no command's answer, so this proves nothing")
	}
	t.Logf("cut %d answers to one row", cut)
}

// TestTheTotalSweepCanFail is the negative control: a command that holds its
// whole set, cuts it to --limit, and says nothing about how many rows there
// were has to fail the sweep's check.
func TestTheTotalSweepCanFail(t *testing.T) {
	dropsTheCount := &registry.Command{
		Path:           []string{"probe", "dropsthecount"},
		Summary:        "Holds two rows and cuts them without saying so",
		Paginated:      true,
		NeedsJira:      true,
		Outputs:        []registry.Output{{Kind: "probe.dropsthecount", Version: 1}},
		CollectionName: "rows",
		Columns:        []render.Column{{Header: "id", Path: "@id"}},
		Stream: func(
			_ context.Context, inv *registry.Invocation, out *render.Stream,
		) (registry.StreamResult, error) {
			rows, complete := registry.Bound(inv.Limit, []string{"a", "b"})
			for _, id := range rows {
				if err := out.Write(render.El("row").Attr("id", id)); err != nil {
					return registry.StreamResult{}, err
				}
			}
			return registry.StreamResult{Complete: complete}, nil
		},
	}
	whole, _, err := runLimited(t, dropsTheCount, site.DataCenter, "all")
	if err != nil {
		t.Fatalf("whole: %v", err)
	}
	_, result, err := runLimited(t, dropsTheCount, site.DataCenter, "1")
	if err != nil {
		t.Fatalf("cut: %v", err)
	}
	if finding, _ := cutFinding(whole, result); finding == "" {
		t.Error("a command that drops the count it holds passed the check, so " +
			"TestATruncationSaysWhatItWasCutFrom cannot fail")
	}
}

// totalUnknown names each command and deployment whose cut answer cannot say
// how many rows it was cut from, and why. It excuses an absent total and
// nothing else: a total that is not the answer's size is wrong everywhere.
var totalUnknown = map[string]string{
	"issue.list cloud": "Cloud's enhanced search sends no count, and its " +
		"approximate-count endpoint is a request of its own that answers " +
		"approximately",
	"issue.history cloud": "Jira counts saves and a row is one changed " +
		"field, so its count is not of rows, and theirs means reading every page",
	"user.list datacenter": userListUnknown,
	"user.list cloud":      userListUnknown,
}

// userListUnknown is why a cut user search cannot say what it was cut from.
const userListUnknown = "the limit reaches the server as maxResults and the " +
	"search answers with a bare array, so nothing counts the users who matched"

// cutFinding says what is wrong with what a --limit 1 run said about an answer
// of whole rows, or nothing when it said exactly how many there were. silent
// is true when the only thing wrong is that it said nothing, which is the one
// finding totalUnknown can excuse.
func cutFinding(whole int, cut registry.StreamResult) (finding string, silent bool) {
	switch {
	case cut.Complete:
		return fmt.Sprintf("wrote 1 of %d rows at --limit 1 and called it complete",
			whole), false
	case cut.Total == 0:
		return fmt.Sprintf("cut %d rows to 1 and did not say how many there were",
			whole), true
	case cut.Total != whole:
		return fmt.Sprintf("cut %d rows to 1 and said there were %d", whole,
			cut.Total), false
	}
	return "", false
}

// runLimited drives a command under a limit against pagingFake serving whole
// pages, and reports how many rows it wrote and what it said about the rest.
// A buffered command has no way to say a total, so its result never carries
// one.
func runLimited(
	t *testing.T, c *registry.Command, kind site.Kind, limit string,
) (rows int, result registry.StreamResult, err error) {
	t.Helper()
	ctx := t.Context()
	inv := pagedInvocation(t, c, kind, &pagingFake{kind: kind}, limit)
	if err := registry.Gate(c, inv); err != nil {
		return 0, registry.StreamResult{}, err
	}
	if c.Validate != nil {
		if err := c.Validate(ctx, inv); err != nil {
			return 0, registry.StreamResult{}, err
		}
	}
	var out strings.Builder
	if c.Stream == nil {
		doc, err := c.Run(ctx, inv)
		if err != nil {
			return 0, registry.StreamResult{}, err
		}
		complete := doc.Collection == nil || doc.Collection.Complete
		return doc.Count(), registry.StreamResult{Complete: complete}, nil
	}
	stream, err := render.NewStream(&out, render.XML, render.StreamSpec{
		Kind: c.Kind(), Version: c.KindVersion(),
		Name: c.CollectionName, Columns: columnsFor(c, inv),
	})
	if err != nil {
		return 0, registry.StreamResult{}, err
	}
	result, err = c.Stream(ctx, inv, stream)
	if err != nil {
		return 0, registry.StreamResult{}, err
	}
	return stream.Count(), result, stream.Close(result.Complete, result.NextPageToken)
}
