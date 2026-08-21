//go:build write

// Writing an arbitrary field is gated on the write tag for the reason every
// other mutating verb is: a reader binary does not contain this file, so it
// cannot set a field even if something asked it to.

package issue

import (
	"context"
	"encoding/json"
	"maps"
	"sort"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
)

// The two flags that reach a field no typed flag owns.
//
// Both are needed and neither is redundant. --field types the value from the
// site's own catalogue, which is what makes `--field 'Story Points'=0.5` refuse
// `0.5x` without a round trip. --field-json sends the value as written, which
// is the only honest answer for a field whose schema type is `any`: five of the
// thirteen custom fields on a stock Data Center report that type, and no amount
// of inference turns it into a shape.
const (
	fieldSetFlag  = "field"
	fieldJSONFlag = "field-json"
)

// fieldWritesKey is where the encoded values resolved during validation are
// left for the request builder.
//
// Resolved in Validate rather than in the body for the reason the read side
// resolves there: the catalogue costs a request, the failure has to be
// reportable, and a value resolved twice is a value that can resolve two ways.
const fieldWritesKey = "issue.fieldwrites"

// fieldSetFlagDecl is --field on a write.
//
// The name collides with --field on `issue get` and `issue list`, where it
// selects a column. That is deliberate. The `=` and the verb both say which is
// meant, no single command carries the two senses, and a caller who knows the
// word for "field" should not have to learn a second one to set it. A bare
// `--field 'Story Points'` here is refused by name rather than read as a column
// request.
func fieldSetFlagDecl() registry.Flag {
	return registry.Flag{
		Name: fieldSetFlag, Type: registry.TypeString, Repeatable: true,
		Usage: "set a field by id or name, as id=value, e.g. " +
			"customfield_10140=5 or 'Story Points=5'; repeat for several, " +
			"and repeat one id to build an array",
	}
}

// fieldJSONFlagDecl is --field-json, the exact escape hatch.
func fieldJSONFlagDecl() registry.Flag {
	return registry.Flag{
		Name: fieldJSONFlag, Type: registry.TypeString, Repeatable: true,
		Usage: "set a field to a JSON value sent as written, as id=<json>, " +
			"e.g. customfield_11350='\"ENG-42\"'; for a field whose type " +
			"this tool will not guess at",
	}
}

// assignment is one `id=value` as the caller wrote it.
type assignment struct {
	// flag is which of the two flags it came from, for the refusal text.
	flag string
	// input is the left side, before resolution: an id or a name.
	input string
	// raw is the right side, verbatim.
	raw string
}

// parseAssignments splits every value of one flag on its first `=`.
//
// The first and not the last, because a value may contain one and a field name
// may not: `--field 'Acceptance Criteria=a=b'` sets the field to `a=b`. Going
// the other way would make the field name `Acceptance Criteria=a`, which is not
// a field, so the refusal would name something the caller never typed.
func parseAssignments(flag string, values []string) ([]assignment, error) {
	out := make([]assignment, 0, len(values))
	for _, v := range values {
		name, raw, found := strings.Cut(v, "=")
		name = strings.TrimSpace(name)
		switch {
		case !found:
			return nil, errs.Usage("FIELD_NOT_KV",
				"--%s takes id=value, and %q has no value", flag, v).
				WithDetail("on a write --%s sets a field; on `issue get` and "+
					"`issue list` it selects a column, and only the second "+
					"takes a bare name", flag).
				// Quoted, because the name that made this fail is very often
				// the one with a space in it, and an unquoted remedy is a
				// second failure for the caller to work out.
				WithRemedy("pass --%s '%s=<value>'", flag, v)
		case name == "":
			return nil, errs.Usage("FIELD_NOT_KV",
				"--%s takes id=value, and %q names no field", flag, v).
				WithRemedy("pass --%s '<id>=<value>'", flag)
		}
		out = append(out, assignment{flag: flag, input: name, raw: raw})
	}
	return out, nil
}

