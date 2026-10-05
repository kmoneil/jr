package cli_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// pointsField is the custom field sortedJira's catalogue names Story Points,
// and areaField the multi-select it names Area.
const (
	pointsField = "customfield_10042"
	areaField   = "customfield_10050"
)

// sortedRows is the one ordering sortedJira serves, newest update first. The
// second and third rows share an update time to the second, so a walk cut after
// the second leaves a row with the boundary's value on the far side of the cut.
// The fourth has no Story Points.
var sortedRows = []struct{ key, updated, points string }{
	{"ENG-5", "2026-09-17T10:00:00.000+0000", "8"},
	{"ENG-4", "2026-09-16T09:30:00.000+0000", "5"},
	{"ENG-3", "2026-09-16T09:30:00.000+0000", "5"},
	{"ENG-2", "2026-09-15T08:00:00.000+0000", "null"},
	{"ENG-1", "2026-09-14T07:00:00.000+0000", "3"},
}

// sortedJira is a Data Center that answers every search with sortedRows,
// whatever the query, and keeps what each search asked for.
//
// It sends a field only when the search asked for it, the way Jira does, so a
// boundary over a field the walk never fetched has nothing to read.
type sortedJira struct {
	url string

	mu         sync.Mutex
	fields     []string
	catalogues int
}

func newSortedJira(t *testing.T) *sortedJira {
	t.Helper()
	j := &sortedJira{}
	srv := httptest.NewServer(http.HandlerFunc(j.serve))
	t.Cleanup(srv.Close)
	j.url = srv.URL
	return j
}

func (j *sortedJira) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/myself"):
		_, _ = w.Write([]byte(`{"name":"ada","displayName":"Ada Lovelace",` +
			`"timeZone":"Etc/UTC"}`))
	case strings.HasSuffix(r.URL.Path, "/serverInfo"):
		_, _ = w.Write([]byte(`{"version":"9.12.0","deploymentType":"Server",` +
			`"serverTime":"2026-09-18T12:00:00.000+0000"}`))
	case strings.HasSuffix(r.URL.Path, "/field"):
		j.mu.Lock()
		j.catalogues++
		j.mu.Unlock()
		_, _ = w.Write([]byte(`[` +
			`{"id":"updated","name":"Updated","custom":false,"orderable":true,` +
			`"navigable":true,"searchable":true,"clauseNames":["updated","updatedDate"],` +
			`"schema":{"type":"datetime","system":"updated"}},` +
			`{"id":"labels","name":"Labels","custom":false,"orderable":true,` +
			`"navigable":true,"searchable":true,"clauseNames":["labels"],` +
			`"schema":{"type":"array","items":"string","system":"labels"}},` +
			`{"id":"` + pointsField + `","name":"Story Points","custom":true,` +
			`"orderable":true,"navigable":true,"searchable":true,` +
			`"clauseNames":["cf[10042]","Story Points"],"schema":{"type":"number",` +
			`"custom":"com.atlassian.jira.plugin.system.customfieldtypes:float",` +
			`"customId":10042}},` +
			`{"id":"` + areaField + `","name":"Area","custom":true,` +
			`"orderable":true,"navigable":true,"searchable":true,` +
			`"clauseNames":["cf[10050]","Area"],"schema":{"type":"array",` +
			`"items":"option","custom":` +
			`"com.atlassian.jira.plugin.system.customfieldtypes:multiselect",` +
			`"customId":10050}}]`))
	case strings.HasSuffix(r.URL.Path, "/search"):
		j.search(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (j *sortedJira) search(w http.ResponseWriter, r *http.Request) {
	asked := strings.Split(r.URL.Query().Get("fields"), ",")
	j.mu.Lock()
	j.fields = asked
	j.mu.Unlock()

	startAt, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
	want, err := strconv.Atoi(r.URL.Query().Get("maxResults"))
	if err != nil || want <= 0 {
		want = 50
	}
	startAt = min(startAt, len(sortedRows))
	end := min(startAt+want, len(sortedRows))

	rows := make([]string, 0, end-startAt)
	for i, row := range sortedRows[startAt:end] {
		// lastViewed is a field Jira sends when asked and this catalogue cannot
		// name, so a boundary over it can only come from guessing the id.
		var extras strings.Builder
		for _, extra := range []struct{ id, value string }{
			{pointsField, row.points},
			{areaField, `[{"value":"api"},{"value":"ui"}]`},
			{"lastViewed", `"2026-09-18T00:00:00.000+0000"`},
		} {
			if slices.Contains(asked, extra.id) {
				_, _ = fmt.Fprintf(&extras, `,%q:%s`, extra.id, extra.value)
			}
		}
		rows = append(rows, fmt.Sprintf(`{"id":"%d","key":%q,"fields":{`+
			`"summary":"row","status":{"name":"Open",`+
			`"statusCategory":{"key":"new","name":"To Do"}},`+
			`"created":"2026-01-01T00:00:00.000+0000","updated":%q,`+
			`"labels":["a","b"]%s}}`, 100+startAt+i, row.key, row.updated, extras.String()))
	}
	_, _ = fmt.Fprintf(w, `{"startAt":%d,"maxResults":%d,"total":%d,"issues":[%s]}`,
		startAt, want, len(sortedRows), strings.Join(rows, ","))
}

func (j *sortedJira) lastFields() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.fields)
}

