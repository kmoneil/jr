//go:build write

package issue_test

import (
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// writeCatalogueJSON is a field catalogue with one field of every type the
// write path treats differently.
//
// The types and the type keys are copied from a real Data Center, measured on
// 2026-08-21: thirteen custom fields, five of them typed "any", and every one
// of the thirteen carrying a schema.custom key. It is that shape and not an
// invented one, because the whole design turns on "any" being common.
const writeCatalogueJSON = `[
	{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}},
	{"id":"issuetype","name":"Issue Type","custom":false,
	 "schema":{"type":"issuetype"}},
	{"id":"labels","name":"Labels","custom":false,
	 "schema":{"type":"array","items":"string"}},
	{"id":"components","name":"Component/s","custom":false,
	 "schema":{"type":"array","items":"component"}},
	{"id":"customfield_10111","name":"Story Points","custom":true,
	 "clauseNames":["cf[10111]","Story Points"],
	 "schema":{"type":"number",
	           "custom":"com.atlassian.jira.plugin.system.customfieldtypes:float"}},
	{"id":"customfield_13256","name":"Acceptance Criteria","custom":true,
	 "schema":{"type":"string",
	           "custom":"com.atlassian.jira.plugin.system.customfieldtypes:textarea"}},
	{"id":"customfield_10106","name":"Epic Status","custom":true,
	 "schema":{"type":"option","custom":"com.pyxis.greenhopper.jira:gh-epic-status"}},
	{"id":"customfield_10109","name":"Sprint","custom":true,
	 "schema":{"type":"array","items":"string",
	           "custom":"com.pyxis.greenhopper.jira:gh-sprint"}},
	{"id":"customfield_10110","name":"Epic Link","custom":true,
	 "schema":{"type":"any","custom":"com.pyxis.greenhopper.jira:gh-epic-link"}},
	{"id":"customfield_11650","name":"Developers","custom":true,
	 "schema":{"type":"array","items":"user",
	           "custom":"com.atlassian.jira.plugin.system.customfieldtypes:multiuserpicker"}}
]`

// fieldWriteInvocation is one `issue edit --dry-run` carrying the given
// repeated flags, resolved against writeCatalogueJSON.
//
// Driven through the registered command rather than through EditRequest,
// because the layer being tested is validation: resolution and encoding happen
// there so that a refusal costs no write, and a test that called the request
// builder directly would skip the part that can refuse.
func fieldWriteInvocation(repeated map[string][]string) *registry.Invocation {
	flags := registry.NewFlags()
	flags.SetBool("dry-run", true)
	for name, values := range repeated {
		for _, v := range values {
			flags.SetString(name, v)
		}
	}
	return &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: writeCatalogueJSON}, kind: site.DataCenter,
		},
		Args: []string{"ENG-101"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
}

