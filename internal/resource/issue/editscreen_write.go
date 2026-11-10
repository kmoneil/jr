//go:build write

package issue

import (
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// ScreenCheckedByCallerKey, set true on an invocation, leaves the edit screen
// to whoever runs the edit as one step of something larger. issue sequence sets
// it on every step it dry-runs: it reads the screen once for all of its edits,
// and an edit after a leading move has to be held to the screen of the status
// the move leads to, which the step's own dry run cannot see.
const ScreenCheckedByCallerKey = "issue.edit.screen-checked-by-caller"

// ScreenBindsEdits reports whether an edit is held to the issue's edit screen
// before it is sent, which is true on Data Center alone.
//
// Data Center refuses a whole edit that names a field its edit screen does not
// hold, "Field 'customfield_10001' cannot be set. It is not on the appropriate
// screen, or unknown.", and the other fields in the same request are dropped
// with it (issue 291). editmeta lists that screen, so the refusal can be had
// before anything is sent.
//
// Cloud does not refuse that way, or not for every credential. On the sandbox
// on 2026-11-10, with a site administrator's token, PUT /issue set and stored
// three custom fields the issue's editmeta did not list: Jira Software's story
// point estimate, a plain number field, and a datetime. Holding a Cloud edit to
// editmeta would refuse writes Cloud makes, a limit this tool would have
// invented, so on Cloud the screen stays Jira's to enforce. Whether a Cloud
// credential without administration is refused has not been measured: the
// sandbox has one user.
func ScreenBindsEdits(kind site.Kind) bool {
	return kind == site.DataCenter
}

// EditScreen is what an issue's edit screen holds: every field id an edit may
// set, with the name the site gives it. A nil EditScreen has not been read; an
// empty one was read and holds nothing.
type EditScreen map[string]string

// ReadEditScreen reads the issue's edit screen from editmeta. One request.
func ReadEditScreen(ctx context.Context, doer Doer, info site.Info, key string) (EditScreen, error) {
	path := info.APIBase() + "/issue/" + url.PathEscape(key) + "/editmeta"
	resp, err := doer.Do(ctx, transport.Request{Method: transport.MethodGet, Path: path})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}
	var got struct {
		Fields map[string]struct {
			Name string `json:"name"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		return nil, errs.Remote("MALFORMED_RESPONSE",
			"%s answered with something that is not JSON", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	screen := make(EditScreen, len(got.Fields))
	for id, f := range got.Fields {
		screen[id] = f.Name
	}
	return screen, nil
}

// Missing is every id in ids the screen does not hold, in the order given.
func (s EditScreen) Missing(ids []string) []string {
	var off []string
	for _, id := range ids {
		if _, ok := s[id]; !ok {
			off = append(off, id)
		}
	}
	return off
}

// IDs is every field id the screen holds, sorted.
func (s EditScreen) IDs() []string {
	return slices.Sorted(maps.Keys(s))
}

// Describe is every field the screen holds, by id and by the name the site
// gives it where the two differ, sorted by id: customfield_10109 (Sprint).
func (s EditScreen) Describe() []string {
	ids := s.IDs()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, describeField(site.Field{ID: id, Name: s[id]}))
	}
	return out
}

// EditedFields is every field id an edit's requests set or update, sorted.
func EditedFields(reqs []transport.Request) ([]string, error) {
	seen := map[string]bool{}
	for _, r := range reqs {
		var body struct {
			Fields map[string]json.RawMessage `json:"fields"`
			Update map[string]json.RawMessage `json:"update"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			return nil, errs.Runtime("MALFORMED_DRY_RUN", "the edit's dry run is not JSON").Wrap(err)
		}
		for id := range body.Fields {
			seen[id] = true
		}
		for id := range body.Update {
			seen[id] = true
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

// checkEditScreen refuses an edit whose request names a field the issue's edit
// screen does not hold, on the deployment that would refuse it, before it is
// sent or previewed. A dry run approving a request Jira refuses whole is the
// gap issue 291 reported, and the one issue 180 closed for a move's
// resolution.
func checkEditScreen(
	ctx context.Context, inv *registry.Invocation, client *Client,
	key string, req transport.Request,
) error {
	if !ScreenBindsEdits(client.Site.Kind) {
		return nil
	}
	if deferred, _ := inv.Value(ScreenCheckedByCallerKey).(bool); deferred {
		return nil
	}
	ids, err := EditedFields([]transport.Request{req})
	if err != nil {
		return err
	}
	screen, err := ReadEditScreen(ctx, client.Transport, client.Site, key)
	if err != nil {
		return err
	}
	off := screen.Missing(ids)
	if len(off) == 0 {
		return nil
	}
	named := describeIDs(ctx, inv, off)
	holds := "holds no fields at all, so no field can be set on it as it is now"
	if len(screen) > 0 {
		holds = "holds " + strings.Join(screen.Describe(), ", ")
	}
	return errs.Usage("FIELD_NOT_ON_SCREEN",
		"%s is not on the edit screen of %s", strings.Join(named, ", "), key).
		WithDetail("Data Center refuses an edit that names a field its edit screen does "+
			"not hold, and drops every other change in the same request with it. "+
			"The edit screen of %s %s", key, holds).
		WithRemedy("%s", "nothing was sent. Rerun without "+strings.Join(off, ", ")+
			" to apply the rest, or ask a Jira administrator to add it to the edit screen")
}

// describeIDs names each field the way a refusal should, from the catalogue
// when --field or --field-json loaded one, which is the only way a custom
// field reaches an edit. Otherwise the ids are the system fields the typed
// flags set, and an id is already the name a caller knows.
func describeIDs(ctx context.Context, inv *registry.Invocation, ids []string) []string {
	out := slices.Clone(ids)
	if len(fieldWrites(inv)) == 0 || inv.Jira == nil {
		return out
	}
	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return out
	}
	catalogue, err := meta.Fields(ctx)
	if err != nil {
		return out
	}
	for i, id := range ids {
		if f, err := catalogue.Resolve(id); err == nil {
			out[i] = describeField(f)
		}
	}
	return out
}