// ownedByFlag maps a field a typed flag already writes to that flag's name.
//
// Two spellings of one write is a silent last-one-wins, and which one wins
// depends on map iteration order, so it is refused rather than resolved. The
// typed flag stays the answer: it validates, it resolves a user, and it knows
// what clearing the field means, none of which --field can do generically.
//
// Keyed by verb because the set is not the same on both, and a refusal naming
// a flag the command does not have is worse than no refusal. `issue edit` has
// no --type and no --project: --project is the persistent context override and
// does not move an issue between projects.
var ownedByFlag = map[string]map[string]string{
	"create": {
		"summary":     "--summary",
		"description": "--description",
		"priority":    "--priority",
		"labels":      "--label",
		"assignee":    "--assignee",
		"parent":      "--parent",
		"issuetype":   "--type",
		"project":     "--project",
	},
	"edit": {
		"summary":     "--summary",
		"description": "--description",
		"priority":    "--priority",
		"labels":      "--label",
		"assignee":    "--assignee",
		"parent":      "--parent",
	},
}

// resolveFieldWrites turns what the caller typed into the `fields` a request
// carries, and leaves it on the invocation.
//
// Every refusal here happens before a request is sent, which is the point: a
// misspelled field, a non-numeric story point, and a type this tool will not
// guess at each cost nothing but the catalogue that was going to be fetched
// anyway.
func resolveFieldWrites(ctx context.Context, inv *registry.Invocation, verb string) error {
	typed, err := parseAssignments(fieldSetFlag, inv.Flags.StringSlice(fieldSetFlag))
	if err != nil {
		return err
	}
	raw, err := parseAssignments(fieldJSONFlag, inv.Flags.StringSlice(fieldJSONFlag))
	if err != nil {
		return err
	}
	if len(typed) == 0 && len(raw) == 0 {
		inv.SetValue(fieldWritesKey, map[string]any(nil))
		return nil
	}
	if inv.Jira == nil {
		return errs.Runtime("NO_SESSION",
			"--"+fieldSetFlag+" cannot be resolved without a connection to Jira")
	}

	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return err
	}
	catalogue, err := meta.Fields(ctx)
	if err != nil {
		return err
	}
	// The deployment, for the one thing that differs between them: what a user
	// value looks like. Connect has already run to fetch the catalogue above,
	// so this costs nothing.
	_, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return err
	}

	fields := map[string]any{}
	// from records which flag set an id, so the second mention of one id can
	// name both flags in the refusal rather than only the one that lost.
	from := map[string]string{}

	// The JSON side first. It is the smaller set, it needs no type, and doing
	// it first means a field named by both flags is refused with the same
	// message whichever order they were typed in.
	owned := ownedByFlag[verb]
	if err := addJSONFields(catalogue, raw, fields, from, owned); err != nil {
		return err
	}
	if err := addTypedFields(catalogue, typed, fields, from, owned, info.Kind); err != nil {
		return err
	}

	inv.SetValue(fieldWritesKey, fields)
	return nil
}

// addJSONFields resolves the --field-json side, whose value is whatever the
// caller wrote as long as it parses.
func addJSONFields(
	catalogue *site.Catalogue, raw []assignment, fields map[string]any,
	from, owned map[string]string,
) error {
	for _, a := range raw {
		f, err := resolveWritableField(catalogue, a, owned)
		if err != nil {
			return err
		}
		if prior, dup := from[f.ID]; dup {
			return duplicateField(f, prior, a.flag)
		}
		var value any
		if err := json.Unmarshal([]byte(a.raw), &value); err != nil {
			return errs.Usage("FIELD_NOT_JSON",
				"the value for %s is not JSON", describeField(f)).
				WithDetail("%s", err.Error()).
				WithRemedy("%s", `quote it as JSON: --`+fieldJSONFlag+
					` `+f.ID+`='"a string"', or =5, or =["a","b"]`)
		}
		fields[f.ID] = value
		from[f.ID] = a.flag
	}
	return nil
}

