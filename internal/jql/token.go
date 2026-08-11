package jql

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/errs"
)

// Kind classifies a token.
type Kind int

// The token kinds. Word operators such as IN, IS, and WAS tokenize as
// TokIdent; the caller, not the lexer, decides what a word means.
const (
	TokIdent Kind = iota
	TokString
	TokNumber
	TokOperator
	TokLParen
	TokRParen
	TokComma
)

var kindNames = map[Kind]string{
	TokIdent:    "identifier",
	TokString:   "string",
	TokNumber:   "number",
	TokOperator: "operator",
	TokLParen:   "(",
	TokRParen:   ")",
	TokComma:    ",",
}

// String implements fmt.Stringer.
func (k Kind) String() string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	return "unknown"
}

// Token is one lexical unit of a JQL query.
//
// Text holds the token's *value*, not its source spelling: a String token
// carries the unescaped content, so a caller comparing values never has to
// think about quoting. Pos is the 1-based rune position where the token starts.
type Token struct {
	Kind Kind
	Text string
	Pos  int
	// Quote is the quote character a String token was written with, or 0.
	Quote rune
}

// operatorRunes are the characters that can begin or continue a symbolic
// operator. A bare word ends when it meets one.
const operatorRunes = "=!<>~"

// Tokenize splits JQL into tokens.
//
// This is the only way this project inspects a query. A regular expression
// cannot tell a field reference from the same text inside a string literal —
// `summary ~ "project = FOO"` does not constrain the project — and a tool that
// guesses wrong about that silently returns the wrong issues.
func Tokenize(query string) ([]Token, error) {
	// Converting to runes would turn an invalid byte into U+FFFD, so a query
	// containing one would lex as something the caller never wrote. Refuse it
	// rather than silently analysing a different query.
	if !utf8.ValidString(query) {
		return nil, errs.Usage("INVALID_ENCODING", "JQL is not valid UTF-8").
			WithDetail("%q", query).
			WithRemedy("re-encode the query as UTF-8")
	}

	src := []rune(query)
	var out []Token

	for i := 0; i < len(src); {
		r := src[i]

		switch {
		case unicode.IsSpace(r):
			i++

		case r == '(':
			out = append(out, Token{Kind: TokLParen, Text: "(", Pos: i + 1})
			i++

		case r == ')':
			out = append(out, Token{Kind: TokRParen, Text: ")", Pos: i + 1})
			i++

		case r == ',':
			out = append(out, Token{Kind: TokComma, Text: ",", Pos: i + 1})
			i++

		case r == '"' || r == '\'':
			tok, next, err := lexString(src, i)
			if err != nil {
				return nil, err
			}
			out, i = append(out, tok), next

		case strings.ContainsRune(operatorRunes, r):
			tok, next, err := lexOperator(src, i)
			if err != nil {
				return nil, err
			}
			out, i = append(out, tok), next

		default:
			tok, next := lexBareWord(src, i)
			out, i = append(out, tok), next
		}
	}
	return out, nil
}

