package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// keyFieldJira serves one issue and a catalogue carrying `issuekey`, the
// field both deployments list under the name "Key".
//
// The catalogue entry is Cloud's, copied from the sandbox's /field on
// 2026-11-04: no schema, and six clause names, so `--field id` and
// `--field workItem` reach it as surely as `--field key`. The issue payload
// carries the
// key where Jira puts it, at the top level and nowhere in `fields`: asked for
// every system field by id, Cloud returned all of them in `fields` except
// `issuekey`, `thumbnail`, `issuerestriction` and an unset `parent`, and Data
// Center did the same for `issuekey` in issue 274.
func keyFieldJira(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"10.3.26","deploymentType":"Server",` +
				`"serverTime":"2026-11-04T12:00:00.000+0000"}`))
		case strings.HasSuffix(r.URL.Path, "/field"):
			_, _ = w.Write([]byte(`[
				{"id":"issuekey","key":"issuekey","name":"Key","custom":false,
				 "orderable":false,"navigable":true,"searchable":false,
				 "clauseNames":["id","issue","issuekey","key","workItem","workItemKey"]},
				{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}},
				{"id":"customfield_10042","name":"Story Points","custom":true,
				 "schema":{"type":"number"}}
			]`))
		case strings.HasSuffix(r.URL.Path, "/search"):
			_, _ = w.Write([]byte(`{"startAt":0,"maxResults":50,"total":1,"issues":[` +
				keyFieldIssueJSON + `]}`))
		default:
			_, _ = w.Write([]byte(keyFieldIssueJSON))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

const keyFieldIssueJSON = `{"id":"1","key":"ENG-102","fields":{` +
	`"summary":"Summary one",` +
	`"status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},` +
	`"created":"2026-11-04T17:00:00.000+0000",` +
	`"updated":"2026-11-04T18:56:22.000+0000",` +
	`"customfield_10042":5}}`

// TestNamingTheKeyChangesNothing is issue 274. `--field key` resolved to
// `issuekey`, which Jira never puts in `fields`, so it became a column that was
// empty on every row and an element that said `set="false"` about an issue
// whose key was the first column. Two answers for one fact, and the wrong one
// is a claim about the data.
//
// The key is already in every row, so naming it is honoured the way naming
// `summary` is: by it being there. The name, the id and two of the clause
// names are driven, because each reaches the same catalogue entry by a
// different path through Resolve.
func TestNamingTheKeyChangesNothing(t *testing.T) {
	url := keyFieldJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	for _, format := range []string{"tsv", "xml", "json"} {
		for _, verb := range [][]string{
			{"issue", "list", "--format", format},
			{"issue", "get", "ENG-102", "--format", format},
		} {
			plain := mustRun(t, env, verb...)
			for _, spelling := range []string{"key", "Key", "issuekey", "id", "workItem"} {
				got := mustRun(t, env, append(verb, "--field", spelling)...)
				if strings.Contains(got.stdout, "issuekey") {
					t.Errorf("%v --field %s reports an issuekey field:\n%s",
						verb, spelling, got.stdout)
				}
				if got.stdout != plain.stdout {
					t.Errorf("%v --field %s changed the output:\n%s\n--- without it:\n%s",
						verb, spelling, got.stdout, plain.stdout)
				}
			}
		}
	}
}

// TestTheKeyBesideAFieldLeavesOneColumn guards the other half: dropping the
// key must not drop what was named with it.
func TestTheKeyBesideAFieldLeavesOneColumn(t *testing.T) {
	url := keyFieldJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "list",
		"--field", "key", "--field", "Story Points", "--format", "tsv")

	lines := strings.Split(got.stdout, "\n")
	header := strings.Split(lines[0], "\t")
	if header[0] != "key" {
		t.Errorf("the first column is %q, want key: %v", header[0], header)
	}
	if last := header[len(header)-1]; last != "customfield_10042" {
		t.Errorf("the last column is %q, want customfield_10042: %v", last, header)
	}
	if n := strings.Count(lines[0], "key"); n != 1 {
		t.Errorf("the header names the key %d times: %v", n, header)
	}
	row := strings.Split(lines[1], "\t")
	if row[0] != "ENG-102" || row[len(row)-1] != "5" {
		t.Errorf("row = %v, want ENG-102 first and 5 last", row)
	}
}
