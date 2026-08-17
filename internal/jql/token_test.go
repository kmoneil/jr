package jql_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/jql"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		kinds []jql.Kind
		texts []string
	}{
		{
			"simple clause",
			`project = ENG`,
			[]jql.Kind{jql.TokIdent, jql.TokOperator, jql.TokIdent},
			[]string{"project", "=", "ENG"},
		},
		{
			"quoted value keeps its content, not its quotes",
			`summary ~ "a b"`,
			[]jql.Kind{jql.TokIdent, jql.TokOperator, jql.TokString},
			[]string{"summary", "~", "a b"},
		},
		{
			"single quotes",
			`summary ~ 'a b'`,
			[]jql.Kind{jql.TokIdent, jql.TokOperator, jql.TokString},
			[]string{"summary", "~", "a b"},
		},
		{
			"two-rune operators are not split",
			`a != b AND c !~ d AND e >= f AND g <= h`,
			[]jql.Kind{
				jql.TokIdent, jql.TokOperator, jql.TokIdent, jql.TokIdent,
				jql.TokIdent, jql.TokOperator, jql.TokIdent, jql.TokIdent,
				jql.TokIdent, jql.TokOperator, jql.TokIdent, jql.TokIdent,
				jql.TokIdent, jql.TokOperator, jql.TokIdent,
			},
			[]string{
				"a", "!=", "b", "AND", "c", "!~", "d", "AND",
				"e", ">=", "f", "AND", "g", "<=", "h",
			},
		},
		{
			"lists",
			`status IN (Done, "In Progress")`,
			[]jql.Kind{
				jql.TokIdent, jql.TokIdent, jql.TokLParen, jql.TokIdent,
				jql.TokComma, jql.TokString, jql.TokRParen,
			},
			[]string{"status", "IN", "(", "Done", ",", "In Progress", ")"},
		},
		{
			"numbers",
			`votes > 3.5`,
			[]jql.Kind{jql.TokIdent, jql.TokOperator, jql.TokNumber},
			[]string{"votes", ">", "3.5"},
		},
		{
			"escapes are resolved",
			`summary ~ "a\"b\\c\nd"`,
			[]jql.Kind{jql.TokIdent, jql.TokOperator, jql.TokString},
			[]string{"summary", "~", "a\"b\\c\nd"},
		},
		{
			"unicode escape",
			`summary ~ "café"`,
			[]jql.Kind{jql.TokIdent, jql.TokOperator, jql.TokString},
			[]string{"summary", "~", "café"},
		},
		{
			"function call",
			`assignee = currentUser()`,
			[]jql.Kind{jql.TokIdent, jql.TokOperator, jql.TokIdent, jql.TokLParen, jql.TokRParen},
			[]string{"assignee", "=", "currentUser", "(", ")"},
		},
		{
			"no spaces around operators",
			`project=ENG`,
			[]jql.Kind{jql.TokIdent, jql.TokOperator, jql.TokIdent},
			[]string{"project", "=", "ENG"},
		},
		{
			"empty query",
			``,
			nil,
			nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokens, err := jql.Tokenize(tc.in)
			if err != nil {
				t.Fatalf("Tokenize(%q): %v", tc.in, err)
			}
			if len(tokens) != len(tc.kinds) {
				t.Fatalf("got %d tokens, want %d: %+v", len(tokens), len(tc.kinds), tokens)
			}
			for i, tok := range tokens {
				if tok.Kind != tc.kinds[i] {
					t.Errorf("token %d is %s, want %s", i, tok.Kind, tc.kinds[i])
				}
				if tok.Text != tc.texts[i] {
					t.Errorf("token %d text = %q, want %q", i, tok.Text, tc.texts[i])
				}
			}
		})
	}
}

func TestTokenizeReportsPositions(t *testing.T) {
	tokens, err := jql.Tokenize(`project = ENG`)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	want := []int{1, 9, 11}
	for i, tok := range tokens {
		if tok.Pos != want[i] {
			t.Errorf("token %d at position %d, want %d", i, tok.Pos, want[i])
		}
	}
}

func TestTokenizeErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"unclosed double quote", `project = ENG AND summary ~ "unclosed`, "Unclosed quote"},
		{"unclosed single quote", `summary ~ 'unclosed`, "Unclosed quote"},
		{"trailing backslash", `summary ~ "a\`, "Trailing backslash"},
		{"unknown escape", `summary ~ "a\qb"`, "Unknown escape"},
		{"truncated unicode escape", `summary ~ "a\u00"`, "Truncated"},
		{"invalid unicode escape", `summary ~ "a\uzzzz"`, "Invalid"},
		// A surrogate half is a legal rune value and an illegal scalar: it
		// survives rune(code) and is replaced by U+FFFD at the first WriteRune,
		// which is the substitution this package exists to refuse.
		{"high surrogate escape", `summary ~ "a\uD800b"`, "Surrogate"},
		{"low surrogate escape", `summary ~ "a\uDC00b"`, "Surrogate"},
		{"last surrogate escape", `summary ~ "a\uDFFFb"`, "Surrogate"},
		{"bare bang", `project ! ENG`, "Unexpected"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := jql.Tokenize(tc.in)
			assertUsageError(t, err, "JQL_SYNTAX")
			if err == nil {
				return
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// TestUnclosedQuoteNamesItsPosition reproduces the error the spec uses as its
// worked example.
func TestUnclosedQuoteNamesItsPosition(t *testing.T) {
	_, err := jql.Tokenize(`project = ENG AND summary ~ "unclosed`)
	if err == nil {
		t.Fatal("an unclosed quote was accepted")
	}
	if !strings.Contains(err.Error(), "position 29") {
		t.Errorf("error does not name the position of the opening quote: %s", err.Error())
	}
}

// TestInvalidUTF8IsRefusedNotSubstituted asserts a query carrying a byte that
// is not valid UTF-8 fails instead of being silently rewritten. Ranging over
// such a string yields U+FFFD, which would mean analysing — and sending — a
// different query than the caller wrote.
func TestInvalidUTF8IsRefusedNotSubstituted(t *testing.T) {
	for _, q := range []string{"\xc4", "project = \xff", "summary ~ \"a\xc4b\""} {
		_, err := jql.Tokenize(q)
		assertUsageError(t, err, "INVALID_ENCODING")
	}

	got, err := jql.New().Eq("summary", "a\xc4b").Render()
	if err == nil {
		t.Fatalf("an invalid byte was rendered into a query: %s", got)
	}
	assertUsageError(t, err, "INVALID_ENCODING")

	if _, err := jql.New().Raw("project = \xc4").Render(); err == nil {
		t.Fatal("raw JQL with an invalid byte was accepted")
	}
}

// TestFieldsIgnoresStringContents is why this package tokenizes instead of
// matching a regex. The incumbent's hasProjectFilter regex reports true for the
// second query here and then drops the project scope it thinks is present.
func TestFieldsIgnoresStringContents(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", `project = ENG`, []string{"project"}},
		{"conjunction", `project = ENG AND status = Done`, []string{"project", "status"}},
		{
			"a field mentioned inside a string is not a field",
			`summary ~ "project = FOO"`,
			[]string{"summary"},
		},
		{
			"nor is one inside a value list",
			`labels IN ("project = FOO", "b")`,
			[]string{"labels"},
		},
		{"IN", `status IN (Done, Closed)`, []string{"status"}},
		{"IS", `assignee IS EMPTY`, []string{"assignee"}},
		{"WAS", `status WAS Done`, []string{"status"}},
		{"CHANGED", `status CHANGED`, []string{"status"}},
		{"quoted field name", `"Story Points" > 3`, []string{"story points"}},
		{"duplicates collapse", `project = ENG OR project = OPS`, []string{"project"}},
		{"parenthesized", `(project = ENG) AND (status = Done)`, []string{"project", "status"}},
		{"order by is not a field", `project = ENG ORDER BY created DESC`, []string{"project"}},
		{"empty", ``, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := jql.Fields(tc.in)
			if err != nil {
				t.Fatalf("Fields(%q): %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Fields(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("Fields(%q) = %v, want %v", tc.in, got, tc.want)
					break
				}
			}
		})
	}
}

func TestReferencesField(t *testing.T) {
	cases := []struct {
		query string
		field string
		want  bool
	}{
		{`project = ENG`, "project", true},
		{`project = ENG`, "PROJECT", true},
		{`project = ENG`, "status", false},
		{`summary ~ "project = FOO"`, "project", false},
		{`summary ~ "project = FOO" AND project = ENG`, "project", true},
	}
	for _, tc := range cases {
		got, err := jql.ReferencesField(tc.query, tc.field)
		if err != nil {
			t.Fatalf("ReferencesField(%q, %q): %v", tc.query, tc.field, err)
		}
		if got != tc.want {
			t.Errorf("ReferencesField(%q, %q) = %v, want %v", tc.query, tc.field, got, tc.want)
		}
	}
}

