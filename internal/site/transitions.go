package site

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
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
	//
	// Nil means the server did not say, which on Data Center is always: 9.12.38
	// and 10.4.0 both omit hasScreen under every expand tried — none,
	// transitions.fields, transitions, hasScreen. A plain bool reported every
	// transition there as screenless, and a consumer branching on that skips a
	// form Jira would have shown.
	HasScreen *bool `json:"hasScreen,omitempty"`
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
		// A pointer so an absent hasScreen stays absent rather than decoding to
		// false, which is a claim Data Center never makes.
		HasScreen *bool                      `json:"hasScreen"`
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

// Resolution is one resolution a transition's screen offers.
type Resolution struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Resolution turns a resolution name or id into the one this transition's
// screen offers, spelled the way Jira has to be sent it.
//
// It is resolved the way the transition itself is: an id first and exactly,
// then a name in any case, and a name matching two is refused with both. The
// answer goes out as the name, which Jira takes as readily as the id and which
// is what a person reading a dry run or a plan recognises. It has to be the
// site's own spelling, because Jira matches the name case-sensitively: Data
// Center 10.4.0 refused `won't do` where the resolution is `Won't Do`, and
// refused `10001` sent as a name.
//
// The set is the screen's, not the site's, because a resolution the site has
// is not necessarily one the transition offers. It arrives with the
// transitions read that resolved the transition, so this costs no request.
//
// A transition whose screen has no resolution field is refused, as Jira
// refuses it on both deployments: "Field 'resolution' cannot be set. It is not
// on the appropriate screen, or unknown." None of the default workflows
// measured on either puts a screen on any transition, so that is the common
// case and not the edge. A
// screen that has the field and lists no values constrains nothing, which is
// what an empty AllowedValues means everywhere else; the input goes as typed
// and Jira decides. Nothing has shown that shape, and refusing it would invent
// a limit nobody measured.
//
// An empty input asks for no resolution and gets none, whatever the screen.
func (t Transition) Resolution(input string) (Resolution, error) {
	want := strings.TrimSpace(input)
	if want == "" {
		return Resolution{}, nil
	}
	field, ok := t.field("resolution")
	if !ok {
		return Resolution{}, errs.Usage("TRANSITION_TAKES_NO_RESOLUTION",
			"the %s transition has no resolution field, so a resolution cannot "+
				"be set with it", t.Name).
			WithDetail("Jira refuses a resolution sent with a transition whose "+
				"screen does not show one; this one's screen has %d field(s)",
				len(t.Fields)).
			WithRemedy("take the transition without a resolution, or one whose "+
				"screen has the field: `%s meta transitions <key> --format xml` "+
				"lists each transition's fields", buildinfo.App)
	}

	offered := resolutionsOffered(field)
	if len(offered) == 0 {
		return Resolution{Name: want}, nil
	}
	// An id is checked first and on its own, because ids are unique and an
	// exact id match is therefore never ambiguous.
	for _, r := range offered {
		if r.ID != "" && r.ID == want {
			return r, nil
		}
	}
	var matches []Resolution
	for _, r := range offered {
		if strings.EqualFold(r.Name, want) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		// Every candidate rather than near misses: a screen offers a handful,
		// and the one the caller meant may be spelled nothing like it.
		return Resolution{}, errs.Usage("UNKNOWN_RESOLUTION",
			"the %s transition offers no resolution %q", t.Name, input).
			WithDetail("available: %s", describeResolutions(offered)).
			WithRemedy("pass one of these, by name or id; they are the ones " +
				"this transition's screen offers")
	default:
		return Resolution{}, errs.Usage("AMBIGUOUS_RESOLUTION",
			"%q names %d resolutions on the %s transition",
			input, len(matches), t.Name).
			WithDetail("%s", describeResolutions(matches)).
			WithRemedy("pass the id of the one you mean")
	}
}

// field finds a field on the transition's screen by id. The id and not the
// name, because a custom field can be called Resolution and is not the field
// `fields.resolution` sets.
func (t Transition) field(id string) (MetaField, bool) {
	for _, f := range t.Fields {
		if f.ID == id {
			return f, true
		}
	}
	return MetaField{}, false
}

// resolutionsOffered pairs each value the resolution field allows with its id.
func resolutionsOffered(f MetaField) []Resolution {
	out := make([]Resolution, 0, len(f.AllowedValues))
	for i, name := range f.AllowedValues {
		r := Resolution{Name: name}
		if i < len(f.AllowedIDs) {
			r.ID = f.AllowedIDs[i]
		}
		out = append(out, r)
	}
	return out
}

// describeResolutions renders candidates for an error detail: the name the
// caller recognises, and the id they can pass instead where Jira sent one.
func describeResolutions(items []Resolution) string {
	parts := make([]string, 0, len(items))
	for _, r := range items {
		if r.ID == "" {
			parts = append(parts, r.Name)
			continue
		}
		parts = append(parts, r.Name+" ("+r.ID+")")
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