// lexString reads a quoted literal and returns its unescaped content.
func lexString(src []rune, start int) (Token, int, error) {
	quote := src[start]
	var b strings.Builder

	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case quote:
			return Token{
				Kind:  TokString,
				Text:  b.String(),
				Pos:   start + 1,
				Quote: quote,
			}, i + 1, nil

		case '\\':
			if i+1 >= len(src) {
				return Token{}, 0, errs.Usage("JQL_SYNTAX",
					"Trailing backslash in JQL at position %d", i+1).
					WithDetail("%s", string(src)).
					WithRemedy(`escape a literal backslash as \\`)
			}
			i++
			r, width, err := unescape(src, i)
			if err != nil {
				return Token{}, 0, err
			}
			b.WriteRune(r)
			i += width - 1

		default:
			b.WriteRune(src[i])
		}
	}

	return Token{}, 0, errs.Usage("JQL_SYNTAX",
		"Unclosed quote in JQL at position %d", start+1).
		WithDetail("%s", string(src)).
		WithRemedy("close the quote, or escape it as %s if it is part of the value",
			`\`+string(quote))
}

// unescape resolves the escape sequence beginning at i (the character after the
// backslash) and reports how many runes it consumed.
func unescape(src []rune, i int) (r rune, width int, err error) {
	switch src[i] {
	case 'n':
		return '\n', 1, nil
	case 'r':
		return '\r', 1, nil
	case 't':
		return '\t', 1, nil
	case '\\', '"', '\'', ' ':
		return src[i], 1, nil
	case 'u':
		if i+4 >= len(src) {
			return 0, 0, errs.Usage("JQL_SYNTAX",
				"Truncated \\u escape in JQL at position %d", i).
				WithDetail("%s", string(src)).
				WithRemedy(`a unicode escape needs four hex digits, e.g. é`)
		}
		// Four digits, so 16 bits: the width is the slice above, and saying so
		// here keeps the bound in the code rather than in arithmetic a reader
		// has to redo.
		code, convErr := strconv.ParseUint(string(src[i+1:i+5]), 16, 16)
		if convErr != nil {
			return 0, 0, errs.Usage("JQL_SYNTAX",
				"Invalid \\u escape in JQL at position %d", i).
				WithDetail("%s", string(src)).
				WithRemedy(`a unicode escape needs four hex digits, e.g. é`)
		}
		// A surrogate half is not a character. rune(0xD800) is a legal rune
		// value and an illegal scalar, so it survives this conversion and dies
		// at the first WriteRune, which substitutes U+FFFD without saying so —
		// the silent corruption quote() forty lines away exists to prevent, and
		// which quote() cannot catch because U+FFFD is valid UTF-8 by then.
		if code >= 0xD800 && code <= 0xDFFF {
			return 0, 0, errs.Usage("JQL_SYNTAX",
				"Surrogate \\u escape in JQL at position %d", i).
				WithDetail("%s", string(src)).
				WithRemedy("U+D800 to U+DFFF are half of a pair and name no " +
					"character; write the character itself")
		}
		return rune(code), 5, nil
	default:
		return 0, 0, errs.Usage("JQL_SYNTAX",
			"Unknown escape sequence \\%c in JQL at position %d", src[i], i).
			WithDetail("%s", string(src)).
			WithRemedy(`valid escapes are \n \r \t \\ \" \' and \uXXXX`)
	}
}

// lexOperator reads a symbolic operator. Two-rune operators are matched before
// one-rune ones, so `!=` never lexes as `!` followed by `=`.
func lexOperator(src []rune, start int) (Token, int, error) {
	if start+1 < len(src) {
		two := string(src[start : start+2])
		switch two {
		case "!=", "<=", ">=", "!~":
			return Token{Kind: TokOperator, Text: two, Pos: start + 1}, start + 2, nil
		}
	}
	one := string(src[start])
	switch one {
	case "=", "<", ">", "~":
		return Token{Kind: TokOperator, Text: one, Pos: start + 1}, start + 1, nil
	}
	return Token{}, 0, errs.Usage("JQL_SYNTAX",
		"Unexpected %q in JQL at position %d", one, start+1).
		WithDetail("%s", string(src)).
		WithRemedy("valid operators are = != > >= < <= ~ !~ and the words IN, IS, WAS, CHANGED")
}

// lexBareWord reads an unquoted word: a field name, a keyword, a word operator,
// a function name, or an unquoted value.
func lexBareWord(src []rune, start int) (Token, int) {
	i := start
	for i < len(src) {
		r := src[i]
		if unicode.IsSpace(r) || r == '(' || r == ')' || r == ',' ||
			r == '"' || r == '\'' || strings.ContainsRune(operatorRunes, r) {
			break
		}
		i++
	}
	text := string(src[start:i])
	kind := TokIdent
	if isNumeric(text) {
		kind = TokNumber
	}
	return Token{Kind: kind, Text: text, Pos: start + 1}, i
}

// isNumeric reports whether s is a number. The empty string is not: ParseFloat
// rejects it, so no separate guard is needed.
func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}
