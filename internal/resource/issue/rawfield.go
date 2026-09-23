package issue

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/transport"
)

// The raw-field read is the third member of the output contract's "writes
// bytes instead of a document" family, and the first that reads. Everything
// here follows the rule that section states: opt-in by flag, an explicit
// --format refused beside it, NO_STDOUT where stdout is not free, the stored
// bytes exactly with nothing added, and a value that does not exist refused
// rather than written as zero bytes.

const rawFieldFlagName = "raw-field"

// rawFieldKey is where validation leaves the resolved field id.
const rawFieldKey = "issue.rawfield"

func rawFieldFlag() registry.Flag {
	return registry.Flag{
		Name: rawFieldFlagName, Type: registry.TypeString,
		Usage: "write one field's stored bytes to stdout with no document " +
			"around them; by id or name, and only a field whose value is text",
	}
}

// rawFieldOwnsStdout reports whether this invocation writes bytes instead of
// a document.
func rawFieldOwnsStdout(inv *registry.Invocation) bool {
	return strings.TrimSpace(inv.Flags.String(rawFieldFlagName)) != ""
}

// validateRawField settles everything about a raw read that can be settled
// before the request: the flags that shape a document this invocation will
// not emit, the second output an explicit --format would name, a stdout that
// is not free, and the field name itself, resolved through the catalogue
// exactly as --field resolves one, so a typo gets the same near misses.
func validateRawField(ctx context.Context, inv *registry.Invocation) error {
	if len(inv.Flags.StringSlice("field")) > 0 || inv.Flags.Bool(noContextFieldsFlag) ||
		inv.Flags.Bool("raw-body") || inv.Flags.Bool(urlFlagName) ||
		inv.Flags.Bool(ageFlagName) || inv.Flags.Bool(withCommentsFlag) {
		return errs.Usage("RAW_FIELD_ALONE",
			"--raw-field writes one value with no document, so a flag that "+
				"shapes the document has nothing to act on").
			WithDetail("refused beside --raw-field: --field, --no-context-fields, " +
				"--raw-body, --url, --age, --with-comments").
			WithRemedy("drop the other flags, or drop --raw-field and read the document")
	}
	if inv.FormatFromFlag {
		return errs.Usage("RAW_AND_FORMAT",
			"--raw-field and --format name two different outputs, and honoring "+
				"both would mean ignoring one").
			WithRemedy("drop one of them; JIRA_FORMAT alone is not grounds for " +
				"this refusal")
	}
	if inv.Stdout == nil {
		// Inside `mcp serve` stdout carries JSON-RPC frames, and bytes written
		// there are a frame the peer cannot parse.
		return errs.Usage("NO_STDOUT",
			"this caller has no stdout to write field bytes to").
			WithRemedy("read the field from the result document instead")
	}

	if inv.Jira == nil {
		return errs.Runtime("NO_SESSION",
			"--raw-field cannot be resolved without a connection to Jira")
	}
	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return err
	}
	catalogue, err := meta.Fields(ctx)
	if err != nil {
		return err
	}
	field, err := catalogue.Resolve(strings.TrimSpace(inv.Flags.String(rawFieldFlagName)))
	if err != nil {
		return err
	}
	inv.SetValue(rawFieldKey, field.ID)
	return nil
}

// writeRawField fetches one field and writes its stored bytes to stdout.
//
// The value goes through one JSON string decode and nothing else, because
// decoding into this tool's issue shape is exactly what the flag exists to
// skip: what Jira holds, byte for byte, is the artifact a read-modify-write
// round trip needs.
func writeRawField(ctx context.Context, client *Client, inv *registry.Invocation) error {
	id, _ := inv.Value(rawFieldKey).(string)
	raw, requestID, err := client.RawField(ctx, inv.Args[0], id)
	if err != nil {
		return err
	}

	trimmed := strings.TrimSpace(string(raw))
	switch {
	case trimmed == "" || trimmed == "null":
		// Zero bytes on stdout could not say unset from empty, which is the
		// same collapse the `set` attribute exists to prevent inside a
		// document, with no attribute available to prevent it here.
		return errs.NotFound("UNSET_FIELD",
			"%s has no value for %s", inv.Args[0], id).
			WithRemedy("the document form marks this case: issue get reports " +
				"the field with set=\"false\"").
			WithRequestID(requestID)
	case trimmed[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			// Unreachable through a decoded response for the reason
			// decodeDescription gives; here because "cannot happen" is a claim.
			return errs.Remote("MALFORMED_BODY",
				"Jira returned a value this tool cannot read").
				WithRequestID(requestID).Wrap(err)
		}
		_, err := io.WriteString(inv.Stdout, s)
		return err
	default:
		return errs.Usage("FIELD_NOT_TEXT",
			"%s on %s is not text, and inventing a byte form for it would be "+
				"an approximation", id, inv.Args[0]).
			WithDetail("the stored value is JSON of shape %s", jsonShape(trimmed)).
			WithRemedy("read it from the document, --format json; a Cloud " +
				"description is a document, which --raw-body emits").
			WithRequestID(requestID)
	}
}

// jsonShape names a JSON value's shape for a refusal, from its first byte.
func jsonShape(trimmed string) string {
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	default:
		return "number"
	}
}

// RawField reads one field's raw JSON value off an issue.
func (c *Client) RawField(
	ctx context.Context, key, id string,
) (json.RawMessage, string, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return nil, "", errs.Usage("INVALID_KEY", "%q is not an issue key", key).
			WithDetail("an issue key looks like ENG-123").
			WithRemedy("pass a key, not an id or a summary")
	}

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   c.Site.APIBase() + "/issue/" + parsed.String(),
		Query:  url.Values{"fields": {id}},
	})
	if err != nil {
		return nil, "", err
	}
	if err := transport.Err(resp); err != nil {
		return nil, "", err
	}

	var envelope struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(resp.Body, &envelope); err != nil {
		return nil, resp.RequestID, errs.Remote("MALFORMED_ISSUE",
			"Jira returned no usable issue for %s", parsed).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	return envelope.Fields[id], resp.RequestID, nil
}
