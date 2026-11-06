package registry

import (
	"strconv"

	"github.com/kmoneil/jr/internal/render"
)

// The request.cost kind lives here rather than in a resource because any
// command that can price itself answers the global --explain-cost with one,
// and resources may not import each other. schema.commands and contract are
// owned here for the same reason.
const (
	KindCost    = "request.cost"
	VersionCost = 1
)

// The steps a cost is split into. Each is a different kind of request, and
// the split is what lets a caller who disagrees with one of them see which.
const (
	// CostPreparation is everything a run spends before its first page: the
	// deployment probe when the cache is cold, and the lookups validation
	// makes. It is measured, by doing them, rather than predicted.
	CostPreparation = "preparation"
	// CostSearch is the pages of the walk, computed from a count.
	CostSearch = "search"
	// CostCheck is a request a run makes after the walk, because of what the
	// walk found.
	CostCheck = "check"
)

// The paging a search step will use. It decides how many rows each page
// after the first brings, which is why a cost names it.
const (
	PagingCursor = "cursor"
	PagingKeyset = "keyset"
	PagingOffset = "offset"
)

func init() {
	render.RegisterSchema(KindCost, CostSchema())
}

// CostSchema is the shape of a cost.
func CostSchema() *render.Schema {
	return &render.Schema{
		Element: "cost",
		Attrs: []render.Field{
			{Name: "requests", Type: render.TypeInt},
			{Name: "approximate", Type: render.TypeBool},
			{Name: "retries", Type: render.TypeInt},
			{Name: "spent", Type: render.TypeInt},
		},
		Children: []render.Child{
			{Schema: render.ListSchema("steps", "step", &render.Schema{
				Element: "step",
				Attrs: []render.Field{
					{
						Name: "name", Type: render.TypeString,
						Enum: []string{CostPreparation, CostSearch, CostCheck},
					},
					{Name: "requests", Type: render.TypeInt},
					{Name: "rows", Type: render.TypeInt, Optional: true},
					{Name: "page-size", Type: render.TypeInt, Optional: true},
					{
						Name: "paging", Type: render.TypeString, Optional: true,
						Enum: []string{PagingCursor, PagingKeyset, PagingOffset},
					},
				},
			})},
		},
	}
}

// Cost is what an invocation would spend in requests, said before it spends
// them.
//
// Requests is the --max-requests a run made now needs to finish, if no
// request is retried and the result set does not change under it. Retries
// are not predicted, because nothing before a request can say it will fail;
// they are named instead, so the ceiling is a multiplication the caller can
// do rather than a number this tool would have to guess.
type Cost struct {
	// Approximate is true when a step rests on a count the server itself
	// calls an estimate, which is every count Cloud gives.
	Approximate bool
	// Retries is the --retries in effect: each request may be sent that many
	// more times, and every send counts against --max-requests.
	Retries int
	// Spent is what answering cost: the preparation, which a run made now
	// spends again unless this answer cached it, and the count.
	Spent int
	Steps []CostStep
}

// CostStep is one kind of request in a cost.
type CostStep struct {
	Name     string
	Requests int
	// Rows, PageSize and Paging describe a search step and are absent on
	// every other. Rows is how many the count said the query matches, before
	// any --limit.
	Rows     *int
	PageSize int
	Paging   string
}

// Requests is the sum of the steps.
func (c Cost) Requests() int {
	total := 0
	for _, s := range c.Steps {
		total += s.Requests
	}
	return total
}

// Node renders a cost.
func (c Cost) Node() *render.Node {
	steps := make([]*render.Node, 0, len(c.Steps))
	for _, s := range c.Steps {
		step := render.El("step").
			Attr("name", s.Name).
			Attr("requests", strconv.Itoa(s.Requests))
		if s.Rows != nil {
			step = step.Attr("rows", strconv.Itoa(*s.Rows))
		}
		if s.PageSize > 0 {
			step = step.Attr("page-size", strconv.Itoa(s.PageSize))
		}
		if s.Paging != "" {
			step = step.Attr("paging", s.Paging)
		}
		steps = append(steps, step)
	}
	return render.El("cost").
		Attr("requests", strconv.Itoa(c.Requests())).
		Attr("approximate", strconv.FormatBool(c.Approximate)).
		Attr("retries", strconv.Itoa(c.Retries)).
		Attr("spent", strconv.Itoa(c.Spent)).
		Child(render.ListEl("steps", "step", steps...))
}

// Doc is the cost as the document --explain-cost answers with.
func (c Cost) Doc() *render.Doc {
	return render.Record(KindCost, VersionCost, c.Node())
}