func TestValidate(t *testing.T) {
	valid := []string{
		`project = ENG`,
		`project = ENG AND (status = Done OR status = Closed)`,
		`assignee = currentUser()`,
		`status IN (Done, "In Progress")`,
		`summary ~ "a (b) c"`,
	}
	for _, q := range valid {
		if err := jql.Validate(q); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", q, err)
		}
	}

	invalid := []struct{ query, code string }{
		{``, "EMPTY_JQL"},
		{`   `, "EMPTY_JQL"},
		{`project = ENG AND (status = Done`, "JQL_SYNTAX"},
		{`project = ENG) AND status = Done`, "JQL_SYNTAX"},
		{`summary ~ "unclosed`, "JQL_SYNTAX"},
	}
	for _, tc := range invalid {
		assertUsageError(t, jql.Validate(tc.query), tc.code)
	}
}

// TestValidateIgnoresParensInsideStrings is the same lesson as Fields: a
// parenthesis inside a quoted value does not affect balance.
func TestValidateIgnoresParensInsideStrings(t *testing.T) {
	if err := jql.Validate(`summary ~ "unbalanced ("`); err != nil {
		t.Errorf("a parenthesis inside a string was counted: %v", err)
	}
	if err := jql.Validate(`summary ~ "unbalanced )"`); err != nil {
		t.Errorf("a parenthesis inside a string was counted: %v", err)
	}
}

// FuzzTokenizeNeverSubstitutesACharacter is the property the package was
// missing, and its absence is why a surrogate escape went unnoticed through
// 100% statement coverage and two green fuzzers.
//
// FuzzValuesRoundTrip does compare values, but it starts from a value and
// renders it — and quote() never emits a \uXXXX escape, so the round trip could
// not produce the input that breaks. FuzzTokenizeDoesNotPanic does start from a
// query and reaches the escape, but asserts only that nothing crashes. A quiet
// substitution does not crash. Between the two there was a hole exactly the
// shape of \u.
//
// U+FFFD is the marker every lossy conversion in Go leaves behind, so the
// property is simply that the lexer never invents one. Anything that did was a
// value silently changed into a different value, which is the one outcome this
// package exists to prevent.
func FuzzTokenizeNeverSubstitutesACharacter(f *testing.F) {
	for _, seed := range []string{
		`summary ~ "a\uD800b"`, `summary ~ "\uDFFF"`, `summary ~ "�"`,
		`summary ~ "café"`, `summary ~ "aAb"`, "summary ~ \"�\"",
		// Found by this fuzzer on its first run, against a property that was
		// too strong rather than against the lexer: a caller who writes �
		// is asking for U+FFFD, and producing it is faithful. Seeded so the
		// distinction stays visible in the source.
		"\"\ufffd\"", "\"\\uFFFD\"", "\"\\ufffd\"",
		"summary ~ \"a\\uFFFDb\"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, query string) {
		tokens, err := jql.Tokenize(query)
		if err != nil {
			return
		}
		// Asked for, in either spelling, is not substituted. The escape is
		// hex and case-insensitive, so the lowered form is what to look for.
		if strings.ContainsRune(query, '\ufffd') ||
			strings.Contains(strings.ToLower(query), "\\ufffd") {
			return
		}
		for _, tok := range tokens {
			if strings.ContainsRune(tok.Text, '�') {
				t.Fatalf("Tokenize(%q) put U+FFFD in token %+v; the input had "+
					"none, so a character was silently replaced", query, tok)
			}
		}
	})
}

