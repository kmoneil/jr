package site

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/nearest"
	"github.com/kmoneil/jr/internal/transport"
)

// FieldsPath is the endpoint that lists a site's fields, relative to the API
// base. Both deployments serve the whole catalogue in one response — there is
// no cursor here, which is why nothing in this file pages.
const FieldsPath = "/field"

// Field is one entry in a site's field catalogue.
//
// ID is what a request asks for and what the output is keyed by; Name is what
// a person calls it. They are different strings for every custom field, which
// is the entire reason this catalogue exists.
type Field struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Custom bool   `json:"custom"`
	// Type is the schema type, e.g. string, number, array. It is empty for a
	// field the server described without a schema.
	Type string `json:"type,omitempty"`
	// Items is the element type of an array field.
	Items string `json:"items,omitempty"`
	// ClauseNames are the names JQL accepts for this field. A custom field is
	// addressable as cf[10042] as well as by its name, and both are resolvable
	// here for the same reason: they are what a person actually types.
	ClauseNames []string `json:"clauseNames,omitempty"`
	Searchable  bool     `json:"searchable"`
	Orderable   bool     `json:"orderable"`
	Navigable   bool     `json:"navigable"`
}

// Catalogue is every field a site has, as one immutable snapshot.
type Catalogue struct {
	Fields []Field `json:"fields"`
}

// rawField is the response shape, which both deployments share.
type rawField struct {
	ID          string   `json:"id"`
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Custom      bool     `json:"custom"`
	Searchable  bool     `json:"searchable"`
	Orderable   bool     `json:"orderable"`
	Navigable   bool     `json:"navigable"`
	ClauseNames []string `json:"clauseNames"`
	Schema      struct {
		Type   string `json:"type"`
		Items  string `json:"items"`
		Custom string `json:"custom"`
	} `json:"schema"`
}

// FetchFields reads a site's whole field catalogue.
func FetchFields(ctx context.Context, client Doer, info Info) (*Catalogue, error) {
	path := info.APIBase() + FieldsPath

	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   path,
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw []rawField
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, errs.Remote("MALFORMED_FIELD_LIST",
			"%s did not return a usable field catalogue", path).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}

	fields := make([]Field, 0, len(raw))
	for _, r := range raw {
		id := r.ID
		if id == "" {
			// Data Center populates key and leaves id empty on some fields.
			// Preferring one silently would drop the field from the catalogue,
			// and a field missing from the catalogue is a name that cannot be
			// resolved for reasons nobody can see.
			id = r.Key
		}
		fields = append(fields, Field{
			ID:          id,
			Name:        r.Name,
			Custom:      r.Custom,
			Type:        r.Schema.Type,
			Items:       r.Schema.Items,
			ClauseNames: r.ClauseNames,
			Searchable:  r.Searchable,
			Orderable:   r.Orderable,
			Navigable:   r.Navigable,
		})
	}

	// Sorted by id so two runs against the same site produce the same rows in
	// the same order. The server does not promise an order, and a collection
	// whose order changes between invocations is one a script cannot diff.
	sort.Slice(fields, func(i, j int) bool { return fields[i].ID < fields[j].ID })
	return &Catalogue{Fields: fields}, nil
}

