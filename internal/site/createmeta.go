package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// MetaField is one field a create or a transition accepts.
type MetaField struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Required bool   `json:"required"`
	// Type is the schema type, e.g. string, number, array.
	Type string `json:"type,omitempty"`
	// Items is the element type of an array field.
	Items string `json:"items,omitempty"`
	// AllowedValues are the values Jira will accept, when it constrains them.
	// They are the difference between "this field is required" and a caller
	// being able to fill it in without a second lookup.
	AllowedValues []string `json:"allowedValues,omitempty"`
	// HasDefault reports whether Jira supplies a value when none is given, so
	// a required field with a default is not reported as something the caller
	// must provide.
	HasDefault bool `json:"hasDefault"`
}

// CreateMeta is what a project and issue type require at creation time.
type CreateMeta struct {
	Project   string      `json:"project"`
	IssueType string      `json:"issueType"`
	Fields    []MetaField `json:"fields"`
}

// IssueType is one type a project offers.
type IssueType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

// rawMetaField is the field description both deployments share, though they
// reach it by different routes.
type rawMetaField struct {
	FieldID  string `json:"fieldId"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Schema   struct {
		Type  string `json:"type"`
		Items string `json:"items"`
	} `json:"schema"`
	HasDefaultValue bool              `json:"hasDefaultValue"`
	AllowedValues   []json.RawMessage `json:"allowedValues"`
}

func (r rawMetaField) convert(id string) MetaField {
	if r.FieldID != "" {
		id = r.FieldID
	} else if r.Key != "" && id == "" {
		id = r.Key
	}
	return MetaField{
		ID:            id,
		Name:          r.Name,
		Required:      r.Required,
		Type:          r.Schema.Type,
		Items:         r.Schema.Items,
		HasDefault:    r.HasDefaultValue,
		AllowedValues: allowedValueNames(r.AllowedValues),
	}
}

// allowedValueNames reduces Jira's allowed values to something printable.
//
// They arrive as objects with a name, a value, or just an id depending on the
// field type. A value that reduces to nothing is dropped rather than rendered
// as an empty string, because an empty cell in a list of choices reads as a
// choice.
func allowedValueNames(raw []json.RawMessage) []string {
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		var obj struct {
			Name  string `json:"name"`
			Value string `json:"value"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(item, &obj); err == nil {
			switch {
			case obj.Name != "":
				out = append(out, obj.Name)
				continue
			case obj.Value != "":
				out = append(out, obj.Value)
				continue
			case obj.ID != "":
				out = append(out, obj.ID)
				continue
			}
		}
		var text string
		if err := json.Unmarshal(item, &text); err == nil && text != "" {
			out = append(out, text)
		}
	}
	return out
}

// decodeMetaFields converts a map of field id to description, sorted so two
// runs agree: required fields first, then by id.
func decodeMetaFields(
	raw map[string]json.RawMessage, path, requestID string,
) ([]MetaField, error) {
	fields := make([]MetaField, 0, len(raw))
	for id, item := range raw {
		var rf rawMetaField
		if err := json.Unmarshal(item, &rf); err != nil {
			return nil, errs.Remote("MALFORMED_FIELD_META",
				"%s described field %q in a shape this tool cannot read", path, id).
				WithRequestID(requestID).
				Wrap(err)
		}
		fields = append(fields, rf.convert(id))
	}
	sortMetaFields(fields)
	return fields, nil
}

// sortMetaFields puts what a caller must supply at the top, because that is the
// question createmeta is asked.
func sortMetaFields(fields []MetaField) {
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].Required != fields[j].Required {
			return fields[i].Required
		}
		return fields[i].ID < fields[j].ID
	})
}

// FetchIssueTypes lists the types a project offers.
//
// It exists because createmeta is addressed by issue type *id* while a caller
// types a name, on both deployments. It is also what makes the two failures
// distinguishable: this request 404s for a project that is not there, and a
// name missing from what it returns is an unknown type reported with the list
// of real ones.
func FetchIssueTypes(
	ctx context.Context, client Doer, info Info, project string,
) ([]IssueType, error) {
	path := info.APIBase() + "/issue/createmeta/" + url.PathEscape(project) + "/issuetypes"

	var out []IssueType
	startAt := 0
	for {
		page, err := fetchIssueTypePage(ctx, client, path, project, startAt)
		if err != nil {
			return nil, err
		}
		values := page.values()
		for _, v := range values {
			out = append(out, IssueType(v))
		}

		// A page that added nothing ends the loop whatever the server claims,
		// so a server that reports isLast=false forever cannot spin here.
		if len(values) == 0 || page.IsLast || len(out) >= page.Total {
			break
		}
		startAt += len(values)
	}

	sort.Slice(out, func(i, j int) bool { return lessID(out[i].ID, out[j].ID) })
	return out, nil
}

