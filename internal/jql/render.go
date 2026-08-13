package jql

import (
	"strings"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/errs"
)

// Render serializes a query to JQL.
//
// This function and the helpers below are the only place in this project where
// a string is quoted. Nothing else concatenates JQL, so injection has exactly
// one place it could live and exactly one set of tests guarding it.
func Render(q *Query) (string, error) {
	if q == nil {
		return "", errs.Runtime("NIL_QUERY", "cannot render a nil query")
	}

	var b strings.Builder
	if q.Where != nil {
		where, err := renderExpr(q.Where, false)
		if err != nil {
			return "", err
		}
		b.WriteString(where)
	}

	if len(q.Order) > 0 {
		terms, err := renderOrder(q.Order)
		if err != nil {
			return "", err
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("ORDER BY ")
		b.WriteString(terms)
	}

	return b.String(), nil
}

// renderOrder serializes the sort terms. An empty direction means ascending;
// anything that is neither ascending nor descending is refused rather than
// passed to the server to reject opaquely.
func renderOrder(order []Order) (string, error) {
	terms := make([]string, 0, len(order))
	for _, o := range order {
		dir := o.Direction
		if dir == "" {
			dir = Asc
		}
		if dir != Asc && dir != Desc {
			return "", errs.Usage("INVALID_ORDER",
				"unknown sort direction %q", string(dir)).
				WithRemedy("use --order asc or --order desc")
		}
		field, err := renderField(o.Field)
		if err != nil {
			return "", err
		}
		terms = append(terms, field+" "+string(dir))
	}
	return strings.Join(terms, ", "), nil
}

// renderExpr serializes an expression. nested is true when the result will sit
// inside another boolean expression, which is when grouping matters.
func renderExpr(e Expr, nested bool) (string, error) {
	switch x := e.(type) {
	case *Clause:
		return renderClause(x)

	case *And:
		return renderBool(x.Exprs, "AND", nested)

	case *Or:
		return renderBool(x.Exprs, "OR", nested)

	case *Not:
		if x.Expr == nil {
			return "", errs.Runtime("INVALID_EXPR", "NOT has no operand")
		}
		inner, err := renderExpr(x.Expr, true)
		if err != nil {
			return "", err
		}
		if inner == "" {
			return "", nil
		}
		return "NOT (" + inner + ")", nil

	case *Raw:
		text := strings.TrimSpace(x.Text)
		if text == "" {
			return "", nil
		}
		if !utf8.ValidString(text) {
			return "", errs.Usage("INVALID_ENCODING",
				"raw JQL is not valid UTF-8").
				WithDetail("%q", text).
				WithRemedy("re-encode the query as UTF-8")
		}
		// Always parenthesized, unconditionally. Deciding case by case whether
		// a fragment "needs" grouping means tokenizing and reasoning about
		// precedence, and being wrong once means a user's OR escapes the
		// project scope.
		return "(" + text + ")", nil

	case nil:
		return "", nil

	default:
		return "", errs.Runtime("INVALID_EXPR", "unknown expression type %T", e)
	}
}

func renderBool(exprs []Expr, op string, nested bool) (string, error) {
	parts := make([]string, 0, len(exprs))
	for _, e := range exprs {
		s, err := renderExpr(e, true)
		if err != nil {
			return "", err
		}
		if s != "" {
			parts = append(parts, s)
		}
	}

	switch len(parts) {
	case 0:
		return "", nil
	case 1:
		// A single operand needs no operator and no grouping of its own; it
		// already carries whatever grouping it required.
		return parts[0], nil
	}

	joined := strings.Join(parts, " "+op+" ")
	if nested {
		return "(" + joined + ")", nil
	}
	return joined, nil
}

func renderClause(c *Clause) (string, error) {
	if c == nil {
		return "", errs.Runtime("INVALID_EXPR", "nil clause")
	}
	field, err := renderField(c.Field)
	if err != nil {
		return "", err
	}
	if c.Op == "" {
		return "", errs.Runtime("INVALID_EXPR", "clause on %s has no operator", field)
	}

	head, err := renderClauseHead(c, field)
	if err != nil {
		return "", err
	}
	preds, err := renderPredicates(c, field)
	if err != nil {
		return "", err
	}
	if preds == "" {
		return head, nil
	}
	return head + " " + preds, nil
}

// renderClauseHead serializes everything up to the predicates.
func renderClauseHead(c *Clause, field string) (string, error) {
	if c.Op == OpChanged {
		// CHANGED has no right-hand side at all. It used to accept one and
		// render it through renderValue, which produced `status CHANGED
		// "BY currentUser()"` for a Text and `status CHANGED after("-7d")` for
		// a Func — a quoted string and a function call, where JQL wants a bare
		// keyword. Both parse here and are rejected by Jira, so the only way to
		// write a working CHANGED was Raw, the path reserved for fragments the
		// caller typed. Predicates are the right-hand side now, and a value is
		// refused rather than silently mis-rendered.
		if c.Value != nil {
			return "", errs.Runtime("INVALID_EXPR",
				"%s CHANGED takes no value; qualify it with a predicate such as BY or AFTER",
				field)
		}
		return field + " CHANGED", nil
	}

	if err := checkListShape(c, field); err != nil {
		return "", err
	}
	value, err := renderValue(c.Value)
	if err != nil {
		return "", err
	}
	return field + " " + string(c.Op) + " " + value, nil
}

// checkListShape holds an operator and its right-hand side to the same idea of
// how many values there are.
func checkListShape(c *Clause, field string) error {
	list, isList := c.Value.(List)
	switch {
	case c.Op.takesList() && !isList:
		return errs.Runtime("INVALID_EXPR",
			"operator %s on %s needs a list of values", c.Op, field)
	case !c.Op.takesList() && isList:
		return errs.Runtime("INVALID_EXPR",
			"operator %s on %s takes a single value, not a list", c.Op, field)
	case isList && len(list) == 0:
		// `field IN ()` is not valid JQL and, worse, an empty IN reads as "no
		// filter" to a human skimming the query. Refuse it.
		return errs.Usage("EMPTY_VALUE_LIST",
			"%s %s was given no values", field, c.Op).
			WithRemedy("omit the filter entirely, or supply at least one value")
	}
	return nil
}

// renderPredicates serializes the history qualifiers on a clause.
func renderPredicates(c *Clause, field string) (string, error) {
	if len(c.Predicates) == 0 {
		return "", nil
	}
	if !c.Op.takesPredicates() {
		return "", errs.Runtime("INVALID_EXPR",
			"operator %s on %s takes no history predicates", c.Op, field)
	}

	seen := make(map[PredicateKeyword]bool, len(c.Predicates))
	parts := make([]string, 0, len(c.Predicates))
	for _, p := range c.Predicates {
		s, err := renderPredicate(c.Op, p, seen)
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " "), nil
}

// renderPredicate serializes one qualifier, after establishing that it is one.
//
// The keyword is emitted bare, so it is checked against a closed set rather
// than trusted. Its value goes through renderValue like any other, which is
// what keeps `BY` from becoming the one place a caller's string reaches Jira
// unquoted.
func renderPredicate(
	op Operator, p Predicate, seen map[PredicateKeyword]bool,
) (string, error) {
	if !predicateKeywords[p.Keyword] {
		return "", errs.Runtime("INVALID_EXPR",
			"unknown history predicate %q", string(p.Keyword))
	}
	if seen[p.Keyword] {
		// Jira takes the last one, so a repeat is a caller who thinks they have
		// narrowed a query and has replaced half of it.
		return "", errs.Runtime("INVALID_EXPR",
			"history predicate %s appears twice", string(p.Keyword))
	}
	seen[p.Keyword] = true

	if changedOnlyPredicates[p.Keyword] && op != OpChanged {
		return "", errs.Runtime("INVALID_EXPR",
			"predicate %s describes a transition and is only valid on CHANGED, not %s",
			string(p.Keyword), string(op))
	}
	if err := checkPredicateShape(p); err != nil {
		return "", err
	}

	value, err := renderValue(p.Value)
	if err != nil {
		return "", err
	}
	return string(p.Keyword) + " " + value, nil
}

// checkPredicateShape holds DURING to its pair of dates and everything else to
// a single value.
//
// DURING is the only predicate whose right-hand side is a list, and the only
// one where the wrong shape would still render: `DURING "-7d"` looks like every
// other predicate and asks Jira a question with half an answer in it.
func checkPredicateShape(p Predicate) error {
	list, isList := p.Value.(List)
	switch {
	case p.Keyword == PredDuring && !isList:
		return errs.Runtime("INVALID_EXPR", "DURING takes two dates, not one value")
	case p.Keyword == PredDuring && len(list) != 2:
		return errs.Runtime("INVALID_EXPR",
			"DURING takes two dates, got %d", len(list))
	case p.Keyword != PredDuring && isList:
		return errs.Runtime("INVALID_EXPR",
			"predicate %s takes one value, not a list", string(p.Keyword))
	}
	return nil
}

func renderValue(v Value) (string, error) {
	switch x := v.(type) {
	case Text:
		return quote(string(x))

	case Num:
		if !isNumeric(string(x)) {
			return "", errs.Runtime("INVALID_VALUE", "%q is not a number", string(x))
		}
		return string(x), nil

	case Keyword:
		switch x {
		case Empty, Null:
			return string(x), nil
		}
		return "", errs.Runtime("INVALID_VALUE", "unknown JQL keyword %q", string(x))

	case *Func:
		return renderFunc(x)

	case List:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if _, nested := item.(List); nested {
				return "", errs.Runtime("INVALID_VALUE", "a value list cannot contain a list")
			}
			s, err := renderValue(item)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return "(" + strings.Join(parts, ", ") + ")", nil

	case nil:
		return "", errs.Runtime("INVALID_VALUE", "clause has no value")

	default:
		return "", errs.Runtime("INVALID_VALUE", "unknown value type %T", v)
	}
}

func renderFunc(f *Func) (string, error) {
	if !isSimpleIdent(f.Name) {
		return "", errs.Runtime("INVALID_VALUE", "%q is not a valid function name", f.Name)
	}
	args := make([]string, 0, len(f.Args))
	for _, a := range f.Args {
		s, err := renderValue(a)
		if err != nil {
			return "", err
		}
		args = append(args, s)
	}
	return f.Name + "(" + strings.Join(args, ", ") + ")", nil
}

// renderField quotes a field name unless it is a plain identifier that is not a
// reserved word. `cf[10001]` and `customfield_10001` pass through; `Story
// Points` and a field literally named `order` are quoted.
func renderField(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		// Its own code, not INVALID_FIELD. That one is a caller's bad --field
		// and exits 2, which is what the contract publishes for it; this is a
		// clause built with no field at all, which no input can produce and no
		// caller can act on. One code with two exits gives a caller a different
		// answer depending on how deep the input got before something noticed.
		return "", errs.Runtime("INVALID_CLAUSE", "clause has no field name")
	}
	if isCustomFieldRef(name) {
		return name, nil
	}
	if isSimpleIdent(name) && !IsReserved(name) {
		return name, nil
	}
	return quote(name)
}

