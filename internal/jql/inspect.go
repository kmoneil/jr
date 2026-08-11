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