// addTypedFields resolves the --field side, encoding each value from the type
// the site reported.
//
// A repeated id on an array field appends, which is how every other repeatable
// flag in this tool reads, and is why nothing here splits a value on commas: a
// comma is a character a value may contain, and guessing which one is a
// separator is the class of thing this tool refuses to do.
func addTypedFields(
	catalogue *site.Catalogue, typed []assignment, fields map[string]any,
	from, owned map[string]string, kind site.Kind,
) error {
	grouped := map[string][]string{}
	// order, so the refusal is the same on every run. The body is marshalled
	// from a map and its keys come out sorted either way; what iteration order
	// would decide is *which* of two invalid fields is reported, and a command
	// that names a different one each time is one nobody can act on.
	order := make([]site.Field, 0, len(typed))

	for _, a := range typed {
		f, err := resolveWritableField(catalogue, a, owned)
		if err != nil {
			return err
		}
		if prior, dup := from[f.ID]; dup && prior == fieldJSONFlag {
			return duplicateField(f, prior, a.flag)
		}
		if _, seen := grouped[f.ID]; !seen {
			order = append(order, f)
		}
		grouped[f.ID] = append(grouped[f.ID], a.raw)
	}

	for _, f := range order {
		value, err := encodeField(f, grouped[f.ID], kind)
		if err != nil {
			return err
		}
		fields[f.ID] = value
		from[f.ID] = fieldSetFlag
	}
	return nil
}

// resolveWritableField turns a name or an id into a field, and refuses one a
// typed flag already owns.
func resolveWritableField(
	catalogue *site.Catalogue, a assignment, owned map[string]string,
) (site.Field, error) {
	f, err := catalogue.Resolve(a.input)
	if err != nil {
		return site.Field{}, err
	}
	if flag, taken := owned[f.ID]; taken {
		return site.Field{}, errs.Usage("FIELD_HAS_A_FLAG",
			"%s is written by %s, not by --%s", describeField(f), flag, a.flag).
			WithDetail("naming one field twice has no single right answer, and "+
				"the typed flag validates the value where this one cannot").
			WithRemedy("%s", "pass "+flag+" instead")
	}
	return f, nil
}

// duplicateField refuses one field named twice.
func duplicateField(f site.Field, first, second string) error {
	if first == second {
		return errs.Usage("DUPLICATE_FIELD",
			"%s was set twice by --%s", describeField(f), first).
			WithRemedy("%s", "pass --"+first+" once for this field")
	}
	return errs.Usage("DUPLICATE_FIELD",
		"%s was set by both --%s and --%s", describeField(f), first, second).
		WithRemedy("pass one of them for this field")
}

// describeField names a field the way a refusal should: the id the request
// carries and the name the caller probably typed.
func describeField(f site.Field) string {
	if f.Name == "" || strings.EqualFold(f.Name, f.ID) {
		return f.ID
	}
	return f.ID + " (" + f.Name + ")"
}

