package jql

import (
	"slices"
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
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
	if strings.TrimSpace(query) == "" {
		return errs.Usage("EMPTY_JQL", "JQL query is empty").
			WithRemedy("supply a query, or omit --jql entirely")
	}

	tokens, err := Tokenize(query)
	if err != nil {
		return err
	}

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