// FuzzTokenizeDoesNotPanic asserts the lexer either returns tokens or a
// structured error for any input, and never panics on a slice bound.
func FuzzTokenizeDoesNotPanic(f *testing.F) {
	for _, seed := range []string{
		``, `project = ENG`, `"`, `'`, `\`, `"\u`, `"\uzzzz`, `((((`, `))))`,
		`!`, `!=`, `a!=b`, "\x00", "é", `"a\`,
		// The surrogate escapes, seeded here too so the crasher-shaped input is
		// visible in the source rather than only in a corpus file.
		`"\uD800"`, `"\uDBFF"`, `"\uDC00"`, `"\uDFFF"`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, query string) {
		tokens, err := jql.Tokenize(query)
		if err != nil {
			// Every failure is a structured one a caller can act on.
			e := errs.Coerce(err)
			if e.Code != "JQL_SYNTAX" && e.Code != "INVALID_ENCODING" {
				t.Fatalf("Tokenize(%q) failed with unexpected code %q: %v", query, e.Code, err)
			}
			if e.Exit != exitcode.Usage {
				t.Fatalf("%s exits %v, want %v", e.Code, e.Exit, exitcode.Usage)
			}
			return
		}
		// Every token must point somewhere inside the input.
		for _, tok := range tokens {
			if tok.Pos < 1 || tok.Pos > len([]rune(query)) {
				t.Fatalf("token %+v has a position outside %q", tok, query)
			}
		}
	})
}

// TestValidateFragmentRefusesAnOrderBy covers the rule that applies only to a
// query this tool is going to embed in a larger one.
//
// `jr issue list --jql 'project = ENG ORDER BY created ASC'` built
// `(project = ENG ORDER BY created ASC) ORDER BY issuekey DESC`, which JQL does
// not allow — an ORDER BY cannot appear inside parentheses — so Jira answered
// with a syntax error naming a character position in a query the caller never
// wrote. Both rules that produce it are right: the fragment is parenthesized so
// an OR inside it cannot widen the scope, and every query carries an ORDER BY
// so the ordering never depends on the server's undocumented default. What was
// missing was a refusal.
func TestValidateFragmentRefusesAnOrderBy(t *testing.T) {
	refused := []string{
		`project = ENG ORDER BY created ASC`,
		`project = ENG order by created`,
		`project = ENG ORDER BY created`,
		// Inside a group, which is where it would break even without wrapping.
		`(project = ENG ORDER BY created)`,
	}
	for _, q := range refused {
		err := jql.ValidateFragment(q)
		if err == nil {
			t.Errorf("%q was accepted as a fragment", q)
			continue
		}
		if code := errs.Coerce(err).Code; code != "JQL_HAS_ORDER_BY" {
			t.Errorf("%q: code = %q, want JQL_HAS_ORDER_BY", q, code)
		}
	}
}

// TestValidateFragmentReadsTokensNotText is why the check tokenizes.
//
// A regular expression over the query text would refuse the first two of these,
// and the first is an ordinary free-text search. The lexer settles it: the
// words are inside a string, so they are a value.
func TestValidateFragmentReadsTokensNotText(t *testing.T) {
	accepted := []string{
		`summary ~ "ORDER BY created"`,
		`summary ~ "order by"`,
		// A field genuinely called order. It is followed by an operator, and
		// only the clause is followed by the word BY.
		`order = 5`,
		`"Order" = 5`,
		// The word alone, which is not a clause.
		`summary ~ order`,
		`project = ENG`,
	}
	for _, q := range accepted {
		if err := jql.ValidateFragment(q); err != nil {
			t.Errorf("%q was refused: %v", q, err)
		}
	}
}

// TestValidateFragmentInheritsValidate keeps the two from drifting apart. A
// fragment has to pass everything a query passes, and then some.
func TestValidateFragmentInheritsValidate(t *testing.T) {
	for _, q := range []string{`a) OR (1=1`, `(project = ENG`, `   `, ``} {
		if err := jql.ValidateFragment(q); err == nil {
			t.Errorf("%q passed as a fragment and Validate refuses it", q)
		}
	}
	// And a query carrying its own ORDER BY is *valid*: `jr jql validate`
	// reports on it and must not be made to call it malformed, which is the
	// whole reason this is a second function rather than a rule inside the
	// first.
	if err := jql.Validate(`project = ENG ORDER BY created ASC`); err != nil {
		t.Errorf("Validate refused a valid query: %v", err)
	}
}

// The assertions below were written from the mutation sweep of 2026-08-16,
// which found sixteen surviving mutants in a package held at 100% statement
// coverage with four fuzz targets. Every line ran; for sixteen changes to those
// lines, nothing noticed. Each test here names the decision it pins rather than
// the mutant, because the mutant is the evidence and the decision is the point.