// fieldWriteRequestBody runs a command as far as its dry run and returns the
// verbatim body it would have sent.
//
// The bytes rather than a decoded map, because the encoding is the thing under
// test: 5 and "5" decode to different Go values but a test that compares maps
// has to say so twice, and the JSON a map re-encodes to is not the JSON that
// would have gone out.
func fieldWriteRequestBody(t *testing.T, command string, inv *registry.Invocation) string {
	t.Helper()
	cmd, ok := registry.Lookup(command)
	if !ok {
		t.Fatalf("%s is not registered", command)
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	doc, err := cmd.Run(t.Context(), inv)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(doc.Record.Children) != 1 {
		t.Fatalf("the dry run carried %d requests", len(doc.Record.Children))
	}
	body, ok := doc.Record.Children[0].ChildNamed("body")
	if !ok {
		t.Fatal("the dry run printed no body")
	}
	return body.Text
}

// TestFieldWriteEncodesFromTheSitesOwnTypes is the point of --field: the value
// is typed by the catalogue, not by what it looks like.
//
// `--field 'Story Points=5'` sends the JSON number 5 because the site says that
// field is a number, and the same five against a string field sends "5". A
// tool that guessed from the characters would send one of the two for both.
func TestFieldWriteEncodesFromTheSitesOwnTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  []string
		want string
	}{
		{
			name: "a number field takes a JSON number",
			set:  []string{"Story Points=5"},
			want: `{"fields":{"customfield_10111":5}}`,
		},
		{
			name: "a fractional number survives",
			set:  []string{"customfield_10111=0.5"},
			want: `{"fields":{"customfield_10111":0.5}}`,
		},
		{
			name: "a string field takes the characters",
			set:  []string{"Acceptance Criteria=5"},
			want: `{"fields":{"customfield_13256":"5"}}`,
		},
		{
			// The value is lower case on purpose. Two capitalised words
			// joined by an equals sign is the shape of a scrub mapping, and
			// internal/lint refuses that shape wherever it appears rather
			// than adjudicating which instances are real.
			name: "an option is wrapped in value",
			set:  []string{"Epic Status=done"},
			want: `{"fields":{"customfield_10106":{"value":"done"}}}`,
		},
		{
			name: "an array of strings from one flag is one element",
			set:  []string{"Sprint=14"},
			want: `{"fields":{"customfield_10109":["14"]}}`,
		},
		{
			name: "a repeated id appends rather than replacing",
			set:  []string{"Sprint=14", "Sprint=15"},
			want: `{"fields":{"customfield_10109":["14","15"]}}`,
		},
		{
			name: "an array of components is named, not valued",
			set:  []string{"components=api"},
			want: `{"fields":{"components":[{"name":"api"}]}}`,
		},
		{
			name: "a value containing an equals sign keeps it",
			set:  []string{"Acceptance Criteria=a=b"},
			want: `{"fields":{"customfield_13256":"a=b"}}`,
		},
		{
			name: "a value may be empty",
			set:  []string{"Acceptance Criteria="},
			want: `{"fields":{"customfield_13256":""}}`,
		},
		{
			name: "nothing is split on a comma",
			set:  []string{"Sprint=14,15"},
			want: `{"fields":{"customfield_10109":["14,15"]}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := fieldWriteInvocation(map[string][]string{"field": tc.set})
			if got := fieldWriteRequestBody(t, "issue.edit", inv); got != tc.want {
				t.Errorf("body  = %s\nwant  = %s", got, tc.want)
			}
		})
	}
}

// TestFieldJSONIsSentAsWritten is the escape hatch, and the reason --field is
// allowed to refuse a type instead of guessing at it.
func TestFieldJSONIsSentAsWritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  []string
		want string
	}{
		{
			name: "a bare string for an any-typed field",
			set:  []string{`customfield_10110="ENG-42"`},
			want: `{"fields":{"customfield_10110":"ENG-42"}}`,
		},
		{
			name: "an object for a field with no encoding here",
			set:  []string{`Developers=[{"name":"ada"}]`},
			want: `{"fields":{"customfield_11650":[{"name":"ada"}]}}`,
		},
		{
			name: "null clears a field",
			set:  []string{`customfield_10110=null`},
			want: `{"fields":{"customfield_10110":null}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := fieldWriteInvocation(map[string][]string{"field-json": tc.set})
			if got := fieldWriteRequestBody(t, "issue.edit", inv); got != tc.want {
				t.Errorf("body  = %s\nwant  = %s", got, tc.want)
			}
		})
	}
}

