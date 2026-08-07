package jql_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/jql"
)

// unknownExpr is an Expr the renderer has never heard of. The AST interface is
// unexported precisely so this cannot happen outside a test, but the renderer
// still refuses rather than emitting a query with a hole in it.
type unknownExpr struct{ jql.Expr }

type unknownValue struct{ jql.Value }

func TestRendererRefusesUnknownNodes(t *testing.T) {
	if _, err := jql.Render(&jql.Query{Where: unknownExpr{}}); err == nil {
		t.Error("Render accepted an unknown expression type")
	}
	if _, err := jql.Render(&jql.Query{
		Where: &jql.Clause{Field: "x", Op: jql.OpEq, Value: unknownValue{}},
	}); err == nil {
		t.Error("Render accepted an unknown value type")
	}
	if _, err := jql.Render(&jql.Query{
		Where: &jql.Clause{Field: "x", Op: jql.OpIn, Value: jql.List{unknownValue{}}},
	}); err == nil {
		t.Error("Render accepted an unknown value inside a list")
	}
	if _, err := jql.Render(&jql.Query{
		Where: &jql.Clause{
			Field: "x", Op: jql.OpEq,
			Value: &jql.Func{Name: "f", Args: []jql.Value{unknownValue{}}},
		},
	}); err == nil {
		t.Error("Render accepted an unknown function argument")
	}
	if _, err := jql.Render(&jql.Query{Where: &jql.Not{Expr: unknownExpr{}}}); err == nil {
		t.Error("Render accepted NOT wrapping an unknown expression")
	}
}

func TestRendererDropsEmptyBranches(t *testing.T) {
	cases := map[string]*jql.Query{
		"nil where":              {},
		"empty conjunction":      {Where: &jql.And{}},
		"empty disjunction":      {Where: &jql.Or{}},
		"conjunction of nothing": {Where: &jql.And{Exprs: []jql.Expr{&jql.Raw{Text: "  "}}}},
		"nil expr in a list":     {Where: &jql.And{Exprs: []jql.Expr{nil}}},
		"NOT of an empty raw":    {Where: &jql.Not{Expr: &jql.Raw{Text: ""}}},
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := jql.Render(q)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if got != "" {
				t.Errorf("rendered %q, want an empty query", got)
			}
		})
	}
}