// quote wraps a value in double quotes and escapes what JQL requires.
//
// Every string value goes through here, unconditionally. Quoting only the
// values that "need" it would mean maintaining a rule about which do, and the
// cost of getting that rule wrong is a user-supplied value being parsed as
// syntax.
func quote(s string) (string, error) {
	// A JQL query travels as UTF-8. Ranging over a string with an invalid byte
	// yields U+FFFD, which would silently substitute a different value than the
	// caller asked to match — the exact silent corruption this package exists
	// to prevent. Refuse instead.
	if !utf8.ValidString(s) {
		return "", errs.Usage("INVALID_ENCODING",
			"value is not valid UTF-8 and cannot be sent as JQL").
			WithDetail("%q", s).
			WithRemedy("re-encode the value as UTF-8")
	}

	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// Unquote reads one rendered JQL value back into the string it stands for.
//
// It is the inverse of quote, and it lives beside it for the reason quote is
// the only place a value is quoted: an escape rule with two implementations is
// an escape rule that will eventually disagree with itself. Tokenize already
// knows every escape JQL defines, so this is a parse and not a second reading
// of the same grammar.
//
// The input is a value Jira rendered, not one a caller typed. The label
// suggestion endpoint answers in JQL's own spelling, a bare word where the
// value needs no quoting and a quoted literal where it does, so the label
// `a,b` comes back as the five characters "a,b" and `back\slash` as
// "back\\slash". Comparing that to what the caller typed without reading the
// quoting first would report every awkward label as unknown.
//
// Anything that is not exactly one value is refused rather than interpreted.
// Two tokens mean the string was never a single value, and a caller that
// guessed otherwise would be comparing against half of one.
func Unquote(s string) (string, error) {
	tokens, err := Tokenize(s)
	if err != nil {
		return "", err
	}
	if len(tokens) != 1 {
		return "", errs.Usage("JQL_SYNTAX",
			"%q is %d values, not one", s, len(tokens))
	}
	switch tokens[0].Kind {
	case TokString, TokIdent, TokNumber:
		return tokens[0].Text, nil
	default:
		return "", errs.Usage("JQL_SYNTAX",
			"%q is %s, not a value", s, tokens[0].Kind)
	}
}

// isSimpleIdent reports whether s can be written bare: a letter or underscore
// followed by letters, digits, or underscores.
func isSimpleIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// isCustomFieldRef reports whether s is the cf[NNNNN] custom field syntax,
// which cannot be quoted.
func isCustomFieldRef(s string) bool {
	rest, ok := strings.CutPrefix(strings.ToLower(s), "cf[")
	if !ok {
		return false
	}
	digits, ok := strings.CutSuffix(rest, "]")
	if !ok || digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
