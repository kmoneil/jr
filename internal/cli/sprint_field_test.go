package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Two sprints as Data Center serves them on an issue: the value of the sprint
// custom field is an array of *strings*, each one the Greenhopper object's Java
// toString. Cloud sends an array of objects and has never had this problem.
//
// The key order is the reporter's, from issue 154, and the two entries differ
// in the way that matters: a closed sprint carries completeDate and an active
// one does not, so anything reading state has both cases in one issue.
const (
	closedSprint = `com.atlassian.greenhopper.service.sprint.Sprint@17d6a1a1[` +
		`activatedDate=2025-01-06T09:00:00.000Z,autoStartStop=false,` +
		`completeDate=2025-01-20T17:00:00.000Z,endDate=2025-01-20T00:00:00.000Z,` +
		`goal=Ship the importer,id=12345,incompleteIssuesDestinationId=<null>,` +
		`name=Team | Sprint 3,rapidViewId=678,sequence=12345,` +
		`startDate=2025-01-06T09:00:00.000Z,state=CLOSED,synced=false]`

	activeSprint = `com.atlassian.greenhopper.service.sprint.Sprint@358455e4[` +
		`activatedDate=2025-01-21T09:00:00.000Z,autoStartStop=false,` +
		`completeDate=<null>,endDate=2025-02-04T00:00:00.000Z,` +
		`goal=<null>,id=12346,incompleteIssuesDestinationId=<null>,` +
		`name=Team | Sprint 4,rapidViewId=678,sequence=12346,` +
		`startDate=2025-01-21T09:00:00.000Z,state=ACTIVE,synced=false]`
)

// sprintJira serves one Data Center issue carrying the sprint field above, plus
// the catalogue --field has to resolve against.
//
// customfield_10020 is the sprint and customfield_10030 is a plain text field
// holding a value that merely mentions the class name. The second one is the
// guard: a parser anchored loosely enough to catch it would be rewriting a
// value somebody typed.
func sprintJira(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"9.12.0","deploymentType":"Server",` +
				`"serverTime":"2026-09-22T12:00:00.000+0000"}`))
		case strings.HasSuffix(r.URL.Path, "/search"):
			_, _ = w.Write([]byte(`{"startAt":0,"maxResults":50,"total":1,"issues":[` +
				issueJSON + `]}`))
		case strings.HasSuffix(r.URL.Path, "/field"):
			_, _ = w.Write([]byte(`[
				{"id":"customfield_10020","name":"Sprint","custom":true,
				 "schema":{"type":"array","items":"string",
				 "custom":"com.pyxis.greenhopper.jira:gh-sprint"}},
				{"id":"customfield_10030","name":"Notes","custom":true,
				 "schema":{"type":"string",
				 "custom":"com.atlassian.jira.plugin.system.customfieldtypes:textarea"}}
			]`))
		default:
			_, _ = w.Write([]byte(issueJSON))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// issueJSON is the one issue this file's stub serves, from both the search
// endpoint and the single-issue one, so a TSV column and an XML element are
// read off the same bytes.
const issueJSON = `{"id":"1","key":"ENG-1","fields":{` +
	`"summary":"an issue that has been through two sprints",` +
	`"status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},` +
	`"created":"2026-01-01T00:00:00.000+0000",` +
	`"updated":"2026-09-22T00:00:00.000+0000",` +
	`"customfield_10020":["` + closedSprint + `","` + activeSprint + `"],` +
	`"customfield_10030":"see com.atlassian.greenhopper.service.sprint.Sprint for the format"` +
	`}}`

// TestASprintFieldIsNotAJavaToString is the defect this file exists for.
//
// scalarizeValue reduces an array by recursing per element, and a Data Center
// sprint array holds strings, so every element lands in `case string` and comes
// back verbatim. The map branch under it already picks `name` correctly and is
// never reached, because the value never was a map. An issue through a dozen
// sprints spends roughly 3,000 tokens saying which sprint it is in.
func TestASprintFieldIsNotAJavaToString(t *testing.T) {
	url := sprintJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "get", "ENG-1", "--field", "Sprint")

	if strings.Contains(got.stdout, "com.atlassian.greenhopper") {
		t.Errorf("the Java toString reached the caller:\n%s", got.stdout)
	}
	for _, want := range []string{"Team | Sprint 3", "Team | Sprint 4"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("sprint name %q is not in the output:\n%s", want, got.stdout)
		}
	}
	// Which sprint is the live one is the question the field is asked, and it
	// is the one the dump buries.
	if !strings.Contains(got.stdout, "active") {
		t.Errorf("nothing says which sprint is active:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "12346") {
		t.Errorf("the sprint id is not addressable:\n%s", got.stdout)
	}
}

// TestAValueThatMerelyMentionsSprintIsUntouched is the other half, and it is
// why the parse is anchored on the fully qualified class name rather than on
// the word Sprint. A text field is a text field.
func TestAValueThatMerelyMentionsSprintIsUntouched(t *testing.T) {
	url := sprintJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "get", "ENG-1", "--field", "Notes")

	const want = "see com.atlassian.greenhopper.service.sprint.Sprint for the format"
	if !strings.Contains(got.stdout, want) {
		t.Errorf("a text field was rewritten:\n%s", got.stdout)
	}
}

