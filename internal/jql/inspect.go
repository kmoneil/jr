package jql

import (
	"slices"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
)

// Fields returns every field name a query compares against, lowercased and
// deduplicated, in the order they first appear.
//
// It works on tokens, never on a regular expression. The incumbent's regex
// equivalent false-positives on `summary ~ "project = FOO"` and then silently
// drops the project scope it thought was already there.
func Fields(query string) ([]string, error) {
	tokens, err := Tokenize(query)
	if err != nil {
		return nil, err
	}

	var out []string
	for i, t := range tokens {
		if !isFieldPosition(tokens, i) {
			continue
		}
		name := strings.ToLower(t.Text)
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, nil
}

// ReferencesField reports whether the query constrains the named field.
func ReferencesField(query, field string) (bool, error) {
	fields, err := Fields(query)
	if err != nil {
		return false, err
	}
	return slices.Contains(fields, strings.ToLower(field)), nil
}

// isFieldPosition reports whether the token at i is a field name: an unquoted
// identifier, or a quoted string, that is immediately followed by an operator.
//
// A string literal only counts when it sits in the field position, which is
// what keeps the text inside `summary ~ "project = FOO"` from being read as a
// field reference — that string is a value, not a field.
func isFieldPosition(tokens []Token, i int) bool {
	switch tokens[i].Kind {
	case TokIdent, TokString:
	default:
		return false
	}
	// A word that is itself an operator or a connective is not a field.
	if tokens[i].Kind == TokIdent && isWordOperator(tokens[i].Text) {
		return false
	}
	if i+1 >= len(tokens) {
		return false
	}

	next := tokens[i+1]
	if next.Kind == TokOperator {
		return true
	}
	if next.Kind != TokIdent {
		return false
	}
	switch strings.ToUpper(next.Text) {
	case "IN", "IS", "WAS", "CHANGED", "NOT":
		return true
	}
	return false
}

// wordOperators are the operator and connective words that can appear where an
// identifier can, and must not be mistaken for field names.
var wordOperators = map[string]bool{
	"AND": true, "OR": true, "NOT": true, "IN": true, "IS": true,
	"WAS": true, "CHANGED": true, "BY": true, "FROM": true, "TO": true,
	"BEFORE": true, "AFTER": true, "ON": true, "DURING": true,
	"ORDER": true, "ASC": true, "DESC": true, "EMPTY": true, "NULL": true,
}

func isWordOperator(s string) bool { return wordOperators[strings.ToUpper(s)] }

// Validate performs the syntax checks that can be made without a server:
// the query lexes, its parentheses balance, and it is not empty.
//
// It is deliberately not a full parser. `jr jql validate` sends the query to
// Jira's parse endpoint, which is the only authority on whether a query is
// valid; this catches the errors worth catching before spending a round trip.
func Validate(query string) error {
	tokens, err := lexToValidate(query)
	if err != nil {
		return err
	}
	return balanced(query, tokens)
}

// lexToValidate is the opening both validators share: a query is not empty, and
// it lexes. Shared rather than repeated because ValidateFragment used to call
// Validate and then tokenize again, which left the second error path
// unreachable — and this package is gated at 100% statement coverage, so an
// unreachable branch is not a small untidiness, it is a failing build.
func lexToValidate(query string) ([]Token, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errs.Usage("EMPTY_JQL", "JQL query is empty").
			WithRemedy("supply a query, or omit --jql entirely")
	}
	return Tokenize(query)
}

// balanced reports whether every parenthesis has its partner, which is the one
// syntax error worth catching here: an unbalanced fragment escapes the wrapper
// the builder puts around it.
func balanced(query string, tokens []Token) error {
	depth := 0
	var openPositions []int
	for _, t := range tokens {
		switch t.Kind {
		case TokLParen:
			depth++
			openPositions = append(openPositions, t.Pos)
		case TokRParen:
			depth--
			if depth < 0 {
				return errs.Usage("JQL_SYNTAX",
					"Unbalanced closing parenthesis in JQL at position %d", t.Pos).
					WithDetail("%s", query).
					WithRemedy("remove it, or add the matching opening parenthesis")
			}
			openPositions = openPositions[:len(openPositions)-1]
		}
	}
	if depth > 0 {
		return errs.Usage("JQL_SYNTAX",
			"Unclosed parenthesis in JQL at position %d", openPositions[0]).
			WithDetail("%s", query).
			WithRemedy("close it, or remove it")
	}
	return nil
}

// ValidateFragment is Validate plus the rules that apply only to a query this
// tool is going to embed in a larger one.
//
// The distinction is the whole of this function. `Validate` answers "is this a
// query", which is what `jr jql validate` reports on and what a caller means
// when they ask whether their JQL is right. This answers "may this be a
// *fragment*", which is a narrower question with a different answer, and the
// two are not interchangeable: a query carrying its own ORDER BY is valid JQL
// and cannot be embedded.
func ValidateFragment(query string) error {
	tokens, err := lexToValidate(query)
	if err != nil {
		return err
	}
	if err := balanced(query, tokens); err != nil {
		return err
	}
	if pos, found := orderByAt(tokens); found {
		return errs.Usage("JQL_HAS_ORDER_BY",
			"the --jql fragment orders its own results, at position %d", pos).
			WithDetail("%s", query).
			WithRemedy("use --sort <field> with --order asc|desc; every query " +
				"this tool sends carries its own ORDER BY, and JQL does not " +
				"allow one inside the parentheses the fragment is wrapped in")
	}
	return nil
}