// encodeField turns the raw strings for one field into the JSON value Jira
// takes, using the schema type the site itself reported.
//
// The supported set is deliberately the set whose encoding needs no lookup and
// admits no second reading. Everything else is refused by name, pointing at
// --field-json, because a value this tool sent on a guess would be a value
// nobody could predict from the command line.
func encodeField(f site.Field, values []string, kind site.Kind) (any, error) {
	if strings.EqualFold(f.Type, "array") {
		items := make([]any, 0, len(values))
		for _, v := range values {
			item, err := encodeScalar(f, f.Items, v, kind)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	}
	if len(values) > 1 {
		return nil, errs.Usage("DUPLICATE_FIELD",
			"%s takes one value and was given %d", describeField(f), len(values)).
			WithDetail("%s", "repeating --"+fieldSetFlag+" builds an array, and "+
				"this field is not one").
			WithRemedy("%s", "pass --"+fieldSetFlag+" once for this field")
	}
	return encodeScalar(f, f.Type, values[0], kind)
}

// encodeScalar encodes one value of a known schema type. The type is passed in
// rather than read off the field, because an array's elements carry the item
// type and the field carries "array".
func encodeScalar(f site.Field, typeName, v string, kind site.Kind) (any, error) {
	switch strings.ToLower(typeName) {
	case "string", "date", "datetime":
		// Sent as written. A date is Jira's to validate: the accepted forms
		// differ by field and by deployment, and a second opinion here would
		// refuse something the server accepts.
		return v, nil
	case "number":
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil, errs.Usage("FIELD_NOT_A_NUMBER",
				"%s takes a number, and %q is not one", describeField(f), v).
				WithRemedy("pass a number, e.g. --%s %s=5", fieldSetFlag, f.ID)
		}
		return n, nil
	case "option":
		return map[string]any{"value": v}, nil
	case "priority", "resolution", "version", "component", "group", "issuetype",
		"securitylevel":
		// The name, because that is what a person types and what --priority
		// already sends. An id would be equally valid to Jira and unusable to
		// the caller, who has the name in front of them.
		return map[string]any{"name": v}, nil
	default:
		return nil, unsupportedFieldType(f, typeName, kind)
	}
}

// unsupportedFieldType is the refusal that names the flag which does work.
//
// It carries the schema type verbatim, so a caller who reports it has said
// everything needed to decide whether the type belongs in encodeScalar.
func unsupportedFieldType(f site.Field, typeName string, kind site.Kind) error {
	e := errs.Usage("FIELD_TYPE_UNSUPPORTED",
		"%s has type %q, which --%s will not guess at",
		describeField(f), fallbackType(typeName), fieldSetFlag)
	if f.CustomType != "" {
		e = e.WithDetail("Jira calls it %s", f.CustomType)
	}
	example := jsonExample(typeName, kind)
	return e.WithRemedy("send the value as written: --%s %s=%s",
		fieldJSONFlag, f.ID, example)
}

// fallbackType names a type the server did not report, so the message does not
// read as an empty string being the problem.
func fallbackType(typeName string) string {
	if typeName == "" {
		return "unknown"
	}
	return typeName
}

// jsonExample is a plausible --field-json value for a type, so the remedy is
// something to edit rather than something to look up.
func jsonExample(typeName string, kind site.Kind) string {
	switch strings.ToLower(typeName) {
	case "user":
		if kind == site.Cloud {
			return `'{"accountId":"..."}'`
		}
		return `'{"name":"..."}'`
	case "array":
		return `'[...]'`
	default:
		return `'"..."'`
	}
}

// FieldsOwnedByAFlag is every field id a typed flag on one verb writes.
//
// Exported for the test that keeps it honest. The list cannot be derived: only
// the request builder knows that --priority writes `priority`, and a new typed
// flag added without a line here would mean one field with two spellings and a
// silent last-one-wins. TestEveryTypedFlagOwnsItsField builds a request with
// every typed flag set and asserts that each field id it produces is here.
func FieldsOwnedByAFlag(verb string) []string {
	out := make([]string, 0, len(ownedByFlag[verb]))
	for id := range ownedByFlag[verb] {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// fieldWrites returns what validation resolved, for the request builder.
func fieldWrites(inv *registry.Invocation) map[string]any {
	v, _ := inv.Value(fieldWritesKey).(map[string]any)
	return v
}

// mergeFieldWrites adds the resolved fields to a request body.
//
// It runs after the typed flags have filled the map, and cannot collide with
// them: resolveWritableField refuses any field a typed flag owns, so no key
// here is a key already present.
func mergeFieldWrites(fields, extra map[string]any) {
	maps.Copy(fields, extra)
}