// TestASprintColumnHoldsTheNames is the TSV half, and TSV is the default format
// for a collection, so it is the one most callers see.
//
// A list container flattens to its children's text, which is why the sprint
// name is the item's text rather than another attribute: the alternative
// rendered a blank cell, which is the same defect the ragged-table and
// --field-does-nothing cards were both about.
func TestASprintColumnHoldsTheNames(t *testing.T) {
	url := sprintJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "list", "--field", "Sprint", "--format", "tsv")

	lines := strings.Split(strings.TrimRight(got.stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a header and one row, got %d lines:\n%s", len(lines), got.stdout)
	}
	header := strings.Split(lines[0], "\t")
	row := strings.Split(lines[1], "\t")
	if len(header) != len(row) {
		t.Fatalf("ragged row: %d headers, %d cells\n%s", len(header), len(row), got.stdout)
	}

	// The separator is the contract's, not a new one: docs/output-contract.md
	// says a column over a list joins with `,`, and escapes a `,` inside a
	// value. A sprint named "Q3, week 2" is exactly why that rule exists.
	cell := row[len(row)-1]
	if cell != `Team | Sprint 3,Team | Sprint 4` {
		t.Errorf("sprint cell = %q, want the two names joined", cell)
	}
	if strings.Contains(got.stdout, "greenhopper") {
		t.Errorf("the Java toString reached a TSV cell:\n%s", got.stdout)
	}
}

// cloudSprintJira is the same field as Cloud sends it: an array of objects,
// not of strings. Cloud never had this defect, and the point of this stub is
// that the fix did not give it one.
func cloudSprintJira(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"1001.0.0","deploymentType":"Cloud",` +
				`"serverTime":"2026-09-22T12:00:00.000+0000"}`))
		case strings.HasSuffix(r.URL.Path, "/field"):
			_, _ = w.Write([]byte(`[
				{"id":"customfield_10020","name":"Sprint","custom":true,
				 "schema":{"type":"array","items":"json",
				 "custom":"com.pyxis.greenhopper.jira:gh-sprint"}}
			]`))
		default:
			_, _ = w.Write([]byte(`{"id":"1","key":"ENG-1","fields":{` +
				`"summary":"a Cloud issue in two sprints",` +
				`"status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},` +
				`"created":"2026-01-01T00:00:00.000+0000",` +
				`"updated":"2026-09-22T00:00:00.000+0000",` +
				`"customfield_10020":[` +
				`{"id":12345,"name":"Team Sprint 3","state":"closed"},` +
				`{"id":12346,"name":"Team Sprint 4","state":"active"}` +
				`]}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestACloudSprintFieldStillScalarizes pins the deployment that was never
// broken. scalarizeValue's map branch picks `name` out of each object, and the
// sprint parse cannot reach a value that is not an array of strings, so Cloud
// renders exactly as it did before this change.
func TestACloudSprintFieldStillScalarizes(t *testing.T) {
	url := cloudSprintJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "get", "ENG-1", "--field", "Sprint")

	if !strings.Contains(got.stdout, "Team Sprint 3, Team Sprint 4") {
		t.Errorf("a Cloud sprint field no longer reports its names:\n%s", got.stdout)
	}
	// The structured rendering is Data Center's answer to Data Center's
	// problem. Cloud's value was never a dump, so it does not acquire an
	// element the contract would then owe every Cloud consumer.
	if strings.Contains(got.stdout, "<sprint ") {
		t.Errorf("a Cloud sprint field was restructured:\n%s", got.stdout)
	}
}
