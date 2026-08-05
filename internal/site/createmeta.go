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
// It exists because Cloud's createmeta is addressed by issue type *id* while a
// caller types a name. Data Center takes the name directly, so this is only
// reached on Cloud.
func FetchIssueTypes(
	ctx context.Context, client Doer, info Info, project string,
) ([]IssueType, error) {
	path := info.APIBase() + "/issue/createmeta/" + url.PathEscape(project) + "/issuetypes"

	var out []IssueType
	startAt := 0
	for {
		resp, err := client.Do(ctx, transport.Request{
			Method: transport.MethodGet,
			Path:   path,
			Query: map[string][]string{
				"startAt":    {strconv.Itoa(startAt)},
				"maxResults": {"100"},
			},
		})
		if err != nil {
			return nil, err
		}
		if err := transport.Err(resp); err != nil {
			return nil, err
		}

		var page struct {
			MaxResults int  `json:"maxResults"`
			StartAt    int  `json:"startAt"`
			Total      int  `json:"total"`
			IsLast     bool `json:"isLast"`
			Values     []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Subtask bool   `json:"subtask"`
			} `json:"values"`
		}
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, errs.Remote("MALFORMED_CREATEMETA",
				"%s did not return usable issue types", path).
				WithRequestID(resp.RequestID).
				Wrap(err)
		}

		for _, v := range page.Values {
			out = append(out, IssueType{ID: v.ID, Name: v.Name, Subtask: v.Subtask})
		}

		// A page that added nothing ends the loop whatever the server claims,
		// so a server that reports isLast=false forever cannot spin here.
		if len(page.Values) == 0 || page.IsLast || len(out) >= page.Total {
			break
		}
		startAt += len(page.Values)
	}

	sort.Slice(out, func(i, j int) bool { return lessID(out[i].ID, out[j].ID) })
	return out, nil
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
// The two deployments diverge here more than anywhere else. Data Center serves
// the whole thing from one request with an expand; Cloud removed that endpoint
// and replaced it with a pair — one to map a type name to an id, one to page
// the fields. The split is behind this function, so a caller sees one shape.
func FetchCreateMeta(
	ctx context.Context, client Doer, info Info, project, issueType string,
) (*CreateMeta, error) {
	if info.Kind == Cloud {
		return fetchCreateMetaCloud(ctx, client, info, project, issueType)
	}
	return fetchCreateMetaDataCenter(ctx, client, info, project, issueType)
}

func fetchCreateMetaCloud(
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
		resp, err := client.Do(ctx, transport.Request{
			Method: transport.MethodGet,
			Path:   path,
			Query: map[string][]string{
				"startAt":    {strconv.Itoa(startAt)},
				"maxResults": {"100"},
			},
		})
		if err != nil {
			return nil, err
		}
		if err := transport.Err(resp); err != nil {
			return nil, err
		}

		var page struct {
			Total  int            `json:"total"`
			IsLast bool           `json:"isLast"`
			Values []rawMetaField `json:"values"`
		}
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, errs.Remote("MALFORMED_CREATEMETA",
				"%s did not return usable field metadata", path).
				WithRequestID(resp.RequestID).
				Wrap(err)
		}

		for _, v := range page.Values {
			fields = append(fields, v.convert(""))
		}
		if len(page.Values) == 0 || page.IsLast || len(fields) >= page.Total {
			break
		}
		startAt += len(page.Values)
	}

	sortMetaFields(fields)
	return &CreateMeta{
		Project: project, IssueType: resolved.Name, Fields: fields,
	}, nil
}

func fetchCreateMetaDataCenter(
	ctx context.Context, client Doer, info Info, project, issueType string,
) (*CreateMeta, error) {
	path := info.APIBase() + "/issue/createmeta"

	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   path,
		Query: map[string][]string{
			"projectKeys":    {project},
			"issuetypeNames": {issueType},
			"expand":         {"projects.issuetypes.fields"},
		},
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw struct {
		Projects []struct {
			Key        string `json:"key"`
			IssueTypes []struct {
				ID     string                     `json:"id"`
				Name   string                     `json:"name"`
				Fields map[string]json.RawMessage `json:"fields"`
			} `json:"issuetypes"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, errs.Remote("MALFORMED_CREATEMETA",
			"%s did not return usable field metadata", path).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}

	// A filter that matched nothing comes back as an empty projects array with
	// a 200, so an unknown project and an unknown type both look like success.
	// Asking again without the type filter separates them, and gives the same
	// refusal Cloud gives — where the type name is resolved before the fields
	// are asked for, so a typo comes back with the alternatives.
	if len(raw.Projects) == 0 || len(raw.Projects[0].IssueTypes) == 0 {
		return nil, dataCenterNoMatch(ctx, client, info, project, issueType, resp.RequestID)
	}

	found := raw.Projects[0].IssueTypes[0]
	fields, err := decodeMetaFields(found.Fields, path, resp.RequestID)
	if err != nil {
		return nil, err
	}
	return &CreateMeta{
		Project: raw.Projects[0].Key, IssueType: found.Name, Fields: fields,
	}, nil
}

// dataCenterNoMatch explains an empty createmeta result.
//
// It asks once more without the issue type filter. A project that offers types
// means the type name was wrong, and the alternatives are worth printing; a
// project that offers none means the project is wrong or unreadable. Reporting
// both as one condition would leave a caller guessing which of three things to
// check — the exact opaqueness §5.2 exists to remove.
//
// A failure of the second request is not propagated. The first request already
// established that the answer is "nothing matched", and a diagnostic lookup
// that fails must not replace that with an unrelated error.
func dataCenterNoMatch(
	ctx context.Context, client Doer, info Info, project, issueType, requestID string,
) error {
	types, err := dataCenterIssueTypes(ctx, client, info, project)
	if err != nil || len(types) == 0 {
		return errs.NotFound("UNKNOWN_PROJECT",
			"project %s has no issue types this credential can create", project).
			WithRequestID(requestID).
			WithDetail("the project does not exist, or creating in it is not permitted").
			WithRemedy("check the key with `jr context show`")
	}

	names := make([]string, 0, len(types))
	for _, t := range types {
		names = append(names, t.Name+" ("+t.ID+")")
	}
	return errs.NotFound("UNKNOWN_ISSUE_TYPE",
		"project %s has no issue type called %q", project, issueType).
		WithRequestID(requestID).
		WithDetail("available: %s", strings.Join(names, ", ")).
		WithRemedy("pass one of the above, by name or by id")
}

// dataCenterIssueTypes lists what a project offers, from the same endpoint
// without the type filter.
func dataCenterIssueTypes(
	ctx context.Context, client Doer, info Info, project string,
) ([]IssueType, error) {
	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   info.APIBase() + "/issue/createmeta",
		Query: map[string][]string{
			"projectKeys": {project},
			"expand":      {"projects.issuetypes"},
		},
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw struct {
		Projects []struct {
			IssueTypes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"issuetypes"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, err
	}
	if len(raw.Projects) == 0 {
		return nil, nil
	}

	out := make([]IssueType, 0, len(raw.Projects[0].IssueTypes))
	for _, t := range raw.Projects[0].IssueTypes {
		out = append(out, IssueType{ID: t.ID, Name: t.Name})
	}
	sort.Slice(out, func(i, j int) bool { return lessID(out[i].ID, out[j].ID) })
	return out, nil
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