func TestChanged(t *testing.T) {
	got, err := jql.Render(&jql.Query{
		Where: &jql.Clause{Field: "status", Op: jql.OpChanged},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "status CHANGED" {
		t.Errorf("got %q", got)
	}
}

// TestChangedTakesNoValue pins the replacement for what this used to do.
//
// A value on CHANGED was rendered through renderValue, so a Text became
// `status CHANGED "BY currentUser()"` and a Func became
// `status CHANGED after("-7d")`. Both are a quoted string or a function call
// where JQL wants a bare keyword, so both were rejected by Jira — the operator
// was declared, tokenized, and unusable except through Raw.
func TestChangedTakesNoValue(t *testing.T) {
	for name, v := range map[string]jql.Value{
		"text":     jql.Text("BY currentUser() AFTER -7d"),
		"function": &jql.Func{Name: "after", Args: []jql.Value{jql.Text("-7d")}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := jql.Render(&jql.Query{Where: &jql.Clause{
				Field: "status", Op: jql.OpChanged, Value: v,
			}})
			if err == nil {
				t.Fatal("Render accepted a value on CHANGED")
			}
			if !strings.Contains(err.Error(), "takes no value") {
				t.Errorf("unhelpful error: %v", err)
			}
		})
	}
}

func TestPredicatesRender(t *testing.T) {
	cases := map[string]struct {
		clause *jql.Clause
		want   string
	}{
		"changed by and after": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{
					jql.By(&jql.Func{Name: "currentUser"}),
					jql.After(jql.Text("-7d")),
				},
			},
			want: `status CHANGED BY currentUser() AFTER "-7d"`,
		},
		"changed before": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{jql.Before(jql.Text("2026-01-01"))},
			},
			want: `status CHANGED BEFORE "2026-01-01"`,
		},
		"changed from and to": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{
					{Keyword: jql.PredFrom, Value: jql.Text("To Do")},
					{Keyword: jql.PredTo, Value: jql.Text("Done")},
				},
			},
			want: `status CHANGED FROM "To Do" TO "Done"`,
		},
		"was with a predicate": {
			clause: &jql.Clause{
				Field: "assignee", Op: jql.OpWas,
				Value:      &jql.Func{Name: "currentUser"},
				Predicates: []jql.Predicate{jql.After(jql.Text("-30d"))},
			},
			want: `assignee WAS currentUser() AFTER "-30d"`,
		},
		"during takes a pair": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{{
					Keyword: jql.PredDuring,
					Value:   jql.List{jql.Text("-30d"), jql.Text("-7d")},
				}},
			},
			want: `status CHANGED DURING ("-30d", "-7d")`,
		},
		"on a moment": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{{
					Keyword: jql.PredOn, Value: jql.Text("2026-01-01"),
				}},
			},
			want: `status CHANGED ON "2026-01-01"`,
		},
		"was in a list, qualified": {
			clause: &jql.Clause{
				Field: "assignee", Op: jql.OpWasIn,
				Value:      jql.List{jql.Text("ada"), jql.Text("grace")},
				Predicates: []jql.Predicate{jql.Before(jql.Text("-1d"))},
			},
			want: `assignee WAS IN ("ada", "grace") BEFORE "-1d"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := jql.Render(&jql.Query{Where: tc.clause})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestPredicateValuesAreQuotedLikeAnyOther is the injection case for the one
// place a bare keyword is emitted. The keyword comes from a closed set and the
// value goes through the same escaper as every other value, so a hostile user
// name cannot close the predicate and open a clause.
func TestPredicateValuesAreQuotedLikeAnyOther(t *testing.T) {
	got, err := jql.New().
		Project("ENG").
		Changed("status", jql.By(jql.Text(`ada" OR project = "OPS`))).
		Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `project = "ENG" AND status CHANGED BY "ada\" OR project = \"OPS"`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestPredicatesAreRefused(t *testing.T) {
	cases := map[string]struct {
		clause *jql.Clause
		want   string
	}{
		"on an operator with no history": {
			clause: &jql.Clause{
				Field: "assignee", Op: jql.OpEq, Value: jql.Text("ada"),
				Predicates: []jql.Predicate{jql.After(jql.Text("-7d"))},
			},
			want: "takes no history predicates",
		},
		"unknown keyword": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{{Keyword: "SINCE", Value: jql.Text("-7d")}},
			},
			want: "unknown history predicate",
		},
		"empty keyword": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{{Value: jql.Text("-7d")}},
			},
			want: "unknown history predicate",
		},
		"the same predicate twice": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{
					jql.After(jql.Text("-7d")), jql.After(jql.Text("-1d")),
				},
			},
			want: "appears twice",
		},
		"FROM on WAS": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpWas, Value: jql.Text("Done"),
				Predicates: []jql.Predicate{{Keyword: jql.PredFrom, Value: jql.Text("To Do")}},
			},
			want: "only valid on CHANGED",
		},
		"DURING with one date": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{{Keyword: jql.PredDuring, Value: jql.Text("-7d")}},
			},
			want: "two dates, not one value",
		},
		"DURING with three dates": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{{
					Keyword: jql.PredDuring,
					Value:   jql.List{jql.Text("a"), jql.Text("b"), jql.Text("c")},
				}},
			},
			want: "got 3",
		},
		"a list where one value belongs": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{{
					Keyword: jql.PredBy, Value: jql.List{jql.Text("ada")},
				}},
			},
			want: "one value, not a list",
		},
		"an unrenderable value": {
			clause: &jql.Clause{
				Field: "status", Op: jql.OpChanged,
				Predicates: []jql.Predicate{{Keyword: jql.PredBy, Value: unknownValue{}}},
			},
			want: "unknown value type",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := jql.Render(&jql.Query{Where: tc.clause})
			if err == nil {
				t.Fatal("Render accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestBuilderHistoryClauses(t *testing.T) {
	got, err := jql.New().
		Changed("status", jql.By(&jql.Func{Name: "currentUser"}), jql.After(jql.Text("-7d"))).
		Was("assignee", &jql.Func{Name: "currentUser"}).
		Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `status CHANGED BY currentUser() AFTER "-7d" AND assignee WAS currentUser()`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestHistoricalOperators(t *testing.T) {
	got, err := jql.Render(&jql.Query{Where: &jql.And{Exprs: []jql.Expr{
		&jql.Clause{Field: "status", Op: jql.OpWas, Value: jql.Text("Done")},
		&jql.Clause{Field: "status", Op: jql.OpWasNot, Value: jql.Text("Closed")},
		&jql.Clause{
			Field: "assignee", Op: jql.OpWasIn,
			Value: jql.List{jql.Text("ada"), jql.Text("grace")},
		},
		&jql.Clause{
			Field: "assignee", Op: jql.OpWasNotIn,
			Value: jql.List{jql.Text("bob"), jql.Text("eve")},
		},
	}}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `status WAS "Done" AND status WAS NOT "Closed" AND ` +
		`assignee WAS IN ("ada", "grace") AND assignee WAS NOT IN ("bob", "eve")`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestComparisonOperators(t *testing.T) {
	got, err := jql.Render(&jql.Query{Where: &jql.And{Exprs: []jql.Expr{
		&jql.Clause{Field: "votes", Op: jql.OpGt, Value: jql.Num("1")},
		&jql.Clause{Field: "votes", Op: jql.OpLt, Value: jql.Num("9")},
		&jql.Clause{Field: "watchers", Op: jql.OpGte, Value: jql.Num("2")},
		&jql.Clause{Field: "watchers", Op: jql.OpLte, Value: jql.Num("8")},
		&jql.Clause{Field: "status", Op: jql.OpIsNot, Value: jql.Null},
	}}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `votes > 1 AND votes < 9 AND watchers >= 2 AND watchers <= 8 AND status IS NOT NULL`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestQuoteEscapesEveryControlCharacter walks each branch of the escaper, and
// asserts the result lexes back to exactly what went in.
func TestQuoteEscapesEveryControlCharacter(t *testing.T) {
	cases := []struct{ name, in, escaped string }{
		{"backslash", `a\b`, `"a\\b"`},
		{"double quote", `a"b`, `"a\"b"`},
		{"newline", "a\nb", `"a\nb"`},
		{"carriage return", "a\rb", `"a\rb"`},
		{"tab", "a\tb", `"a\tb"`},
		{"single quote is not escaped", `a'b`, `"a'b"`},
		{"multibyte passes through", "café 🙂", `"café 🙂"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := jql.Render(&jql.Query{
				Where: &jql.Clause{Field: "summary", Op: jql.OpEq, Value: jql.Text(tc.in)},
			})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			want := "summary = " + tc.escaped
			if got != want {
				t.Fatalf("got  %s\nwant %s", got, want)
			}

			tokens, err := jql.Tokenize(tc.escaped)
			if err != nil {
				t.Fatalf("escaped form does not lex: %v", err)
			}
			if len(tokens) != 1 || tokens[0].Text != tc.in {
				t.Errorf("round trip lost data: %+v", tokens)
			}
		})
	}
}

func TestFieldQuoting(t *testing.T) {
	cases := []struct{ field, want string }{
		{"project", "project"},
		{"customfield_10042", "customfield_10042"},
		{"_leading", "_leading"},
		{"cf[10042]", "cf[10042]"},
		{"CF[10042]", "CF[10042]"},
		{"cf[]", `"cf[]"`},
		{"cf[abc]", `"cf[abc]"`},
		{"cf[10042", `"cf[10042"`},
		{"1field", `"1field"`},
		{"field-with-dash", `"field-with-dash"`},
		{"Story Points", `"Story Points"`},
		{"order", `"order"`},
		{"ORDER", `"ORDER"`},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			got, err := jql.Render(&jql.Query{
				Where: &jql.Clause{Field: tc.field, Op: jql.OpEq, Value: jql.Text("x")},
			})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if want := tc.want + ` = "x"`; got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
		})
	}
}

func TestOrderByFieldIsQuotedToo(t *testing.T) {
	got, err := jql.New().OrderBy("Story Points", jql.Desc).Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != `ORDER BY "Story Points" DESC` {
		t.Errorf("got %q", got)
	}
}

func TestBuilderComparisons(t *testing.T) {
	got, err := jql.New().
		Ne("status", "Done").
		NotLike("summary", "wip").
		Gte("votes", jql.Num("2")).
		Lte("votes", jql.Num("8")).
		Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `status != "Done" AND summary !~ "wip" AND votes >= 2 AND votes <= 8`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestBuilderIgnoresEmptyInput(t *testing.T) {
	got, err := jql.New().
		Where(nil).
		OrderBy("", jql.Asc).
		Project().
		In("labels").
		NotIn("labels").
		Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "" {
		t.Errorf("rendered %q from nothing", got)
	}
}

func TestBuilderStringIsRenderOrEmpty(t *testing.T) {
	if got := jql.New().Project("ENG").String(); got != `project = "ENG"` {
		t.Errorf("String() = %q", got)
	}
	// A query that cannot render yields the empty string rather than a partial
	// one; callers that need the reason use Render.
	if got := jql.New().Where(unknownExpr{}).String(); got != "" {
		t.Errorf("String() on an unrenderable query = %q, want empty", got)
	}
}

func TestAllOfAndNestedGrouping(t *testing.T) {
	got, err := jql.New().
		Project("ENG").
		Where(jql.AnyOf(
			jql.AllOf(jql.Eq("status", "Done"), jql.Eq("resolution", "Fixed")),
			jql.Eq("status", "Closed"),
		)).
		Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `project = "ENG" AND ` +
		`((status = "Done" AND resolution = "Fixed") OR status = "Closed")`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSingleValueInCollapsesToEquality(t *testing.T) {
	if got := jql.New().In("labels", "retry").String(); got != `labels = "retry"` {
		t.Errorf("In with one value = %q", got)
	}
	if got := jql.New().NotIn("labels", "retry").String(); got != `labels != "retry"` {
		t.Errorf("NotIn with one value = %q", got)
	}
}

func TestEscapedSpaceInAString(t *testing.T) {
	tokens, err := jql.Tokenize(`summary ~ "a\ b"`)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if tokens[2].Text != "a b" {
		t.Errorf("escaped space resolved to %q", tokens[2].Text)
	}
}

func TestUnicodeEscapesResolve(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"\u00e9"`, "\u00e9"},
		{`"caf\u00e9"`, "caf\u00e9"},
		{`"\u0041\u0042"`, "AB"},
		{`"\u0009"`, "\t"},
		{`"a\u00e9b"`, "a\u00e9b"},
	}
	for _, tc := range cases {
		tokens, err := jql.Tokenize("summary ~ " + tc.in)
		if err != nil {
			t.Fatalf("Tokenize(%s): %v", tc.in, err)
		}
		if tokens[2].Text != tc.want {
			t.Errorf("%s resolved to %q, want %q", tc.in, tokens[2].Text, tc.want)
		}
	}
}

