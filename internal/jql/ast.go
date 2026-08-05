// Package jql builds JQL. It is never concatenated.
//
// A typed builder produces an AST and a single renderer serializes it. The
// renderer in render.go is the only place in this project where a string is
// quoted, so there is exactly one piece of code to audit for injection and
// exactly one place a quoting bug can live.
//
// Two rules follow from that and are enforced by test:
//
//   - Raw user JQL is always parenthesized. Without it, a user's OR escapes the
//     project scope: `project = ENG AND summary ~ x OR priority = Highest`
//     returns every Highest-priority issue in every project.
//   - JQL is never inspected with a regular expression. Deciding whether a
//     query already constrains a field means tokenizing it, because a regex
//     cannot tell `project = FOO` from `summary ~ "project = FOO"`.
package jql

import "strings"

// Expr is a JQL expression: a comparison, a boolean combination of them, or an
// opaque fragment supplied by the user.
type Expr interface {
	// isExpr keeps the set of expression types closed, so the renderer's type
	// switch is exhaustive by construction.
	isExpr()
}

// Operator is a JQL comparison operator.
type Operator string

// The comparison operators.
const (
	OpEq       Operator = "="
	OpNe       Operator = "!="
	OpGt       Operator = ">"
	OpGte      Operator = ">="
	OpLt       Operator = "<"
	OpLte      Operator = "<="
	OpLike     Operator = "~"
	OpNotLike  Operator = "!~"
	OpIn       Operator = "IN"
	OpNotIn    Operator = "NOT IN"
	OpIs       Operator = "IS"
	OpIsNot    Operator = "IS NOT"
	OpWas      Operator = "WAS"
	OpWasIn    Operator = "WAS IN"
	OpWasNot   Operator = "WAS NOT"
	OpWasNotIn Operator = "WAS NOT IN"
	OpChanged  Operator = "CHANGED"
)

// takesList reports whether the operator's right-hand side is a parenthesized
// list rather than a single value.
func (o Operator) takesList() bool {
	switch o {
	case OpIn, OpNotIn, OpWasIn, OpWasNotIn:
		return true
	}
	return false
}

// Clause is a single comparison: `field op value`.
type Clause struct {
	Field string
	Op    Operator
	Value Value
}

func (*Clause) isExpr() {}

// And is a conjunction. An empty And renders as nothing.
type And struct{ Exprs []Expr }

func (*And) isExpr() {}

// Or is a disjunction.
type Or struct{ Exprs []Expr }

func (*Or) isExpr() {}

// Not negates an expression.
type Not struct{ Expr Expr }

func (*Not) isExpr() {}

// Raw is a fragment supplied by the user, passed through untouched.
//
// It is always rendered inside parentheses. That is not a stylistic choice: it
// is the difference between a filter and a scope escape.
type Raw struct{ Text string }

func (*Raw) isExpr() {}

// Value is the right-hand side of a clause.
type Value interface {
	isValue()
}

// Text is a string value. The renderer always quotes it, so its content can
// never be read as syntax.
type Text string

func (Text) isValue() {}

// Num is a numeric value, rendered bare.
type Num string

func (Num) isValue() {}

// Keyword is a bare JQL keyword such as EMPTY or NULL, rendered unquoted.
// It is a distinct type precisely so a *user-supplied* string can never become
// one by accident.
type Keyword string

// The keywords a clause may compare against.
const (
	Empty Keyword = "EMPTY"
	Null  Keyword = "NULL"
)

func (Keyword) isValue() {}

// Func is a JQL function call such as currentUser() or startOfDay(-1d).
// Arguments are values and are rendered by the same rules as any other value.
type Func struct {
	Name string
	Args []Value
}

func (*Func) isValue() {}

// List is the right-hand side of IN and its variants.
type List []Value

func (List) isValue() {}

// Direction is a sort direction.
type Direction string

// The sort directions. There is no --reverse: a boolean negation on top of an
// implicit default is a coin flip every time.
const (
	Asc  Direction = "ASC"
	Desc Direction = "DESC"
)

// ParseDirection resolves an --order value.
func ParseDirection(s string) (Direction, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "asc", "ascending":
		return Asc, true
	case "desc", "descending":
		return Desc, true
	}
	return "", false
}

// Order is one ORDER BY term.
type Order struct {
	Field     string
	Direction Direction
}

// Query is a complete JQL query: a where expression and an ordering.
type Query struct {
	Where Expr
	Order []Order
}