// Resolve turns what a caller typed into a field id.
//
// It accepts an id, a name, or a JQL clause name, all case-insensitively, and
// returns the canonical id. Nothing is guessed: an input that matches no field
// is refused with the near misses, and one that matches several is refused with
// the candidates rather than resolved to whichever came first.
func (c *Catalogue) Resolve(input string) (Field, error) {
	want := strings.ToLower(strings.TrimSpace(input))
	if want == "" {
		return Field{}, errs.Usage("INVALID_FIELD", "a field name cannot be empty")
	}

	// An id is checked first and on its own. Ids are unique, so an exact id
	// match is never ambiguous — whereas a field whose *name* happens to equal
	// another field's id would otherwise make an exact id lookup fail as
	// ambiguous.
	for _, f := range c.Fields {
		if strings.EqualFold(f.ID, want) {
			return f, nil
		}
	}

	var matches []Field
	for _, f := range c.Fields {
		if strings.EqualFold(f.Name, want) || hasFold(f.ClauseNames, want) {
			matches = append(matches, f)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Field{}, c.unknownField(input)
	default:
		// Jira lets two custom fields share a name, and they are different
		// fields with different values. Picking one would silently report the
		// wrong column for every issue.
		return Field{}, errs.Usage("AMBIGUOUS_FIELD",
			"%q names %d fields on this site", input, len(matches)).
			WithDetail("%s", describe(matches)).
			WithRemedy("pass the id of the one you mean")
	}
}

// unknownField builds the refusal for an input that named nothing, with the
// closest candidates. A bare "unknown field" leaves the caller to go and read
// the whole catalogue to find a typo.
func (c *Catalogue) unknownField(input string) error {
	e := errs.Usage("UNKNOWN_FIELD", "this site has no field called %q", input)
	if near := c.NearMatches(input, maxSuggestions); len(near) > 0 {
		return e.WithDetail("did you mean: %s", describe(near)).
			WithRemedy("run `jr field list` for the full catalogue")
	}
	return e.WithRemedy("run `jr field list` for the full catalogue")
}

// maxSuggestions bounds the near-match list. A refusal that prints forty
// candidates is one nobody reads.
const maxSuggestions = 5

// NearMatches returns the fields whose name or id is closest to the input,
// nearest first and at most n of them.
func (c *Catalogue) NearMatches(input string, n int) []Field {
	want := strings.ToLower(strings.TrimSpace(input))
	if want == "" {
		return nil
	}

	type scored struct {
		field Field
		dist  int
	}
	var out []scored
	for _, f := range c.Fields {
		d := min(distance(want, strings.ToLower(f.Name)), distance(want, strings.ToLower(f.ID)))
		if d <= threshold(want) {
			out = append(out, scored{f, d})
		}
	}

	// Ties break on id so the suggestion list is the same on every run: two
	// equally-close candidates must not swap places between invocations.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].dist != out[j].dist {
			return out[i].dist < out[j].dist
		}
		return out[i].field.ID < out[j].field.ID
	})
	if len(out) > n {
		out = out[:n]
	}

	fields := make([]Field, 0, len(out))
	for _, s := range out {
		fields = append(fields, s.field)
	}
	return fields
}

// threshold and distance are internal/nearest's, which is where this rule
// lives now. It was written here first, for the field catalogue, and three
// other refusals in this tree then invented their own idea of "close": a
// substring match for a command name, cobra's suggester for a subcommand, and
// nothing at all for a flag. One rule, one package, four callers.
func threshold(s string) int { return nearest.Threshold(s) }

func distance(a, b string) int { return nearest.Distance(a, b) }

// describe renders candidates for an error detail: the name is what the caller
// typed and the id is what they need instead, so both have to be there.
func describe(fields []Field) string {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, f.Name+" ("+f.ID+")")
	}
	return strings.Join(parts, ", ")
}

func hasFold(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}

// fieldsCacheKey is the cache entry the catalogue is stored under, which puts
// it at <cache>/<site>/fields.json.
const fieldsCacheKey = "fields"

// Metadata is a site's descriptive data, fetched once and cached.
//
// It exists so that resolving a custom field name to customfield_10042 does not
// cost a round trip on every invocation — §9.1 calls that a feature rather than
// an optimization, because a name that is expensive to use is one people stop
// using in favour of the id.
//
// It is also where issue types, statuses, and transitions will live: they are
// the same shape of problem, cached the same way, and a command should reach
// all of them through one accessor rather than four.
type Metadata struct {
	Client Doer
	Info   Info
	// Cache may be nil, which costs a round trip per invocation and nothing
	// else.
	Cache *Cache
	// TTL overrides DefaultTTL.
	TTL time.Duration
	// Now is the time source, so a test can expire an entry without waiting.
	Now func() time.Time
	// Refresh forces a fetch even when a valid cache entry exists.
	Refresh bool

	once      sync.Once
	catalogue *Catalogue
	err       error
}

// Fields returns the site's field catalogue, from cache when it is fresh.
//
// The result is memoized for the life of the process as well as on disk, so a
// command that resolves a name while validating its flags and again while
// running makes one request, not two.
func (m *Metadata) Fields(ctx context.Context) (*Catalogue, error) {
	m.once.Do(func() { m.catalogue, m.err = m.fields(ctx) })
	return m.catalogue, m.err
}

func (m *Metadata) fields(ctx context.Context) (*Catalogue, error) {
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	ttl := m.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}

	if m.Cache != nil {
		// Share this accessor's clock, so an entry it writes is one it will
		// accept back.
		m.Cache.Now = now
	}

	if m.Cache != nil && !m.Refresh {
		var cached Catalogue
		if ok, err := m.Cache.Get(fieldsCacheKey, ttl, &cached); err != nil {
			return nil, err
		} else if ok {
			return &cached, nil
		}
	}

	catalogue, err := FetchFields(ctx, m.Client, m.Info)
	if err != nil {
		return nil, err
	}
	if m.Cache != nil {
		// A cache that cannot be written is not worth failing the command for:
		// the fetch succeeded, and the only cost is doing it again next time.
		_ = m.Cache.Put(fieldsCacheKey, catalogue)
	}
	return catalogue, nil
}