// TestFieldWriteRefusesBeforeAnythingIsSent is the whole contract of this
// feature stated as refusals. Every one of them costs no write: they happen in
// validation, which runs before the request is built.
func TestFieldWriteRefusesBeforeAnythingIsSent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		repeated map[string][]string
		code     string
		detail   string
	}{
		{
			name:     "a bare name is not a write",
			repeated: map[string][]string{"field": {"Story Points"}},
			code:     "FIELD_NOT_KV",
			// The message has to distinguish itself from the read-side --field,
			// which does take a bare name. Somebody who knows one will type it
			// at the other.
			detail: "selects a column",
		},
		{
			name:     "an empty name names no field",
			repeated: map[string][]string{"field": {"=5"}},
			code:     "FIELD_NOT_KV",
		},
		{
			name: "an unknown field is refused, not sent",
			// A dropped letter rather than a transposition. The obvious
			// transposition of "Story" is in misspell's dictionary, and the
			// linter refuses it wherever it appears, including here.
			repeated: map[string][]string{"field": {"Story Point=5"}},
			code:     "UNKNOWN_FIELD",
			detail:   "Story Points",
		},
		{
			name:     "a number field refuses what is not a number",
			repeated: map[string][]string{"field": {"Story Points=abc"}},
			code:     "FIELD_NOT_A_NUMBER",
		},
		{
			name:     "a type this tool will not guess at names the flag that works",
			repeated: map[string][]string{"field": {"Epic Link=ENG-42"}},
			code:     "FIELD_TYPE_UNSUPPORTED",
			detail:   "com.pyxis.greenhopper.jira:gh-epic-link",
		},
		{
			name:     "an array of a type this tool will not guess at is refused too",
			repeated: map[string][]string{"field": {"Developers=ada"}},
			code:     "FIELD_TYPE_UNSUPPORTED",
		},
		{
			name:     "a field a typed flag owns is refused, naming the flag",
			repeated: map[string][]string{"field": {"summary=hello"}},
			code:     "FIELD_HAS_A_FLAG",
			detail:   "--summary",
		},
		{
			name:     "labels belong to --label, not to --field",
			repeated: map[string][]string{"field": {"labels=retry"}},
			code:     "FIELD_HAS_A_FLAG",
		},
		{
			name:     "a scalar set twice has no single answer",
			repeated: map[string][]string{"field": {"Story Points=1", "Story Points=2"}},
			code:     "DUPLICATE_FIELD",
		},
		{
			name: "one field from both flags is refused, not merged",
			repeated: map[string][]string{
				"field":      {"Story Points=1"},
				"field-json": {"customfield_10111=2"},
			},
			code:   "DUPLICATE_FIELD",
			detail: "--field",
		},
		{
			name:     "the same field twice through --field-json",
			repeated: map[string][]string{"field-json": {`customfield_10110="a"`, `customfield_10110="b"`}},
			code:     "DUPLICATE_FIELD",
		},
		{
			name:     "a --field-json value that is not JSON",
			repeated: map[string][]string{"field-json": {"customfield_10110=ENG-42"}},
			code:     "FIELD_NOT_JSON",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.edit")
			if !ok {
				t.Fatal("issue edit is not registered")
			}
			inv := fieldWriteInvocation(tc.repeated)

			err := cmd.Validate(t.Context(), inv)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			e := errs.Coerce(err)
			if e.Code != tc.code {
				t.Errorf("code = %q, want %q", e.Code, tc.code)
			}
			if errs.ExitOf(err) != exitcode.Usage {
				t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
			}
			if tc.detail == "" {
				return
			}
			// The refusal has to carry the thing that makes it actionable,
			// wherever it sits: a near miss, a type key, or the flag to use
			// instead.
			whole := e.Message + " " + e.Detail + " " + e.Remedy
			if !strings.Contains(whole, tc.detail) {
				t.Errorf("refusal does not mention %q:\n%s", tc.detail, whole)
			}
		})
	}
}

// TestAnEditNamingOnlyAFieldIsNotNothing guards the check that refuses an edit
// with no changes. It enumerates flags by name, so a new way to change an issue
// is a new way to trip it.
func TestAnEditNamingOnlyAFieldIsNotNothing(t *testing.T) {
	for _, flag := range []string{"field", "field-json"} {
		t.Run(flag, func(t *testing.T) {
			value := "Story Points=5"
			if flag == "field-json" {
				value = "customfield_10111=5"
			}
			inv := fieldWriteInvocation(map[string][]string{flag: {value}})
			if got := fieldWriteRequestBody(t, "issue.edit", inv); !strings.Contains(got, "customfield_10111") {
				t.Errorf("body = %s", got)
			}
		})
	}
}

