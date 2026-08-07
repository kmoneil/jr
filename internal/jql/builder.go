package jql

// Builder accumulates AND-ed conditions and produces a Query.
//
// Every method takes values as *data*. There is no method that takes a
// fragment of syntax except Raw, and Raw is parenthesized. That is the whole
// safety property: a caller cannot construct a query that reinterprets a value
// as syntax, because no method gives it the chance.
//
//	q := jql.New().
//	    Project("ENG").
//	    In("labels", labels...).      // values are data, never syntax
//	    Raw(userSuppliedJQL)          // always wrapped in parens
type Builder struct {
	conds []Expr
	order []Order
}

// New returns an empty builder.
func New() *Builder { return &Builder{} }

// Where appends an arbitrary expression as a top-level condition.
func (b *Builder) Where(e Expr) *Builder {
	if e != nil {
		b.conds = append(b.conds, e)
	}
	return b
}

// Clause appends `field op value`.
func (b *Builder) Clause(field string, op Operator, v Value) *Builder {
	return b.Where(&Clause{Field: field, Op: op, Value: v})
}

// Eq appends `field = value`.
func (b *Builder) Eq(field, value string) *Builder {
	return b.Clause(field, OpEq, Text(value))
}

// Ne appends `field != value`.
func (b *Builder) Ne(field, value string) *Builder {
	return b.Clause(field, OpNe, Text(value))
}

// Like appends `field ~ value`.
func (b *Builder) Like(field, value string) *Builder {
	return b.Clause(field, OpLike, Text(value))
}

// NotLike appends `field !~ value`.
func (b *Builder) NotLike(field, value string) *Builder {
	return b.Clause(field, OpNotLike, Text(value))
}

// In appends `field IN (values...)`, or nothing when values is empty.
//
// An empty filter is dropped rather than rendered, because `field IN ()` is
// neither valid JQL nor a meaningful request. A caller that means "match
// nothing" must say so explicitly.
func (b *Builder) In(field string, values ...string) *Builder {
	return b.inList(field, OpIn, values)
}

// NotIn appends `field NOT IN (values...)`, or nothing when values is empty.
func (b *Builder) NotIn(field string, values ...string) *Builder {
	return b.inList(field, OpNotIn, values)
}

func (b *Builder) inList(field string, op Operator, values []string) *Builder {
	if len(values) == 0 {
		return b
	}
	if len(values) == 1 {
		// A one-element IN is the same query as an equality and reads better
		// in an error message or a saved filter.
		single := OpEq
		if op == OpNotIn {
			single = OpNe
		}
		return b.Clause(field, single, Text(values[0]))
	}
	list := make(List, 0, len(values))
	for _, v := range values {
		list = append(list, Text(v))
	}
	return b.Clause(field, op, list)
}

// Project scopes the query to one or more project keys.
func (b *Builder) Project(keys ...string) *Builder {
	return b.In("project", keys...)
}

// IsEmpty appends `field IS EMPTY`.
func (b *Builder) IsEmpty(field string) *Builder {
	return b.Clause(field, OpIs, Empty)
}

// IsNotEmpty appends `field IS NOT EMPTY`.
func (b *Builder) IsNotEmpty(field string) *Builder {
	return b.Clause(field, OpIsNot, Empty)
}

// Gte appends `field >= value`, for an already-validated value.
func (b *Builder) Gte(field string, v Value) *Builder {
	return b.Clause(field, OpGte, v)
}

// Lte appends `field <= value`, for an already-validated value.
func (b *Builder) Lte(field string, v Value) *Builder {
	return b.Clause(field, OpLte, v)
}

// Changed appends `field CHANGED` with its history predicates.
//
// With no predicates it asks whether the field ever changed, which is a real
// question and rarely the one anybody means; the caller is expected to qualify
// it.
func (b *Builder) Changed(field string, preds ...Predicate) *Builder {
	return b.Where(&Clause{Field: field, Op: OpChanged, Predicates: preds})
}

// Was appends `field WAS value`, optionally qualified by when.
func (b *Builder) Was(field string, v Value, preds ...Predicate) *Builder {
	return b.Where(&Clause{Field: field, Op: OpWas, Value: v, Predicates: preds})
}

// By, After, and Before build the three predicates this project uses.
//
// The other four — FROM, TO, ON, DURING — are rendered and validated but have
// no constructor, because nothing here builds one and an untested convenience
// is a claim about behaviour nobody has checked. A caller that needs one writes
// the Predicate literal, and the renderer holds it to the same rules.
func By(v Value) Predicate { return Predicate{Keyword: PredBy, Value: v} }

// After qualifies a history clause with a lower time bound.
func After(v Value) Predicate { return Predicate{Keyword: PredAfter, Value: v} }

// Before qualifies a history clause with an upper time bound.
func Before(v Value) Predicate { return Predicate{Keyword: PredBefore, Value: v} }

// Raw appends a user-supplied fragment. It is always parenthesized when
// rendered, so a user's OR cannot escape the scope the other conditions set.
//
// Raw is not validated here. Call Validate on the finished query, or
// `jr jql validate` to have Jira parse it, before sending it anywhere.
func (b *Builder) Raw(text string) *Builder {
	return b.Where(&Raw{Text: text})
}

// OrderBy appends a sort term. Repeated calls order by each field in turn.
func (b *Builder) OrderBy(field string, dir Direction) *Builder {
	if field == "" {
		return b
	}
	if dir == "" {
		dir = Asc
	}
	b.order = append(b.order, Order{Field: field, Direction: dir})
	return b
}

// Query returns the accumulated query.
func (b *Builder) Query() *Query {
	q := &Query{Order: b.order}
	if len(b.conds) > 0 {
		q.Where = &And{Exprs: b.conds}
	}
	return q
}

// String renders the query, or returns the empty string if it cannot be
// rendered. Use Render for the error.
func (b *Builder) String() string {
	s, err := Render(b.Query())
	if err != nil {
		return ""
	}
	return s
}

// Render serializes the accumulated query.
func (b *Builder) Render() (string, error) { return Render(b.Query()) }

// AnyOf combines expressions with OR. The result is parenthesized when it is
// nested inside another expression, so mixing it with AND cannot change the
// meaning of the surrounding query.
func AnyOf(exprs ...Expr) Expr { return &Or{Exprs: exprs} }

// AllOf combines expressions with AND.
func AllOf(exprs ...Expr) Expr { return &And{Exprs: exprs} }

// Negate wraps an expression in NOT.
func Negate(e Expr) Expr { return &Not{Expr: e} }

// Eq builds a standalone `field = value` clause, for composing with AnyOf.
func Eq(field, value string) Expr {
	return &Clause{Field: field, Op: OpEq, Value: Text(value)}
}