func (j *sortedJira) catalogueReads() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.catalogues
}

// listSorted runs `issue list` against j in a fresh session and requires the
// exit a cut list gives.
func listSorted(t *testing.T, j *sortedJira, args ...string) result {
	t.Helper()
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", j.url, "--project", "ENG")
	got := run(t, env, append([]string{"issue", "list"}, args...)...)
	if got.exit != exitcode.Partial {
		t.Fatalf("exit = %v, want 3 (PARTIAL)\nstdout:\n%s\nstderr:\n%s",
			got.exit, got.stdout, got.stderr)
	}
	return got
}

// warningLeaf reads one leaf of the TSV warning, which is what a caller reading
// stderr sees by default.
func warningLeaf(stderr, name string) (string, bool) {
	for line := range strings.SplitSeq(stderr, "\n") {
		if leaf, value, ok := strings.Cut(line, "\t"); ok && leaf == name {
			return value, true
		}
	}
	return "", false
}

// TestACutSortedListSaysWhereItStopped is the boundary issue 217 asked for: a
// list cut short under --sort names the field, the direction, and that field's
// value on the last row written, beside the total it qualifies.
//
// The cut falls inside a tie. The second and third rows share an update time,
// so the row the warning describes is not the last one with its value, and the
// resumed walk begins on that same value again. That is the contract's tie
// rule, and this is the case that shows why it has to be stated: "covered down
// to 09:30" is true of every row ordered before the last one written, and not
// of every row carrying 09:30.
func TestACutSortedListSaysWhereItStopped(t *testing.T) {
	j := newSortedJira(t)
	const reached = "2026-09-16T09:30:00Z"

	got := listSorted(t, j, "--sort", "updated", "--order", "desc", "--limit", "2")
	want := "total\t5\nsort\tupdated\norder\tdesc\nreached\t" + reached + "\n"
	if !strings.Contains(got.stderr, want) {
		t.Fatalf("the warning does not say where the sorted walk stopped, beside "+
			"the total; want\n%s\nin:\n%s", want, got.stderr)
	}
	if n := j.catalogueReads(); n != 0 {
		t.Errorf("a sort on a default field read the field catalogue %d times; "+
			"the common invocation must not pay for the boundary", n)
	}

	token, ok := warningLeaf(got.stderr, "next-page-token")
	if !ok {
		t.Fatalf("no resume token to continue the walk with:\n%s", got.stderr)
	}
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", j.url, "--project", "ENG")
	resumed := run(t, env, "issue", "list", "--sort", "updated", "--order", "desc",
		"--limit", "1", "--page-token", token)
	if !strings.Contains(resumed.stdout, "ENG-3") || !strings.Contains(resumed.stdout, reached) {
		t.Errorf("the resumed walk did not begin on the boundary's own value, so "+
			"this test is not cutting inside a tie:\n%s", resumed.stdout)
	}
}

// TestASortedBoundaryReadsAFieldTheRowsDoNotShow is the case the boundary is
// most needed for: the sort field is not a column, so no format shows it, and
// the walk has to fetch it without putting it in the rows.
//
// `Story Points` is a name, not an id, so it goes through the catalogue, and the
// boundary names the id the rows would key it by if they carried it. With no
// --order, a named field ascends, and the warning says so: `reached` reads the
// opposite way in each direction.
func TestASortedBoundaryReadsAFieldTheRowsDoNotShow(t *testing.T) {
	j := newSortedJira(t)

	got := listSorted(t, j, "--sort", "Story Points", "--limit", "2", "--format", "xml")
	if !slices.Contains(j.lastFields(), pointsField) {
		t.Fatalf("the search did not fetch the sort field; it asked for %v",
			j.lastFields())
	}
	if strings.Contains(got.stdout, pointsField) {
		t.Errorf("fetching the sort field put it in the rows, which nobody asked "+
			"for:\n%s", got.stdout)
	}
	for _, want := range []string{
		"<sort>" + pointsField + "</sort>",
		"<order>asc</order>",
		"<reached>5</reached>",
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the warning does not carry %s:\n%s", want, got.stderr)
		}
	}
}

// TestNoBoundaryIsWrittenWhereNoneIsKnown keeps an absent boundary meaning
// unknown, on the terms an absent total does. Each case is cut short, so each
// warning is written, and none of them may name a boundary it does not have.
func TestNoBoundaryIsWrittenWhereNoneIsKnown(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		// The key ordering nobody named. Its position is the resume token.
		{"no --sort", []string{"--limit", "2"}},
		// A list has no single value, and a joined one reads like a bound.
		{"a list-valued field", []string{"--sort", "labels", "--limit", "2"}},
		{"a multi-select", []string{"--sort", "Area", "--limit", "2"}},
		// The fourth row has no Story Points.
		{"an empty value on the last row", []string{"--sort", "Story Points", "--limit", "4"}},
		// A sort Jira may accept and the catalogue cannot name.
		{"an unresolvable field", []string{"--sort", "lastViewed", "--limit", "2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := listSorted(t, newSortedJira(t), tc.args...)
			for _, leaf := range []string{"sort", "order", "reached"} {
				if value, ok := warningLeaf(got.stderr, leaf); ok {
					t.Errorf("the warning names a %s of %q it cannot know:\n%s",
						leaf, value, got.stderr)
				}
			}
		})
	}
}
