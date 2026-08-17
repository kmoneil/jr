package jql_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/jql"
)

func render(t *testing.T, b *jql.Builder) string {
	t.Helper()
	got, err := b.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return got
}

func TestBuilder(t *testing.T) {
	cases := []struct {
		name  string
		build func() *jql.Builder
		want  string
	}{
		{
			"empty",
			func() *jql.Builder { return jql.New() },
			"",
		},
		{
			"single project",
			func() *jql.Builder { return jql.New().Project("ENG") },
			`project = "ENG"`,
		},
		{
			"several projects",
			func() *jql.Builder { return jql.New().Project("ENG", "OPS") },
			`project IN ("ENG", "OPS")`,
		},
		{
			"no projects drops the filter",
			func() *jql.Builder { return jql.New().Project().Eq("status", "Done") },
			`status = "Done"`,
		},
		{
			"conjunction",
			func() *jql.Builder {
				return jql.New().Project("ENG").Eq("status", "In Progress")
			},
			`project = "ENG" AND status = "In Progress"`,
		},
		{
			"repeated labels accumulate",
			func() *jql.Builder {
				return jql.New().In("labels", "retry", "transport")
			},
			`labels IN ("retry", "transport")`,
		},
		{
			"negation is its own filter",
			func() *jql.Builder { return jql.New().NotIn("labels", "wontfix", "duplicate") },
			`labels NOT IN ("wontfix", "duplicate")`,
		},
		{
			"is empty",
			func() *jql.Builder { return jql.New().IsEmpty("assignee") },
			`assignee IS EMPTY`,
		},
		{
			"is not empty",
			func() *jql.Builder { return jql.New().IsNotEmpty("fixVersion") },
			`fixVersion IS NOT EMPTY`,
		},
		{
			"order by",
			func() *jql.Builder {
				return jql.New().Project("ENG").OrderBy("updated", jql.Desc)
			},
			`project = "ENG" ORDER BY updated DESC`,
		},
		{
			"order by several fields",
			func() *jql.Builder {
				return jql.New().OrderBy("priority", jql.Desc).OrderBy("created", jql.Asc)
			},
			`ORDER BY priority DESC, created ASC`,
		},
		{
			"order defaults to ascending",
			func() *jql.Builder { return jql.New().OrderBy("created", "") },
			`ORDER BY created ASC`,
		},
		{
			"disjunction nested in a conjunction is grouped",
			func() *jql.Builder {
				return jql.New().Project("ENG").Where(jql.AnyOf(
					jql.Eq("status", "Done"),
					jql.Eq("status", "Closed"),
				))
			},
			`project = "ENG" AND (status = "Done" OR status = "Closed")`,
		},
		{
			"single-element disjunction needs no grouping",
			func() *jql.Builder {
				return jql.New().Project("ENG").Where(jql.AnyOf(jql.Eq("status", "Done")))
			},
			`project = "ENG" AND status = "Done"`,
		},
		{
			"negation",
			func() *jql.Builder {
				return jql.New().Where(jql.Negate(jql.Eq("status", "Done")))
			},
			`NOT (status = "Done")`,
		},
		{
			"reserved field name is quoted",
			func() *jql.Builder { return jql.New().Eq("order", "1") },
			`"order" = "1"`,
		},
		{
			"field name with a space is quoted",
			func() *jql.Builder { return jql.New().Eq("Story Points", "3") },
			`"Story Points" = "3"`,
		},
		{
			"custom field id passes through",
			func() *jql.Builder { return jql.New().Eq("customfield_10042", "x") },
			`customfield_10042 = "x"`,
		},
		{
			"cf reference passes through unquoted",
			func() *jql.Builder { return jql.New().Eq("cf[10042]", "x") },
			`cf[10042] = "x"`,
		},
		{
			"like",
			func() *jql.Builder { return jql.New().Like("summary", "retry") },
			`summary ~ "retry"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(t, tc.build()); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestRawIsAlwaysParenthesized is the rule that separates a filter from a scope
// escape. Without the parentheses, the user's OR binds looser than the AND that
// scopes the query to a project, and the query returns every matching issue in
// every project the caller can see.
func TestRawIsAlwaysParenthesized(t *testing.T) {
	got := render(t, jql.New().
		Project("ENG").
		Raw(`summary ~ "x" OR priority = Highest`))

	want := `project = "ENG" AND (summary ~ "x" OR priority = Highest)`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	// The unparenthesized form is what the incumbent produces. Assert we never
	// emit it, whatever else changes about the renderer.
	escaped := `project = "ENG" AND summary ~ "x" OR priority = Highest`
	if got == escaped {
		t.Fatal("raw JQL escaped the project scope")
	}
}

func TestRawVariants(t *testing.T) {
	cases := []struct{ name, raw, want string }{
		{"simple", "status = Done", `(status = Done)`},
		{"already parenthesized", "(status = Done)", `((status = Done))`},
		{"empty is dropped", "", ``},
		{"whitespace is dropped", "   ", ``},
		{"leading OR is contained", "OR priority = Highest", `(OR priority = Highest)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(t, jql.New().Raw(tc.raw)); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestValuesAreNeverSyntax is the injection test. Every one of these is a value
// a user could legitimately type, and none of them may change the shape of the
// query.
func TestValuesAreNeverSyntax(t *testing.T) {
	hostile := []string{
		`" OR project = OPS`,
		`" OR project = OPS --`,
		`x" AND "1`,
		`\`,
		`\\`,
		`\" OR 1=1`,
		`')`,
		`") OR (project = OPS`,
		"line\nbreak",
		"tab\there",
		`nested "quotes" inside`,
		`ORDER BY created`,
		`EMPTY`,
		`NULL`,
		`currentUser()`,
	}

	for _, value := range hostile {
		t.Run(value, func(t *testing.T) {
			got := render(t, jql.New().Project("ENG").Eq("summary", value))

			prefix := `project = "ENG" AND summary = `
			if !strings.HasPrefix(got, prefix) {
				t.Fatalf("query shape changed: %s", got)
			}

			// The rendered value must lex back to exactly one string token
			// carrying exactly the original bytes.
			tokens, err := jql.Tokenize(strings.TrimPrefix(got, prefix))
			if err != nil {
				t.Fatalf("rendered value does not lex: %v\nquery: %s", err, got)
			}
			if len(tokens) != 1 {
				t.Fatalf("value lexed as %d tokens, want 1: %s", len(tokens), got)
			}
			if tokens[0].Kind != jql.TokString {
				t.Fatalf("value lexed as %s, want a string: %s", tokens[0].Kind, got)
			}
			if tokens[0].Text != value {
				t.Fatalf("value did not round-trip\n got %q\nwant %q", tokens[0].Text, value)
			}
		})
	}
}

// FuzzValuesRoundTrip is the property behind the table above: for any input,
// rendering it as a value and reading it back yields the original.
//
// It reads the value back with jql.Unquote rather than by lexing here, because
// Unquote is what every caller outside this package uses to read a value Jira
// rendered, and the label suggestion endpoint answers in this same spelling. A
// property asserted against a hand-rolled equivalent of the function under
// test is a property the function does not have.
func FuzzValuesRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"", "plain", `"`, `\`, `\"`, "a\nb", "a\tb", `" OR 1=1`,
		"unicode é", "🙂", `))((`, "'", `''`, "EMPTY",
		"\xc4", "\xff\xfe", "valid\xc4invalid",
		// Labels Jira stores and hands back quoted, measured on both
		// deployments 2026-08-13.
		"a,b", `back\slash`, "brac[ket]", `q"uote`, "uni-café",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		got, err := jql.New().Eq("summary", value).Render()
		if err != nil {
			// The only value this may refuse is one that cannot be transmitted
			// at all. Refusing is correct; silently substituting U+FFFD for an
			// invalid byte would match a different issue than the caller asked
			// for, and say nothing about it.
			assertUsageError(t, err, "INVALID_ENCODING")
			if utf8.ValidString(value) {
				t.Fatalf("render(%q) refused a representable value: %v", value, err)
			}
			return
		}

		const prefix = "summary = "
		if !strings.HasPrefix(got, prefix) {
			t.Fatalf("render(%q) changed the query shape: %s", value, got)
		}
		rendered := strings.TrimPrefix(got, prefix)

		read, err := jql.Unquote(rendered)
		if err != nil {
			t.Fatalf("render(%q) produced %s, which Unquote refuses: %v",
				value, rendered, err)
		}
		if read != value {
			t.Fatalf("round trip lost data\n got %q\nwant %q", read, value)
		}
	})
}

