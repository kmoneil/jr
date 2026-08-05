package issue

import (
	"encoding/base64"
	"encoding/json"
	"strconv"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/site"
)

// PageToken is an opaque cursor a caller can pass back to resume a result set.
//
// It is opaque for a reason. Cloud pages by cursor and Data Center still pages
// by offset, and the two cannot be unified without lying to somebody. Wrapping
// whichever the server actually uses means the caller never sees an offset —
// so there is no offset to pass to a cursor API, which is precisely how the
// incumbent shipped `--paginate 50:2` silently returning page one.
//
// It carries the deployment it came from, so a token minted against Cloud is
// refused against Data Center rather than being read as an offset of zero.
type PageToken struct {
	// Deployment is the kind of site this token was minted against.
	Deployment site.Kind `json:"d"`
	// Cursor is the server's own token, on a cursor-paginated deployment.
	Cursor string `json:"c,omitempty"`
	// Offset is the row to resume from, when paging by offset.
	Offset int `json:"o,omitempty"`
	// AfterKey is the last key of the previous page, when paging by keyset.
	//
	// A keyset cursor is immune to inserts and deletes: it names a position in
	// the data rather than a count of rows to skip, so an issue created
	// mid-run cannot shift it. Offset pagination has no such guarantee, which
	// is why this is preferred wherever it can be used.
	AfterKey string `json:"k,omitempty"`
}

// EncodePageToken renders a token for the caller. An empty token encodes to the
// empty string, so "no more pages" and "a token meaning nothing" are the same
// value rather than two states a consumer must distinguish.
func EncodePageToken(t PageToken) string {
	if t.Cursor == "" && t.Offset == 0 && t.AfterKey == "" {
		return ""
	}
	data, err := json.Marshal(t)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodePageToken parses a --page-token value.
//
// Every failure is a usage error rather than a silent restart from the
// beginning. A mistyped token that quietly returned page one is the failure
// mode this whole design exists to prevent: the caller would page forever and
// never notice.
func DecodePageToken(encoded string, want site.Kind) (PageToken, error) {
	if encoded == "" {
		return PageToken{Deployment: want}, nil
	}

	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return PageToken{}, errs.Usage("INVALID_PAGE_TOKEN",
			"--page-token is not a token this tool issued").
			WithRemedy("pass a next-page-token from a previous result, or omit it to start over").
			Wrap(err)
	}

	var t PageToken
	if err := json.Unmarshal(data, &t); err != nil {
		return PageToken{}, errs.Usage("INVALID_PAGE_TOKEN",
			"--page-token is not a token this tool issued").
			WithRemedy("pass a next-page-token from a previous result, or omit it to start over").
			Wrap(err)
	}

	if t.Deployment != want {
		// The two deployments page differently, so replaying one's token
		// against the other would resume from somewhere unrelated.
		return PageToken{}, errs.Usage("INVALID_PAGE_TOKEN",
			"--page-token was issued for a %s site and this is %s",
			deploymentName(t.Deployment), deploymentName(want)).
			WithRemedy("re-run the query without --page-token against this site")
	}
	if t.Offset < 0 {
		return PageToken{}, errs.Usage("INVALID_PAGE_TOKEN",
			"--page-token carries a negative offset").
			WithDetail("offset %d", t.Offset)
	}
	if t.AfterKey != "" {
		if _, ok := ParseKey(t.AfterKey); !ok {
			return PageToken{}, errs.Usage("INVALID_PAGE_TOKEN",
				"--page-token carries something that is not an issue key").
				WithDetail("%q", t.AfterKey)
		}
	}
	return t, nil
}

func deploymentName(k site.Kind) string {
	switch k {
	case site.Cloud:
		return "Cloud"
	case site.DataCenter:
		return "Data Center"
	case "":
		return "an unknown"
	default:
		return string(k)
	}
}

// String renders the token for debugging. It is the encoded form, because the
// decoded one invites a caller to construct it by hand.
func (t PageToken) String() string { return EncodePageToken(t) }

// resumeValue returns the query parameter that resumes from this token.
//
// A keyset token has none: it resumes through the query itself, by narrowing
// the JQL, which is what makes it immune to rows shifting underneath it.
func (t PageToken) resumeValue(info site.Info) (param, value string, ok bool) {
	if info.CursorPaginated() {
		if t.Cursor == "" {
			return "", "", false
		}
		return "nextPageToken", t.Cursor, true
	}
	if t.AfterKey != "" || t.Offset == 0 {
		return "", "", false
	}
	return "startAt", strconv.Itoa(t.Offset), true
}