// TestRendererDefaultsSortDirection covers a Query built by hand rather than
// through the builder, which fills the direction in itself.
func TestRendererDefaultsSortDirection(t *testing.T) {
	got, err := jql.Render(&jql.Query{Order: []jql.Order{{Field: "created"}}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "ORDER BY created ASC" {
		t.Errorf("got %q, want an ascending default", got)
	}
}

func TestRendererRefusesNilClause(t *testing.T) {
	var nilClause *jql.Clause
	if _, err := jql.Render(&jql.Query{
		Where: &jql.And{Exprs: []jql.Expr{nilClause}},
	}); err == nil {
		t.Error("Render accepted a nil clause")
	}
}

func TestDateFunctionRejectsEmptyArgument(t *testing.T) {
	for _, in := range []string{"startOfWeek(,)", "endOfDay(-1,)", "endOfDay(,-1)"} {
		if _, err := jql.ParseDate(in); err == nil {
			t.Errorf("ParseDate(%q) accepted an empty argument", in)
		} else {
			assertUsageError(t, err, "INVALID_DATE")
		}
	}

	// Whitespace between the parentheses is an empty argument *list*, which is
	// a different thing and is fine.
	v, err := jql.ParseDate("startOfWeek( )")
	if err != nil {
		t.Fatalf("ParseDate(\"startOfWeek( )\"): %v", err)
	}
	got, err := jql.Render(&jql.Query{
		Where: &jql.Clause{Field: "created", Op: jql.OpGte, Value: v},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "created >= startOfWeek()" {
		t.Errorf("got %q", got)
	}
}

func TestKindStringNamesEveryTokenKind(t *testing.T) {
	kinds := []jql.Kind{
		jql.TokIdent, jql.TokString, jql.TokNumber,
		jql.TokOperator, jql.TokLParen, jql.TokRParen, jql.TokComma,
	}
	for _, k := range kinds {
		if s := k.String(); s == "" || s == "unknown" {
			t.Errorf("Kind(%d).String() = %q", k, s)
		}
	}
	if got := jql.Kind(99).String(); got != "unknown" {
		t.Errorf("an undefined kind names itself %q", got)
	}
}

func TestInspectPropagatesLexErrors(t *testing.T) {
	if _, err := jql.Fields(`summary ~ "unclosed`); err == nil {
		t.Error("Fields accepted unlexable JQL")
	}
	if _, err := jql.ReferencesField(`summary ~ "unclosed`, "project"); err == nil {
		t.Error("ReferencesField accepted unlexable JQL")
	}
}

// TestTrailingOperatorIsNotAField covers the field-position check at the end of
// a token stream, where there is no following token to inspect.
func TestTrailingOperatorIsNotAField(t *testing.T) {
	got, err := jql.Fields(`project`)
	if err != nil {
		t.Fatalf("Fields: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a bare identifier with no operator was read as a field: %v", got)
	}

	got, err = jql.Fields(`status IN (Done) AND`)
	if err != nil {
		t.Fatalf("Fields: %v", err)
	}
	if len(got) != 1 || got[0] != "status" {
		t.Errorf("Fields = %v, want [status]", got)
	}
}

func TestNumbersThatAreNotNumbers(t *testing.T) {
	tokens, err := jql.Tokenize(`a = 1e5 AND b = 1.2.3 AND c = -4`)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	byText := map[string]jql.Kind{}
	for _, tok := range tokens {
		byText[tok.Text] = tok.Kind
	}
	if byText["1e5"] != jql.TokNumber {
		t.Errorf("1e5 lexed as %s", byText["1e5"])
	}
	if byText["1.2.3"] != jql.TokIdent {
		t.Errorf("1.2.3 lexed as %s, want an identifier", byText["1.2.3"])
	}
	if byText["-4"] != jql.TokNumber {
		t.Errorf("-4 lexed as %s", byText["-4"])
	}
}

func TestFuncArgumentsAreQuotedByTheRenderer(t *testing.T) {
	// A function argument goes through the same escaping as any other value, so
	// a hostile membersOf() argument cannot close the call and start a new
	// clause.
	got, err := jql.Render(&jql.Query{
		Where: &jql.Clause{
			Field: "assignee", Op: jql.OpIn,
			Value: jql.List{&jql.Func{
				Name: "membersOf",
				Args: []jql.Value{jql.Text(`admins") OR project = OPS OR membersOf("x`)},
			}},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(got, `OR project = OPS`) && !strings.Contains(got, `\"`) {
		t.Fatalf("a function argument escaped its quotes: %s", got)
	}
	if !strings.HasPrefix(got, `assignee IN (membersOf("admins\") OR`) {
		t.Fatalf("unexpected rendering: %s", got)
	}
}
