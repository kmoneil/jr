package issue_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// TestHistoryFiltersByChangedField is the review's item 7.
//
// One `issue history` call cost roughly 10,000 tokens of context for one fact —
// did a named person move this issue — because a changelog carries every
// description and Acceptance Criteria edit as a full before-and-after document
// body on a single TSV line. The reviewer's defence was `head -20`, which cost
// them the answer as well as the tokens: they read the issue as having no
// transitions and had to reverse that two turns later.
func TestHistoryFiltersByChangedField(t *testing.T) {
	for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
		t.Run(string(kind), func(t *testing.T) {
			all, _, _ := runHistory(t, kind, registry.Limit{All: true},
				recordedPageSize(kind))
			allRows := rowsOf(all)
			if len(allRows) < 3 {
				t.Fatalf("the recording holds %d rows, too few to filter "+
					"meaningfully:\n%s", len(allRows), all)
			}

			// Pick a field the recording actually holds, so this test cannot
			// pass by filtering everything away.
			field := fieldOf(allRows[0])
			want := 0
			for _, r := range allRows {
				if strings.EqualFold(fieldOf(r), field) {
					want++
				}
			}
			if want == len(allRows) {
				t.Skipf("every row is %q, so filtering proves nothing here", field)
			}

			got, result, _ := runFilteredHistory(t, kind, field)
			rows := rowsOf(got)
			if len(rows) != want {
				t.Errorf("--changed-field %s returned %d rows, want %d:\n%s",
					field, len(rows), want, got)
			}
			for _, r := range rows {
				if !strings.EqualFold(fieldOf(r), field) {
					t.Errorf("a row for %q survived --changed-field %s:\n%s",
						fieldOf(r), field, r)
				}
			}
			// Filtering is not truncation. The rows removed were not wanted,
			// so the answer is whole.
			if !result.Complete {
				t.Error("a filtered history reported itself incomplete; " +
					"completeness is about what the server sent, not about " +
					"what the caller asked to see")
			}
		})
	}
}

// TestHistoryFilterMatchesTheChangelogsOwnNames pins the matching rule, which
// is the part a catalogue lookup would have broken.
//
// Jira 9.12 Data Center sends no field id in a changelog at all, measured and
// written into ChangeSchema. Resolving a friendly name to customfield_10042 and
// comparing against the id would therefore match nothing on that deployment,
// which is why this matches what the changelog carries.
func TestHistoryFilterMatchesTheChangelogsOwnNames(t *testing.T) {
	all, _, _ := runHistory(t, site.DataCenter, registry.Limit{All: true}, 0)
	field := fieldOf(rowsOf(all)[0])

	lower, _, _ := runFilteredHistory(t, site.DataCenter, strings.ToLower(field))
	upper, _, _ := runFilteredHistory(t, site.DataCenter, strings.ToUpper(field))
	if len(rowsOf(lower)) == 0 {
		t.Fatalf("matching is case-sensitive; %q found nothing", strings.ToLower(field))
	}
	if len(rowsOf(lower)) != len(rowsOf(upper)) {
		t.Errorf("case changed the answer: %d rows for %q, %d for %q",
			len(rowsOf(lower)), strings.ToLower(field),
			len(rowsOf(upper)), strings.ToUpper(field))
	}
}