// issueTypePage is one createmeta response.
//
// The two deployments name the array differently. Cloud returns "issueTypes"
// and no isLast; Data Center returns the paged-collection envelope with
// "values". The route and the parameters are identical, which is what made this
// easy to miss: the request was right, the response parsed to nothing, and the
// command reported that a project with seven issue types had none by the
// requested name.
type issueTypePage struct {
	MaxResults int          `json:"maxResults"`
	StartAt    int          `json:"startAt"`
	Total      int          `json:"total"`
	IsLast     bool         `json:"isLast"`
	Values     []rawTypeRef `json:"values"`
	IssueTypes []rawTypeRef `json:"issueTypes"`
}

func (p issueTypePage) values() []rawTypeRef {
	if len(p.Values) > 0 {
		return p.Values
	}
	return p.IssueTypes
}

func fetchIssueTypePage(
	ctx context.Context, client Doer, path, project string, startAt int,
) (issueTypePage, error) {
	var page issueTypePage

	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   path,
		Query: map[string][]string{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {"100"},
		},
	})
	if err != nil {
		return page, err
	}
	if resp.Status == 400 || resp.Status == 404 {
		// The project is the only thing this route is addressed by, so either
		// status means that project: a 10.3 instance answers an unknown one
		// with 400 and a 404 is equally plausible elsewhere. Saying which cause
		// it is keeps it apart from "no such type": they have different fixes,
		// and a bare "Jira rejected the request" leaves a caller guessing which
		// of three things to check.
		return page, errs.NotFound("UNKNOWN_PROJECT",
			"project %q does not exist, or this credential cannot create in it",
			project).
			WithRequestID(resp.RequestID).
			WithRemedy("check the key with `%s project list`", buildinfo.App)
	}
	if err := transport.Err(resp); err != nil {
		return page, err
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		return page, errs.Remote("MALFORMED_CREATEMETA",
			"%s did not return usable issue types", path).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}
	return page, nil
}

// rawTypeRef is one issue type as either deployment describes it.
type rawTypeRef struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

// ResolveIssueType turns a type name or id into the type itself, refusing an
// unknown or ambiguous one rather than guessing.
func ResolveIssueType(types []IssueType, input string) (IssueType, error) {
	want := strings.TrimSpace(input)
	if want == "" {
		return IssueType{}, errs.Usage("INVALID_ISSUE_TYPE",
			"an issue type cannot be empty")
	}

	for _, t := range types {
		if t.ID == want {
			return t, nil
		}
	}

	var matches []IssueType
	for _, t := range types {
		if strings.EqualFold(t.Name, want) {
			matches = append(matches, t)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		names := make([]string, 0, len(types))
		for _, t := range types {
			names = append(names, t.Name+" ("+t.ID+")")
		}
		e := errs.Usage("UNKNOWN_ISSUE_TYPE",
			"this project has no issue type called %q", input)
		if len(names) > 0 {
			e = e.WithDetail("available: %s", strings.Join(names, ", "))
		}
		return IssueType{}, e
	default:
		ids := make([]string, 0, len(matches))
		for _, t := range matches {
			ids = append(ids, t.ID)
		}
		return IssueType{}, errs.Usage("AMBIGUOUS_ISSUE_TYPE",
			"%q names %d issue types in this project", input, len(matches)).
			WithDetail("ids: %s", strings.Join(ids, ", ")).
			WithRemedy("pass the id of the one you mean")
	}
}

// FetchCreateMeta reads what a project and issue type require at creation.
//
// It is two requests on both deployments: one to map a type name to an id, one
// to page that type's fields. There is no deployment split, which there used to
// be — Data Center once served the whole thing from a single `createmeta` call
// filtered by projectKeys and issuetypeNames, and this took that route.
//
// Atlassian deprecated it in 8.4 and removed it in 9.0, so on any supported
// Data Center it answers "Issue Does Not Exist" — Jira reading `createmeta` as
// an issue key. A live 10.3 instance is where that was found; the fixture that
// was supposed to cover it encoded the old shape and passed happily.
//
// Nothing branches on the deployment now, and nothing branches on the version
// either: 8.x is out of support, so the route that replaced it is the only one
// worth having.
func FetchCreateMeta(
	ctx context.Context, client Doer, info Info, project, issueType string,
) (*CreateMeta, error) {
	types, err := FetchIssueTypes(ctx, client, info, project)
	if err != nil {
		return nil, err
	}
	resolved, err := ResolveIssueType(types, issueType)
	if err != nil {
		return nil, err
	}

	path := info.APIBase() + "/issue/createmeta/" + url.PathEscape(project) +
		"/issuetypes/" + url.PathEscape(resolved.ID)

	var fields []MetaField
	startAt := 0
	for {
		page, err := fetchMetaFieldPage(ctx, client, path, startAt)
		if err != nil {
			return nil, err
		}
		values := page.values()
		for _, v := range values {
			fields = append(fields, v.convert(""))
		}
		if len(values) == 0 || page.IsLast || len(fields) >= page.Total {
			break
		}
		startAt += len(values)
	}

	sortMetaFields(fields)
	return &CreateMeta{
		Project: project, IssueType: resolved.Name, Fields: fields,
	}, nil
}

// metaFieldPage is one page of field metadata: "fields" on Cloud, "values" on
// Data Center, the same split as the issue-type list above.
type metaFieldPage struct {
	Total  int            `json:"total"`
	IsLast bool           `json:"isLast"`
	Values []rawMetaField `json:"values"`
	Fields []rawMetaField `json:"fields"`
}

func (p metaFieldPage) values() []rawMetaField {
	if len(p.Values) > 0 {
		return p.Values
	}
	return p.Fields
}

func fetchMetaFieldPage(
	ctx context.Context, client Doer, path string, startAt int,
) (metaFieldPage, error) {
	var page metaFieldPage

	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   path,
		Query: map[string][]string{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {"100"},
		},
	})
	if err != nil {
		return page, err
	}
	if err := transport.Err(resp); err != nil {
		return page, err
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		return page, errs.Remote("MALFORMED_CREATEMETA",
			"%s did not return usable field metadata", path).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}
	return page, nil
}

