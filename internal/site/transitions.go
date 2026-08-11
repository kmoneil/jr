package site

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/transport"
)

// Transition is one workflow move available on an issue right now.
type Transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// To is the status the issue lands in.
	To Status `json:"to"`
	// HasScreen reports whether Jira would show a form. A transition with a
	// screen may have required fields, which is why the flag is carried rather
	// than inferred from the field list being empty.
	HasScreen bool `json:"hasScreen"`
	// Fields are the fields this transition accepts, required ones first.
	Fields []MetaField `json:"fields,omitempty"`
}

// Status is a workflow state plus the category it belongs to.
//
// The category matters more than the name for anything automated: a project can
// rename "In Progress" to anything, but the category stays one of three values.
type Status struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// Transitions is what an issue can do next.
type Transitions struct {
	// IssueKey is the issue this was read for. It is carried so a caller
	// cannot mistake one issue's transitions for another's.
	IssueKey string       `json:"issueKey"`
	Items    []Transition `json:"transitions"`
}

// rawTransitions is the response shape, which both deployments share.
type rawTransitions struct {
	Transitions []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		To   struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			StatusCategory struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"statusCategory"`
		} `json:"to"`
		HasScreen bool                       `json:"hasScreen"`
		Fields    map[string]json.RawMessage `json:"fields"`
	} `json:"transitions"`
}

// FetchTransitions asks what an issue can do next.
//
// The result is deliberately never cached. It depends on the issue's current
// status and on this user's permissions, so a stored copy answers the question
// as it stood when it was stored — and a caller acting on a stale list picks a
// transition id the workflow no longer offers. §9.1 caches the *workflow*, not
// the answer to "what can I do right now".
func FetchTransitions(
	ctx context.Context, client Doer, info Info, issueKey string,
) (*Transitions, error) {
	// Escaped because the key is a caller's argument reaching a URL path. Go's
	// JoinPath cleans ".." rather than refusing it, so an unescaped key holding
	// separators would resolve to a different endpoint on the same host than
	// the one this function names.
	path := info.APIBase() + "/issue/" + url.PathEscape(issueKey) + "/transitions"

	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   path,
		Query:  map[string][]string{"expand": {"transitions.fields"}},
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw rawTransitions
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, errs.Remote("MALFORMED_TRANSITIONS",
			"%s did not return usable transitions", path).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}

	items := make([]Transition, 0, len(raw.Transitions))
	for _, t := range raw.Transitions {
		fields, err := decodeMetaFields(t.Fields, path, resp.RequestID)
		if err != nil {
			return nil, err
		}
		items = append(items, Transition{
			ID:   t.ID,
			Name: t.Name,
			To: Status{
				ID:   t.To.ID,
				Name: t.To.Name,
				Category: NormalizeCategory(
					t.To.StatusCategory.Key, t.To.StatusCategory.Name,
				),
			},
			HasScreen: t.HasScreen,
			Fields:    fields,
		})
	}

	// Sorted by id so two runs against one issue produce the same rows in the
	// same order. The server promises no order, and a collection whose order
	// changes between invocations is one a script cannot diff.
	sort.Slice(items, func(i, j int) bool { return lessID(items[i].ID, items[j].ID) })
	return &Transitions{IssueKey: issueKey, Items: items}, nil
}

// Resolve turns a transition name or id into the transition itself.
//
// Nothing is guessed. A name matching nothing is refused with the available
// moves — which is a short list, so all of them are worth printing — and a name
// matching several is refused with the candidates.
func (t *Transitions) Resolve(input string) (Transition, error) {
	want := strings.TrimSpace(input)
	if want == "" {
		return Transition{}, errs.Usage("INVALID_TRANSITION",
			"a transition cannot be empty")
	}

	// An id is checked first and on its own, because ids are unique and an
	// exact id match is therefore never ambiguous.
	for _, item := range t.Items {
		if item.ID == want {
			return item, nil
		}
	}

	var matches []Transition
	for _, item := range t.Items {
		if strings.EqualFold(item.Name, want) {
			matches = append(matches, item)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Transition{}, t.unavailable(input)
	default:
		// A workflow can offer two transitions with the same name going to
		// different statuses. Picking one would move the issue somewhere the
		// caller did not ask for.
		return Transition{}, errs.Usage("AMBIGUOUS_TRANSITION",
			"%q names %d transitions on %s", input, len(matches), t.IssueKey).
			WithDetail("%s", describeTransitions(matches)).
			WithRemedy("pass the id of the one you mean")
	}
}

// unavailable builds the refusal, listing everything the issue can actually do.
//
// The whole list goes in rather than near misses only: a workflow offers a
// handful of moves, and "not available" is far more often a transition that
// exists but is blocked from the current status than it is a typo.
func (t *Transitions) unavailable(input string) error {
	e := errs.Usage("UNKNOWN_TRANSITION",
		"%s cannot be transitioned by %q right now", t.IssueKey, input)
	if len(t.Items) == 0 {
		return e.WithDetail("this issue offers no transitions at all").
			WithRemedy("check that the credential may act on this issue")
	}
	return e.WithDetail("available: %s", describeTransitions(t.Items)).
		WithRemedy("a transition missing here is blocked from the current status, " +
			"not misspelled")
}

// describeTransitions renders candidates for an error detail. The name is what
// the caller typed and the id is what they need instead, so both go in.
func describeTransitions(items []Transition) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.Name+" ("+item.ID+" → "+item.To.Name+")")
	}
	return strings.Join(parts, ", ")
}

// Transitions returns what an issue can do next.
//
// It is on Metadata beside the field catalogue because `issue move` has to
// resolve a transition name and a resource may not import another resource.
// Unlike the catalogue it is never cached, and never memoized either: two calls
// in one process can legitimately differ if something moved the issue in
// between, and answering the second from the first would hide that.
func (m *Metadata) Transitions(ctx context.Context, issueKey string) (*Transitions, error) {
	return FetchTransitions(ctx, m.Client, m.Info, issueKey)
}

// NormalizeCategory maps Jira's status category onto a stable name.
//
// Jira's own keys are "new", "indeterminate", and "done", which are not words
// anyone would guess.
func NormalizeCategory(key, name string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "new", "undefined":
		return CategoryToDo
	case "indeterminate":
		return CategoryInProgress
	case "done":
		return CategoryDone
	}
	// Some Data Center versions omit the key and send only a name.
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "to do", "new":
		return CategoryToDo
	case "in progress":
		return CategoryInProgress
	case "done", "complete":
		return CategoryDone
	}
	return CategoryUnknown
}

// Status categories, normalized.
const (
	CategoryToDo       = "to-do"
	CategoryInProgress = "in-progress"
	CategoryDone       = "done"
	CategoryUnknown    = "unknown"
)

// lessID orders ids numerically when they are numbers and lexically otherwise.
//
// Jira transition ids are numeric strings, and "11" sorts below "2" as text —
// the same trap issue keys have, in a place nobody would think to look.
func lessID(a, b string) bool {
	an, aok := numeric(a)
	bn, bok := numeric(b)
	if aok && bok {
		if an != bn {
			return an < bn
		}
		return a < b
	}
	if aok != bok {
		// A numeric id sorts before a non-numeric one, so the order is total
		// whatever a plugin puts in the field.
		return aok
	}
	return a < b
}

// numeric parses an unsigned decimal id, reporting whether the whole string was
// one. It does not use strconv.Atoi because an id long enough to overflow must
// fall back to text rather than wrap to a small number.
func numeric(s string) (uint64, bool) {
	if s == "" || len(s) > 18 {
		return 0, false
	}
	var n uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + uint64(r-'0')
	}
	return n, true
}