// TestFieldWriteReachesCreate asserts the same path on the other verb. Create
// builds its fields map differently, so a merge that worked on edit is not
// evidence about this one.
func TestFieldWriteReachesCreate(t *testing.T) {
	flags := registry.NewFlags()
	flags.SetBool("dry-run", true)
	flags.SetString("type", "Story")
	flags.SetString("summary", "Retry drops the last error")
	flags.SetString("field", "Story Points=5")
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: writeCatalogueJSON},
			kind: site.DataCenter, project: "ENG",
		},
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	}

	body := fieldWriteRequestBody(t, "issue.create", inv)
	for _, want := range []string{
		`"customfield_10111":5`,
		`"summary":"Retry drops the last error"`,
		`"project":{"key":"ENG"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not carry %s:\n%s", want, body)
		}
	}
}

// TestAWriteWithNoFieldFlagsCostsNoCatalogue is why the resolution is guarded.
// The catalogue is a request, and the common invocation must not pay for a
// feature it did not use.
func TestAWriteWithNoFieldFlagsCostsNoCatalogue(t *testing.T) {
	cmd, ok := registry.Lookup("issue.edit")
	if !ok {
		t.Fatal("issue edit is not registered")
	}
	doer := &stubDoer{body: writeCatalogueJSON}
	flags := registry.NewFlags()
	flags.SetString("summary", "a better summary")
	inv := &registry.Invocation{
		Jira:   &stubSession{doer: doer, kind: site.DataCenter},
		Args:   []string{"ENG-101"},
		Flags:  flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doer.calls != 0 {
		t.Errorf("an edit naming no field made %d metadata requests", doer.calls)
	}
}

// TestEveryTypedFlagOwnsItsField is the drift guard on the one list here that
// cannot be derived.
//
// ownedByFlag says which field ids a typed flag writes, and --field refuses
// those so one field never has two spellings. Only the request builders know
// the mapping, so a new typed flag (a --duedate, a --component) added without a
// line in that list would silently reintroduce the last-one-wins this feature
// was designed to refuse, and nothing would fail.
//
// So the test asks the builders. Every typed option is set, and every field id
// that comes out has to be spoken for.
func TestEveryTypedFlagOwnsItsField(t *testing.T) {
	client, _ := writeClient(site.DataCenter)
	created, err := client.CreateRequest(issue.CreateOptions{
		Project: "ENG", Type: "Bug", Summary: "a summary",
		Description: "a description", Priority: "High",
		Labels: []string{"retry"}, Assignee: "ada", Parent: "ENG-1",
	})
	if err != nil {
		t.Fatalf("build create: %v", err)
	}
	edited, err := client.EditRequest(issue.EditOptions{
		Key: "ENG-101", Summary: "a summary", Description: "a description",
		Priority: "High", Labels: []string{"retry"},
		Assignee: "ada", SetAssignee: true, Parent: "ENG-1", SetParent: true,
	})
	if err != nil {
		t.Fatalf("build edit: %v", err)
	}

	for _, tc := range []struct {
		verb string
		body []byte
	}{{"create", created.Body}, {"edit", edited.Body}} {
		t.Run(tc.verb, func(t *testing.T) {
			// Per verb, because the sets differ: `issue edit` has no --type
			// and no --project, so naming either from an edit would offer a
			// flag that command does not have.
			owned := issue.FieldsOwnedByAFlag(tc.verb)
			if len(owned) == 0 {
				t.Fatalf("no fields are owned on %s, so this test asserts nothing", tc.verb)
			}
			var payload struct {
				Fields map[string]any `json:"fields"`
			}
			if err := json.Unmarshal(tc.body, &payload); err != nil {
				t.Fatalf("decode: %v\n%s", err, tc.body)
			}
			if len(payload.Fields) == 0 {
				t.Fatal("the request named no fields, so this test asserts nothing")
			}
			for id := range payload.Fields {
				if !slices.Contains(owned, id) {
					t.Errorf("%s writes %q through a typed flag and --field does "+
						"not refuse it.\nAdd it to ownedByFlag with the flag's "+
						"name, or one field has two spellings and the last one "+
						"wins.\nowned: %v", tc.verb, id, owned)
				}
			}
		})
	}
}

// TestAnEditIsNotOfferedACreateFlag is the other half of keeping the owned set
// per verb. `issue edit` has no --type and no --project, so a refusal naming
// either would send the caller to a flag that command does not have. That is
// worse than no refusal, because it reads as authoritative.
func TestAnEditIsNotOfferedACreateFlag(t *testing.T) {
	inv := fieldWriteInvocation(map[string][]string{"field": {"issuetype=Bug"}})

	// It is accepted, and encoded from the schema type like anything else:
	// changing an issue's type is what Jira's own edit endpoint calls it.
	body := fieldWriteRequestBody(t, "issue.edit", inv)
	if !strings.Contains(body, `"issuetype":{"name":"Bug"}`) {
		t.Errorf("issuetype did not reach the request:\n%s", body)
	}

	// And on create the same field is refused, naming the flag that is there.
	flags := registry.NewFlags()
	flags.SetBool("dry-run", true)
	flags.SetString("type", "Story")
	flags.SetString("summary", "s")
	flags.SetString("field", "issuetype=Bug")
	create := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: writeCatalogueJSON},
			kind: site.DataCenter, project: "ENG",
		},
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	}
	createCmd, ok := registry.Lookup("issue.create")
	if !ok {
		t.Fatal("issue create is not registered")
	}
	err := createCmd.Validate(t.Context(), create)
	if err == nil {
		t.Fatal("issue create accepted --field issuetype")
	}
	e := errs.Coerce(err)
	if e.Code != "FIELD_HAS_A_FLAG" {
		t.Errorf("code = %q, want FIELD_HAS_A_FLAG", e.Code)
	}
	if !strings.Contains(e.Message, "--type") {
		t.Errorf("the refusal did not name --type:\n%s", e.Message)
	}
}
