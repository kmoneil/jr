package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// emptyShapeJira serves one issue carrying every shape an empty custom field
// arrives in, plus two that are not empty and must not be marked as such.
//
// `Absent` is deliberately missing from the payload rather than null: that is
// what a server sends for a field the issue's screen does not carry, and it is
// a different fact from a field that is present and has no value.
func emptyShapeJira(t *testing.T) string {
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
		case strings.HasSuffix(r.URL.Path, "/field"):
			_, _ = w.Write([]byte(`[
				{"id":"customfield_10042","name":"Story Points","custom":true,
				 "schema":{"type":"number"}},
				{"id":"customfield_10050","name":"Team","custom":true,
				 "schema":{"type":"string"}}
			]`))
		case strings.HasSuffix(r.URL.Path, "/search"):
			_, _ = w.Write([]byte(`{"startAt":0,"maxResults":50,"total":1,"issues":[` +
				emptyIssueJSON + `]}`))
		default:
			_, _ = w.Write([]byte(emptyIssueJSON))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

const emptyIssueJSON = `{"id":"1","key":"ENG-1","fields":{` +
	`"summary":"an issue with an empty field",` +
	`"status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},` +
	`"created":"2026-01-01T00:00:00.000+0000",` +
	`"updated":"2026-09-22T00:00:00.000+0000",` +
	`"customfield_10042":null,"customfield_10050":"Platform"}}`

// allEmptyShapesJira serves the four payloads that reduce to an empty cell and
// the two that do not, so the marker is asserted against every one rather than
// against the single case the card happened to name.
//
// customfield_1 is deliberately absent from `fields` rather than null. The
// probe that produced this list found that `0` and `"0"` were already
// distinguishable and never needed fixing, which is the half of issue 159 that
// turned out not to be a defect.
func allEmptyShapesJira(t *testing.T) string {
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
		case strings.HasSuffix(r.URL.Path, "/field"):
			_, _ = w.Write([]byte(`[
				{"id":"customfield_1","name":"Absent","custom":true,"schema":{"type":"number"}},
				{"id":"customfield_2","name":"Null","custom":true,"schema":{"type":"number"}},
				{"id":"customfield_3","name":"Blank","custom":true,"schema":{"type":"string"}},
				{"id":"customfield_4","name":"EmptyList","custom":true,
				 "schema":{"type":"array","items":"string"}},
				{"id":"customfield_5","name":"Zero","custom":true,"schema":{"type":"number"}},
				{"id":"customfield_6","name":"ZeroText","custom":true,"schema":{"type":"string"}}
			]`))
		default:
			_, _ = w.Write([]byte(`{"id":"1","key":"ENG-1","fields":{` +
				`"summary":"every empty shape at once",` +
				`"status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},` +
				`"created":"2026-01-01T00:00:00.000+0000",` +
				`"updated":"2026-09-22T00:00:00.000+0000",` +
				`"customfield_2":null,"customfield_3":"","customfield_4":[],` +
				`"customfield_5":0,"customfield_6":"0"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestEveryEmptyShapeIsMarkedAndNoOtherIs is the measured version of this card.
//
// Four payloads reduce to an empty cell: a key absent from `fields`, an
// explicit null, an empty string, and an empty array. All four are unset. A
// numeric zero and the string "0" are values and must not be marked, which is
// the case the card originally named as the defect and which already worked.
func TestEveryEmptyShapeIsMarkedAndNoOtherIs(t *testing.T) {
	url := allEmptyShapesJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "get", "ENG-1",
		"--field", "Absent", "--field", "Null", "--field", "Blank",
		"--field", "EmptyList", "--field", "Zero", "--field", "ZeroText")

	for _, unset := range []string{
		`<customfield_1 name="Absent" set="false"/>`,
		`<customfield_2 name="Null" set="false"/>`,
		`<customfield_3 name="Blank" set="false"/>`,
		`<customfield_4 name="EmptyList" set="false"/>`,
	} {
		if !strings.Contains(got.stdout, unset) {
			t.Errorf("not marked unset: %s\n%s", unset, got.stdout)
		}
	}
	for _, set := range []string{
		`<customfield_5 name="Zero">0</customfield_5>`,
		`<customfield_6 name="ZeroText">0</customfield_6>`,
	} {
		if !strings.Contains(got.stdout, set) {
			t.Errorf("a value was lost or marked unset: %s\n%s", set, got.stdout)
		}
	}
	if n := strings.Count(got.stdout, `set="false"`); n != 4 {
		t.Errorf("want 4 unset markers, got %d:\n%s", n, got.stdout)
	}
}

// TestAnUnsetFieldSaysSoRatherThanLookingBroken is the defect this file exists
// for. A field the issue has no value for rendered as `<customfield_10042/>`,
// which the reporter said "looks as much like a malformed response as an empty
// value", and they were right: nothing in the document says which it is.
func TestAnUnsetFieldSaysSoRatherThanLookingBroken(t *testing.T) {
	url := emptyShapeJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "get", "ENG-1",
		"--field", "Story Points", "--field", "Team")

	if !strings.Contains(got.stdout, `set="false"`) {
		t.Errorf("an unset field does not say it is unset:\n%s", got.stdout)
	}
	// And a field that does have a value must not be marked, or the attribute
	// says nothing.
	if strings.Count(got.stdout, `set="false"`) != 1 {
		t.Errorf("want exactly one unset marker, got %d:\n%s",
			strings.Count(got.stdout, `set="false"`), got.stdout)
	}
}

// TestARequestedFieldCarriesItsName is the other half of issue 159. The caller
// asked for `Story Points` and had to carry an id mapping to read their own
// result, and that mapping does not travel between sites.
//
// The name comes from the site's catalogue rather than from what was typed, so
// `--field 'Story Points'` and `--field customfield_10042` produce identical
// bytes. That is what makes this safe to add where changing the TSV header,
// which is deliberately the id, would not be.
func TestARequestedFieldCarriesItsName(t *testing.T) {
	url := emptyShapeJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	byName := mustRun(t, env, "issue", "get", "ENG-1", "--field", "Story Points")
	if !strings.Contains(byName.stdout, `name="Story Points"`) {
		t.Errorf("a requested field does not carry its name:\n%s", byName.stdout)
	}

	byID := mustRun(t, env, "issue", "get", "ENG-1", "--field", "customfield_10042")
	if byID.stdout != byName.stdout {
		t.Errorf("the two spellings of one field produced different bytes:\n%s\n---\n%s",
			byName.stdout, byID.stdout)
	}
}

// TestTheTSVBytesDoNotMove is the guard on the half of issue 159 that was
// declined. The column header stays the id, because how a request was spelled
// is not part of the contract, and two spellings still collapse to one column.
func TestTheTSVBytesDoNotMove(t *testing.T) {
	url := emptyShapeJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "list",
		"--field", "Story Points", "--field", "customfield_10042", "--format", "tsv")

	header := strings.Split(strings.Split(got.stdout, "\n")[0], "\t")
	if n := strings.Count(strings.Join(header, "\t"), "customfield_10042"); n != 1 {
		t.Errorf("two spellings did not collapse to one column: %v", header)
	}
	for _, h := range header {
		if h == "Story Points" {
			t.Errorf("the TSV header is the typed name rather than the id: %v", header)
		}
	}
}
