package transport

import (
	"sync"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
)

// BudgetExceededCode is the error code for a request budget that ran out.
//
// It is distinct so the pagination layer can recognize it and turn it into a
// partial result — data on stdout, a resumable cursor, and exit 3 — rather than
// a bare failure. Hitting the budget means "there is more", not "this broke".
const BudgetExceededCode = "REQUEST_BUDGET_EXCEEDED"

// budget caps the total HTTP calls one invocation may make.
//
// It exists so an agent's `--limit all` against a 40,000-issue project cannot
// become a four-hour job. Retries count against it: from the server's point of
// view a retry is another request, and a budget that ignored them would not
// bound anything.
type budget struct {
	mu    sync.Mutex
	max   int
	count int
}

func newBudget(maxRequests int) *budget {
	return &budget{max: maxRequests}
}

// consume records one request, or reports that the budget is spent.
func (b *budget) consume() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.max > 0 && b.count >= b.max {
		return errs.New(exitcode.Partial, BudgetExceededCode,
			"stopped after %d requests, the --max-requests limit", b.max).
			WithRemedy("raise --max-requests, narrow the query, or resume with --page-token")
	}
	b.count++
	return nil
}

func (b *budget) spent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// remaining returns how many calls are left, or -1 when unlimited.
func (b *budget) remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.max <= 0 {
		return -1
	}
	return max(b.max-b.count, 0)
}

// IsBudgetExceeded reports whether err is the request budget running out.
func IsBudgetExceeded(err error) bool {
	e, ok := errs.AsError(err)
	return ok && e.Code == BudgetExceededCode
}