// TestUnquoteReadsWhatJiraRenders covers the spellings the label suggestion
// endpoint answers with. Jira quotes a value only when it has to, so both
// halves of that rule have to be read: `retry` arrives bare and `a,b` arrives
// as five characters with the quotes in them.
func TestUnquoteReadsWhatJiraRenders(t *testing.T) {
	for _, tc := range []struct{ rendered, want string }{
		{`retry`, "retry"},
		{`zz-000`, "zz-000"},
		{`uni-café`, "uni-café"},
		{`2026`, "2026"},
		{`"a,b"`, "a,b"},
		{`"brac[ket]"`, "brac[ket]"},
		{`"back\\slash"`, `back\slash`},
		{`"q\"uote"`, `q"uote`},
		{`"spaced out"`, "spaced out"},
		{`""`, ""},
	} {
		got, err := jql.Unquote(tc.rendered)
		if err != nil {
			t.Errorf("Unquote(%s): %v", tc.rendered, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Unquote(%s) = %q, want %q", tc.rendered, got, tc.want)
		}
	}
}

// TestUnquoteRefusesWhatIsNotOneValue is the guard that keeps a caller from
// comparing against half of something. Reading `a,b` bare would answer "a",
// which is a label that may well exist while the one asked about does not.
func TestUnquoteRefusesWhatIsNotOneValue(t *testing.T) {
	for _, rendered := range []string{
		`a,b`,        // three tokens, which is why Jira quotes this one
		`a b`,        // two
		``,           // none
		`"unclosed`,  // a lex error
		`=`,          // an operator is not a value
		`(`,          // nor is a parenthesis
		`"bad\qesc"`, // an escape JQL does not define
	} {
		if got, err := jql.Unquote(rendered); err == nil {
			t.Errorf("Unquote(%q) = %q, want a refusal", rendered, got)
		}
	}
}

func TestRenderRejectsMalformedExpressions(t *testing.T) {
	cases := map[string]*jql.Query{
		"clause with no field": {
			Where: &jql.Clause{Op: jql.OpEq, Value: jql.Text("x")},
		},
		"clause with no operator": {
			Where: &jql.Clause{Field: "status", Value: jql.Text("x")},
		},
		"clause with no value": {
			Where: &jql.Clause{Field: "status", Op: jql.OpEq},
		},
		"IN with a single value": {
			Where: &jql.Clause{Field: "status", Op: jql.OpIn, Value: jql.Text("x")},
		},
		"equality with a list": {
			Where: &jql.Clause{
				Field: "status", Op: jql.OpEq,
				Value: jql.List{jql.Text("a"), jql.Text("b")},
			},
		},
		"IN with an empty list": {
			Where: &jql.Clause{Field: "status", Op: jql.OpIn, Value: jql.List{}},
		},
		"nested list": {
			Where: &jql.Clause{
				Field: "status", Op: jql.OpIn,
				Value: jql.List{jql.List{jql.Text("a")}},
			},
		},
		"unknown keyword": {
			Where: &jql.Clause{Field: "status", Op: jql.OpIs, Value: jql.Keyword("MAYBE")},
		},
		"non-numeric number": {
			Where: &jql.Clause{Field: "votes", Op: jql.OpGt, Value: jql.Num("many")},
		},
		"bad function name": {
			Where: &jql.Clause{
				Field: "assignee", Op: jql.OpEq,
				Value: &jql.Func{Name: "current user"},
			},
		},
		"NOT with no operand": {
			Where: &jql.Not{},
		},
		"unknown sort direction": {
			Order: []jql.Order{{Field: "created", Direction: jql.Direction("sideways")}},
		},
		"sort with no field": {
			Order: []jql.Order{{Field: "", Direction: jql.Asc}},
		},
	}

	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := jql.Render(q)
			if err == nil {
				t.Fatalf("Render accepted a malformed query and produced %q", got)
			}
			if got != "" {
				t.Errorf("Render returned %q alongside an error", got)
			}
		})
	}
}