// createMetaEntry is what goes on disk.
//
// It carries both the pairing it was requested under and the one the server
// echoed back. The requested pairing is checked on read, so a cache key
// collision is caught rather than answered with another type's fields; the
// resolved pairing is what the caller gets, so a warm cache returns byte for
// byte what a cold one did.
type createMetaEntry struct {
	Project           string      `json:"project"`
	IssueType         string      `json:"issueType"`
	ResolvedProject   string      `json:"resolvedProject"`
	ResolvedIssueType string      `json:"resolvedIssueType"`
	Fields            []MetaField `json:"fields"`
}

// createMetaKey derives the cache filename for one project and issue type.
//
// It is a hash because a cache key becomes a filename and must match a narrow
// format, while a project key is uppercase and an issue type name can hold
// spaces. Sanitizing instead would map "Sub task" and "Sub-task" onto one
// entry, which is a wrong answer rather than a miss — so the pairing is stored
// in the value and verified on read.
func createMetaKey(project, issueType string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + issueType))
	return "createmeta." + hex.EncodeToString(sum[:8])
}

// CreateMeta returns what a project and issue type require, from cache when it
// is fresh.
//
// Unlike transitions this is cached: it changes when an administrator edits a
// screen, not when an issue moves, so a day-old answer is still the answer.
func (m *Metadata) CreateMeta(
	ctx context.Context, project, issueType string,
) (*CreateMeta, error) {
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	ttl := m.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	key := createMetaKey(project, issueType)

	if m.Cache != nil {
		m.Cache.Now = now
	}
	if m.Cache != nil && !m.Refresh {
		var cached createMetaEntry
		ok, err := m.Cache.Get(key, ttl, &cached)
		if err != nil {
			return nil, err
		}
		// The stored pairing has to match, or a hash collision would serve one
		// issue type's required fields as another's.
		if ok && cached.Project == project && cached.IssueType == issueType {
			return &CreateMeta{
				Project:   cached.ResolvedProject,
				IssueType: cached.ResolvedIssueType,
				Fields:    cached.Fields,
			}, nil
		}
	}

	meta, err := FetchCreateMeta(ctx, m.Client, m.Info, project, issueType)
	if err != nil {
		return nil, err
	}
	if m.Cache != nil {
		_ = m.Cache.Put(key, createMetaEntry{
			Project: project, IssueType: issueType,
			ResolvedProject: meta.Project, ResolvedIssueType: meta.IssueType,
			Fields: meta.Fields,
		})
	}
	return meta, nil
}