// TestAnUnmatchedChangedFieldSaysWhatTheIssueHolds is the empty-answer rule
// this project applies everywhere else, applied here for free.
//
// A mistyped field and a field nobody touched produce the same zero rows,
// complete, exit 0. The candidate list costs no request: it is the rows already
// fetched.
func TestAnUnmatchedChangedFieldSaysWhatTheIssueHolds(t *testing.T) {
	var stderr strings.Builder
	got, result, _ := runFilteredHistoryTo(t, site.DataCenter,
		"nosuchfield-xyz", &stderr)

	if n := len(rowsOf(got)); n != 0 {
		t.Fatalf("a field the issue never changed returned %d rows", n)
	}
	if !result.Complete {
		t.Error("an empty filtered result was reported incomplete")
	}
	warning := stderr.String()
	if !strings.Contains(warning, "UNKNOWN_CHANGED_FIELD") {
		t.Fatalf("no warning for a --changed-field that matched nothing:\n%s", warning)
	}
	// Naming what is there is the half that saves the next call. A warning that
	// only says "unknown" sends the caller back for the whole history, which is
	// the output they were avoiding.
	all, _, _ := runHistory(t, site.DataCenter, registry.Limit{All: true}, 0)
	if !strings.Contains(warning, fieldOf(rowsOf(all)[0])) {
		t.Errorf("the warning does not name a field the issue does hold:\n%s", warning)
	}
}

// TestAMatchedChangedFieldIsSilent is the other direction. A warning that fired
// whenever the flag was used would be noise on every correct invocation.
func TestAMatchedChangedFieldIsSilent(t *testing.T) {
	all, _, _ := runHistory(t, site.DataCenter, registry.Limit{All: true}, 0)
	field := fieldOf(rowsOf(all)[0])

	var stderr strings.Builder
	if _, _, _ = runFilteredHistoryTo(t, site.DataCenter, field, &stderr); true {
		if strings.Contains(stderr.String(), "UNKNOWN_CHANGED_FIELD") {
			t.Errorf("warned on a --changed-field that matched:\n%s", stderr.String())
		}
	}
}

// TestNoChangedFieldChangesNothing is the guard against the filter being on by
// default, which would be a silent output change on every existing caller.
func TestNoChangedFieldChangesNothing(t *testing.T) {
	for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
		before, _, _ := runHistory(t, kind, registry.Limit{All: true},
			recordedPageSize(kind))
		if len(rowsOf(before)) == 0 {
			t.Fatalf("%s recording holds no rows", kind)
		}
	}
}

// recordedPageSize is the page size each cassette was recorded at.
//
// The Cloud changelog fixture holds five requests at maxResults=2, so a run
// that asks for anything else is a fixture miss on the first request rather
// than a test of this filter. Data Center serves the whole changelog in one
// response and pages nothing, so it has no page size to match.
func recordedPageSize(kind site.Kind) int {
	if kind == site.Cloud {
		return 2
	}
	return 0
}

func rowsOf(out string) []string {
	trimmed := strings.TrimRight(out, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return nil
	}
	return lines[1:] // the header is not a row
}

// fieldOf reads the field column, which HistoryColumns puts third.
func fieldOf(row string) string {
	cols := strings.Split(row, "\t")
	if len(cols) < 3 {
		return ""
	}
	return cols[2]
}

func runFilteredHistory(
	t *testing.T, kind site.Kind, field string,
) (string, registry.StreamResult, *strings.Builder) {
	t.Helper()
	var stderr strings.Builder
	out, result, _ := runFilteredHistoryTo(t, kind, field, &stderr)
	return out, result, &stderr
}

// runFilteredHistoryTo is runHistory with a --changed-field and a readable
// stderr, which the shared helper discards.
func runFilteredHistoryTo(
	t *testing.T, kind site.Kind, field string, stderr *strings.Builder,
) (string, registry.StreamResult, *strings.Builder) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.history")
	if !ok {
		t.Fatal("issue history is not registered")
	}
	fixture, key := "history-recorded.cloud.json", "AGL-2"
	if kind != site.Cloud {
		fixture, key = "history-recorded.datacenter.json", "ENG-2"
	}
	conn, _ := replayConn(t, fixture)

	flags := registry.NewFlags()
	flags.SetString("changed-field", field)
	if n := recordedPageSize(kind); n > 0 {
		flags.SetInt("page-size", n)
	}
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
		},
		Args: []string{key}, Flags: flags, Limit: registry.Limit{All: true},
		Stderr: stderr, Format: render.XML, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), inv, stream)
	if err != nil {
		t.Fatalf("stream history: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	_ = io.Discard
	return buf.String(), result, stderr
}