func TestRenderRejectsNilQuery(t *testing.T) {
	if _, err := jql.Render(nil); err == nil {
		t.Fatal("Render(nil) succeeded")
	}
}

func TestKeywordsAndFunctionsAreNotQuoted(t *testing.T) {
	q := &jql.Query{Where: &jql.And{Exprs: []jql.Expr{
		&jql.Clause{Field: "assignee", Op: jql.OpIs, Value: jql.Empty},
		&jql.Clause{Field: "reporter", Op: jql.OpEq, Value: &jql.Func{Name: "currentUser"}},
		&jql.Clause{
			Field: "created", Op: jql.OpGte,
			Value: &jql.Func{Name: "startOfWeek", Args: []jql.Value{jql.Text("-1")}},
		},
		&jql.Clause{Field: "votes", Op: jql.OpGt, Value: jql.Num("3")},
	}}}

	got, err := jql.Render(q)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `assignee IS EMPTY AND reporter = currentUser() AND ` +
		`created >= startOfWeek("-1") AND votes > 3`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestIsReserved(t *testing.T) {
	for _, w := range []string{"and", "AND", "Order", "select", "cf", "empty"} {
		if !jql.IsReserved(w) {
			t.Errorf("IsReserved(%q) = false", w)
		}
	}
	for _, w := range []string{"project", "summary", "assignee", "customfield_1"} {
		if jql.IsReserved(w) {
			t.Errorf("IsReserved(%q) = true", w)
		}
	}
}

func TestParseDirection(t *testing.T) {
	cases := map[string]jql.Direction{
		"asc": jql.Asc, "ASC": jql.Asc, "ascending": jql.Asc,
		"desc": jql.Desc, "DESC": jql.Desc, " descending ": jql.Desc,
	}
	for in, want := range cases {
		got, ok := jql.ParseDirection(in)
		if !ok || got != want {
			t.Errorf("ParseDirection(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	// There is no --reverse, and no third direction that silently means one of
	// the other two.
	for _, in := range []string{"", "reverse", "up", "1", "descend"} {
		if _, ok := jql.ParseDirection(in); ok {
			t.Errorf("ParseDirection(%q) was accepted", in)
		}
	}
}

func assertUsageError(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error %s, got nil", wantCode)
	}
	e := errs.Coerce(err)
	if e.Code != wantCode {
		t.Errorf("error code = %q, want %q (%s)", e.Code, wantCode, e.Message)
	}
	if e.Exit != exitcode.Usage {
		t.Errorf("%s exits %v, want %v", e.Code, e.Exit, exitcode.Usage)
	}
}

// TestACustomFieldIdIsDigitsOnly pins the test that decides whether `cf[10009]`
// is a field reference or a string.
//
// A custom field id is `cf[` then digits then `]`, and the digit test is a pair
// of bounds. Moving either one by a character makes `0` or `9` stop being a
// digit, and every custom field id in this package's tests happened to be
// spellable without one of them. The consequence is not cosmetic: a field
// reference that gets quoted is sent to Jira as a string, and the query then
// asks about a value nobody stored.
func TestACustomFieldIdIsDigitsOnly(t *testing.T) {
	for _, tc := range []struct {
		field string
		want  string
	}{
		{"cf[10009]", `cf[10009] = "x"`},
		{"cf[123]", `cf[123] = "x"`},
		{"cf[1x]", `"cf[1x]" = "x"`},
		{"cf[]", `"cf[]" = "x"`},
		{"cf[abc]", `"cf[abc]" = "x"`},
	} {
		t.Run(tc.field, func(t *testing.T) {
			if got := jql.New().Eq(tc.field, "x").String(); got != tc.want {
				t.Errorf("Eq(%q) = %q, want %q", tc.field, got, tc.want)
			}
		})
	}
}

// TestAQueryWithNoConditionsHasNoWhere is the difference between a query that
// asks for everything in an order and a query that asks for everything matching
// nothing in particular.
//
// The builder decides on the count of conditions it accumulated, and a bound
// moved by one turns zero conditions into an empty `And`. Nothing here built a
// query with an order and no conditions, which is exactly what `--order` on its
// own produces.
func TestAQueryWithNoConditionsHasNoWhere(t *testing.T) {
	q := jql.New().OrderBy("updated", jql.Desc).Query()
	if q.Where != nil {
		t.Errorf("a builder with no conditions produced a WHERE: %#v", q.Where)
	}

	got, err := jql.Render(q)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if want := "ORDER BY updated DESC"; got != want {
		t.Errorf("render = %q, want %q", got, want)
	}
}