// TestEveryKindOfTokenReportsItsOwnPosition covers what
// TestTokenizeReportsPositions does not.
//
// That one tokenizes `project = ENG`, so it exercises the position arithmetic
// on an identifier and an operator. A comma, a string, and a parenthesis each
// compute their own, and turning `i + 1` into `i - 1` on the comma survived
// every test in this package. A position is what a refusal points a caller at
// in their own query, so being off by two there is a caller counting characters
// to a place nothing is wrong.
//
// Positions are 1-based, and a string's is its opening quote.
func TestEveryKindOfTokenReportsItsOwnPosition(t *testing.T) {
	const query = `status IN ("Done", 'In Progress')`

	tokens, err := jql.Tokenize(query)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}

	want := []struct {
		text string
		pos  int
	}{
		{"status", 1},
		{"IN", 8},
		{"(", 11},
		{"Done", 12},
		{",", 18},
		{"In Progress", 20},
		{")", 33},
	}
	if len(tokens) != len(want) {
		t.Fatalf("got %d tokens, want %d: %+v", len(tokens), len(want), tokens)
	}
	for i, w := range want {
		if tokens[i].Text != w.text || tokens[i].Pos != w.pos {
			t.Errorf("token %d is %q at %d, want %q at %d",
				i, tokens[i].Text, tokens[i].Pos, w.text, w.pos)
		}
	}
}

// TestASyntaxErrorNamesThePositionItFoundIt is the same rule for the two
// refusals the tokenizer raises itself. Both build their position by hand, and
// both survived a mutation that moved it.
func TestASyntaxErrorNamesThePositionItFoundIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"trailing backslash", `x ~ "ab\`, "position 8"},
		{"an operator that is not one", `a ! b`, "position 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := jql.Tokenize(tc.query)
			if err == nil {
				t.Fatalf("%q was accepted", tc.query)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not say %q: %s", tc.want, err.Error())
			}
		})
	}
}

// TestAnOperatorAtTheEndOfTheQueryDoesNotOverrun pins the bounds check that
// lets `!=` be matched before `!`.
//
// It reads two runes when there are two to read, and the query ending in a
// single-rune operator is the case where there are not. Both mutations of that
// check index past the end of the input, so this is a panic rather than a wrong
// answer, and nothing in the package ended a query on an operator.
func TestAnOperatorAtTheEndOfTheQueryDoesNotOverrun(t *testing.T) {
	tokens, err := jql.Tokenize("status =")
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens, want 2: %+v", len(tokens), tokens)
	}
	if tokens[1].Text != "=" || tokens[1].Pos != 8 {
		t.Errorf("final token is %q at %d, want %q at 8", tokens[1].Text, tokens[1].Pos, "=")
	}
}

// TestAWordOperatorIsNeverAField is what `Fields` and `ReferencesField` are
// for, and the guard that makes it true had nothing asserting it.
//
// A word operator sitting where a field could be read is the case: in
// `status WAS IN (Done)` the token after `WAS` is `IN`, which is exactly the
// shape that makes the token before it a field. Without the guard, `was` is
// reported as a field the query constrains, and `--jql` scope checks read that
// list to decide whether a query is already bounded.
func TestAWordOperatorIsNeverAField(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"status WAS IN (Done)", []string{"status"}},
		{"status IS NOT EMPTY", []string{"status"}},
		{"assignee WAS NOT ada", []string{"assignee"}},
		{"status = Done AND priority = High", []string{"status", "priority"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got, err := jql.Fields(tc.query)
			if err != nil {
				t.Fatalf("Fields: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Fields(%q) = %v, want %v", tc.query, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("Fields(%q) = %v, want %v", tc.query, got, tc.want)
				}
			}
		})
	}
}

// TestAnUnclosedParenthesisNamesTheOneStillOpen pins which parenthesis the
// refusal points at.
//
// The balance check keeps a stack of open positions and pops it on every close,
// and the position it reports is the first entry left on it. A pop that removes
// the wrong element, or none, reports a parenthesis that was closed several
// characters ago, and both mutations of that pop survived: nothing here had two
// parenthesised groups where only the second was open.
func TestAnUnclosedParenthesisNamesTheOneStillOpen(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  string
	}{
		{"(a) AND (b", "position 9"},
		{"((a)", "position 1"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			err := jql.Validate(tc.query)
			if err == nil {
				t.Fatalf("%q was accepted", tc.query)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not say %q: %s", tc.want, err.Error())
			}
		})
	}
}
