package jql_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/jql"
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
// rendering it as a value and lexing the result yields one string token holding
// the original.
func FuzzValuesRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"", "plain", `"`, `\`, `\"`, "a\nb", "a\tb", `" OR 1=1`,
		"unicode é", "🙂", `))((`, "'", `''`, "EMPTY",
		"\xc4", "\xff\xfe", "valid\xc4invalid",
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

		tokens, err := jql.Tokenize(rendered)
		if err != nil {
			t.Fatalf("render(%q) produced unlexable JQL %s: %v", value, rendered, err)
		}
		if len(tokens) != 1 || tokens[0].Kind != jql.TokString {
			t.Fatalf("render(%q) produced %d token(s), want one string: %s",
				value, len(tokens), rendered)
		}
		if tokens[0].Text != value {
			t.Fatalf("round trip lost data\n got %q\nwant %q", tokens[0].Text, value)
		}
	})
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