// orderByAt finds an ORDER BY clause and reports where it starts.
//
// Tokens, never a regular expression, for the reason the rest of this file
// works that way: `summary ~ "ORDER BY created"` lexes as one string and is a
// value, not a clause. A field genuinely called order is safe too — it is
// followed by an operator, and only the clause is followed by the word BY.
func orderByAt(tokens []Token) (int, bool) {
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].Kind != TokIdent || tokens[i+1].Kind != TokIdent {
			continue
		}
		if strings.EqualFold(tokens[i].Text, "ORDER") &&
			strings.EqualFold(tokens[i+1].Text, "BY") {
			return tokens[i].Pos, true
		}
	}
	return 0, false
}

// ProjectsSelected returns the project keys a query positively selects, in the
// order they first appear, uppercased and deduplicated.
//
// It is the question "which projects can this query return issues from", asked
// of a fragment that is about to be ANDed with a project scope. A fragment
// naming a project the scope excludes cannot match, and the caller says so
// rather than returning a complete, empty, exit-0 result.
//
// Three fields answer it, and they answer it differently:
//
//   - `project` carries a key directly: `project = ENG`, `project in (ENG, OPS)`.
//   - `key`, `issuekey` and `id` carry an issue key whose prefix is the project:
//     `key = ENG-1` selects ENG.
//   - `parent` is the same shape as `key` and is included for the same reason.
//
// Only *positive* selection counts. `project != ENG` and `project not in (...)`
// name a project without selecting it, so warning on them would fire on the
// invocation that is deliberately excluding something — a false positive on a
// correct query, which is worse here than staying quiet, because the caller of
// this is a warning nobody asked for.
//
// Tokens, never a regular expression, for the reason the rest of this file
// works that way: the text inside `summary ~ "project = FOO"` is a value, and
// isFieldPosition already knows that.
func ProjectsSelected(query string) ([]string, error) {
	tokens, err := Tokenize(query)
	if err != nil {
		return nil, err
	}

	var out []string
	add := func(key string) {
		key = strings.ToUpper(key)
		if key != "" && !slices.Contains(out, key) {
			out = append(out, key)
		}
	}

	for i, t := range tokens {
		if !isFieldPosition(tokens, i) {
			continue
		}
		field := strings.ToLower(t.Text)
		fromKey, ok := projectBearingFields[field]
		if !ok {
			continue
		}
		for _, v := range selectedValues(tokens, i+1) {
			if fromKey {
				add(projectOf(v))
				continue
			}
			add(v)
		}
	}
	return out, nil
}

// projectBearingFields maps a field to whether its value is an issue key that
// has to have its project prefix taken off, rather than a project key already.
//
// `id` is here because JQL accepts `id = ENG-1` as a synonym for `key`, and a
// numeric id names no project and is dropped by projectOf.
var projectBearingFields = map[string]bool{
	"project":  false,
	"key":      true,
	"issuekey": true,
	"id":       true,
	"parent":   true,
}

// selectedValues returns the literals a comparison starting at i selects, and
// nothing at all for a negated or non-selecting one.
//
// It reads from the operator forward: `= ENG`, `in (ENG, OPS)`. Anything else,
// including `!=`, `not in`, `~`, `is empty`, and `was`, selects no project this
// function will vouch for.
// The bounds check that is not here is deliberate. Every caller reaches this
// through isFieldPosition, which returns false unless i+1 exists, so the
// operator is always in range. A guard for it is an unreachable branch, and
// this package is gated at 100% statement coverage precisely so an unreachable
// branch is a failing build rather than a small untidiness — the same reasoning
// lexToValidate carries above.
func selectedValues(tokens []Token, i int) []string {
	switch t := tokens[i]; {
	case t.Kind == TokOperator && t.Text == string(OpEq):
		return literalAt(tokens, i+1)
	case t.Kind == TokIdent && strings.EqualFold(t.Text, "IN"):
		return literalList(tokens, i+1)
	default:
		return nil
	}
}

// literalAt returns the single literal at i, if there is one.
//
// A value that is not a bare word or a quoted string — a function call like
// `currentUser()`, a number, a parenthesis — names no project statically, and
// this returns nothing rather than guessing at one.
func literalAt(tokens []Token, i int) []string {
	if i >= len(tokens) {
		return nil
	}
	switch tokens[i].Kind {
	case TokIdent, TokString:
		return []string{tokens[i].Text}
	default:
		return nil
	}
}

// literalList returns the literals inside `(a, b, c)`, and nothing for a list
// this cannot read whole.
//
// A list that does not close, or that holds something other than literals and
// commas, yields nothing: a partial reading would report *some* of the projects
// a query selects, and a caller comparing that against a scope would warn about
// a query that also names the scope it is being checked against.
func literalList(tokens []Token, i int) []string {
	if i >= len(tokens) || tokens[i].Kind != TokLParen {
		return nil
	}
	var out []string
	for j := i + 1; j < len(tokens); j++ {
		switch tokens[j].Kind {
		case TokRParen:
			return out
		case TokComma:
			continue
		case TokIdent, TokString:
			out = append(out, tokens[j].Text)
		default:
			return nil
		}
	}
	return nil
}

// projectOf takes the project key off an issue key: ENG-1 is ENG.
//
// A string with no hyphen, or nothing before it, names no project. So does a
// bare number, which is a legal `id` and identifies an issue without saying
// where it lives.
func projectOf(issueKey string) string {
	prefix, _, found := strings.Cut(issueKey, "-")
	if !found {
		return ""
	}
	return prefix
}
