package site

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// User is one account.
//
// ID is an accountId on Cloud and a username on Data Center. The two are not
// interchangeable, which is why the field is named for what it is rather than
// for either one.
type User struct {
	ID      string
	Display string
	Email   string
	Active  bool
	// TimeZone is the IANA zone on the user's profile, where the server
	// discloses it. A search does not; a lookup by id usually does.
	TimeZone string
	// Kind distinguishes a person from an application or a customer account,
	// where the server says. Assigning an issue to an app account is a mistake
	// worth being able to see before making.
	Kind string
}

// rawUser is the response shape, which both deployments share.
type rawUser struct {
	AccountID   string `json:"accountId"`
	AccountType string `json:"accountType"`
	Name        string `json:"name"`
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
	Email       string `json:"emailAddress"`
	Active      bool   `json:"active"`
	TimeZone    string `json:"timeZone"`
}

func (r rawUser) convert() User {
	id := r.AccountID
	if id == "" {
		id = r.Name
	}
	if id == "" {
		id = r.Key
	}
	return User{
		ID: id, Display: r.DisplayName, Email: r.Email,
		Active: r.Active, Kind: r.AccountType, TimeZone: r.TimeZone,
	}
}

// UserPage is one page of search results plus whether the directory held more.
//
// Every other listing in this tool pages to exhaustion and then compares what
// it holds against what was asked for, so it can always say whether the set is
// whole. This endpoint cannot be paged that way here, so the answer has to come
// out of the one request — which is why the bound and the verdict travel
// together rather than the caller inferring one from the other.
type UserPage struct {
	Users []User
	// Complete is false when the directory held more than the bound allowed.
	//
	// It is a field rather than something a caller works out from len(Users),
	// because len(Users) cannot answer it: the bound and the test for the bound
	// would be the same number, and the comparison is then unreachable. That is
	// exactly how `user list` came to report every truncated search as
	// exhaustive.
	Complete bool
}

// SearchUsers finds accounts matching a query, up to limit.
//
// The parameter name differs by deployment: Cloud takes `query` and matches
// display name and email, Data Center takes `username` and matches rather more
// loosely. Sending the wrong one is not an error — it is an empty result,
// which is why the split lives here rather than in each caller.
//
// It asks the server for one more row than the caller wanted. `/user/search`
// answers with a bare array — no total, no isLast, on either deployment — so a
// full page is the only evidence there is that more exist, and asking for
// exactly the bound makes a truncated result indistinguishable from a whole
// one. The extra row is a probe: it is never converted, never counted, and
// never emitted.
//
// The alternative was to send the bound and call a page that came back full
// incomplete. That is the guess `attachComments` deliberately refused for the
// comment thread — it reports every result whose size happens to equal the
// limit as partial, forever. A probe row costs nothing and is exact.
func SearchUsers(
	ctx context.Context, client Doer, info Info, query string, limit int,
) (UserPage, error) {
	param := "username"
	if info.Kind == Cloud {
		param = "query"
	}
	path := info.APIBase() + "/user/search"

	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   path,
		Query: url.Values{
			param:        {query},
			"maxResults": {strconv.Itoa(limit + 1)},
		},
	})
	if err != nil {
		return UserPage{}, err
	}
	if err := transport.Err(resp); err != nil {
		return UserPage{}, err
	}

	var raw []rawUser
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return UserPage{}, errs.Remote("MALFORMED_USERS",
			"%s did not return usable users", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	// The probe row came back, so the directory holds more than was asked for.
	// Decided on the raw count, before the id filter below: an entry dropped
	// for having no id is still an entry the server had, so counting after the
	// filter would report a full page as whole.
	more := len(raw) > limit

	out := make([]User, 0, len(raw))
	for _, r := range raw {
		u := r.convert()
		if u.ID == "" {
			// An entry this deployment gave no identifier to is not one a
			// caller can name back, so offering it as a candidate would be
			// offering something that cannot be passed.
			continue
		}
		out = append(out, u)
	}
	// Trimmed here, in the order the server sent, rather than after the sort.
	// Sorting first would let the probe row displace a real one — it would land
	// wherever its display name falls and push the last row out — so which
	// users a caller sees would depend on a row fetched only to be discarded.
	if len(out) > limit {
		out = out[:limit]
	}
	// Ordered so two runs agree; neither endpoint promises one.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Display != out[j].Display {
			return out[i].Display < out[j].Display
		}
		return out[i].ID < out[j].ID
	})
	return UserPage{Users: out, Complete: !more}, nil
}

// FetchUser looks one account up by the id this deployment identifies it with.
func FetchUser(ctx context.Context, client Doer, info Info, id string) (User, error) {
	param := "username"
	if info.Kind == Cloud {
		param = "accountId"
	}
	path := info.APIBase() + "/user"

	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   path,
		Query:  url.Values{param: {id}},
	})
	if err != nil {
		return User{}, err
	}
	if err := transport.Err(resp); err != nil {
		return User{}, err
	}

	var raw rawUser
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return User{}, errs.Remote("MALFORMED_USER",
			"%s did not return a usable user", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	converted := raw.convert()
	if converted.ID == "" {
		return User{}, errs.NotFound("NO_SUCH_USER", "no user %q on this site", id)
	}
	return converted, nil
}

// userSearchLimit bounds the candidate list. It is a list a person reads in an
// error message, not a result set, so it is small on purpose.
const userSearchLimit = 20

// ResolveUser turns what a caller typed into the id this deployment wants.
//
// An accountId is a hundred-odd opaque characters and nobody has one to hand,
// so a caller types a name — and a name sent where an id belongs is a 400 that
// names nothing. This is the same job the field catalogue does for `--field`,
// and it follows the same rule: an exact match resolves, everything else is
// refused with what was found rather than guessed at.
//
// "Exact" means the id, the email address, or the display name, compared
// case-insensitively. A partial match is not resolved even when the server
// returns exactly one, because the set it was drawn from changes as people
// join: `--assignee ada` meaning one person today and refusing tomorrow is
// tolerable, and meaning a different person tomorrow is not. The near miss is
// reported so the caller can copy the name that would have worked.
func ResolveUser(ctx context.Context, client Doer, info Info, input string) (User, error) {
	want := strings.TrimSpace(input)
	if want == "" {
		return User{}, errs.Usage("INVALID_USER", "a user cannot be empty")
	}

	// Completeness is deliberately ignored here. This is a resolution, not a
	// listing: an exact match either is in the candidate set or the input is
	// refused with what was found, and both answers are honest whether or not
	// the directory held more. `user list` is where the caller is owed the
	// verdict, because there the candidates are the result.
	page, err := SearchUsers(ctx, client, info, want, userSearchLimit)
	if err != nil {
		return User{}, err
	}
	found := page.Users

	// Each class in turn, so a display name that happens to equal somebody
	// else's email does not make an exact email match ambiguous.
	for _, match := range []func(User) bool{
		func(u User) bool { return strings.EqualFold(u.ID, want) },
		func(u User) bool { return u.Email != "" && strings.EqualFold(u.Email, want) },
		func(u User) bool { return strings.EqualFold(u.Display, want) },
	} {
		var exact []User
		for _, u := range found {
			if match(u) {
				exact = append(exact, u)
			}
		}
		switch len(exact) {
		case 0:
			continue
		case 1:
			return exact[0], nil
		default:
			// Two people can share a display name, and they are different
			// people. Picking one assigns somebody else's work to them.
			return User{}, errs.Usage("AMBIGUOUS_USER",
				"%q names %d users on this site", input, len(exact)).
				WithDetail("%s", describeUsers(exact)).
				WithRemedy("pass the id of the one you mean")
		}
	}

	if len(found) == 0 {
		// Cloud's search matches a display name and an email address and not
		// an accountId, so a caller who already has an id gets nothing back
		// and is asked about directly. It costs a second request only for the
		// spelling that used to be the only one that worked.
		if u, err := FetchUser(ctx, client, info, want); err == nil {
			return u, nil
		}
	}
	return User{}, unknownUser(input, found)
}

func unknownUser(input string, found []User) error {
	e := errs.Usage("UNKNOWN_USER", "no user on this site is called %q", input)

	near := plausible(input, found)
	if len(near) == 0 {
		return e.WithRemedy("search for one with `%s user list <text>`", buildinfo.App)
	}
	return e.
		WithDetail("closest: %s", describeUsers(near)).
		WithRemedy("pass one of these exactly, by name or by id")
}

// plausible drops the results that are not suggestions.
//
// Cloud answers a query it cannot match with whatever it has, and offering
// "Atlassian Assist" as the closest thing to "Nobody At All" is worse than
// offering nothing: it reads as a near miss and is not one. A candidate has to
// share a word with what was typed, and a short word is not enough to go on.
func plausible(input string, found []User) []User {
	want := strings.ToLower(strings.TrimSpace(input))

	var words []string
	for _, w := range strings.FieldsFunc(want, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '.' || r == '@' || r == '-' || r == '_'
	}) {
		if len(w) >= 4 {
			words = append(words, w)
		}
	}

	var out []User
	for _, u := range found {
		haystack := strings.ToLower(u.Display + " " + u.Email + " " + u.ID)
		if strings.Contains(haystack, want) {
			out = append(out, u)
			continue
		}
		for _, w := range words {
			if strings.Contains(haystack, w) {
				out = append(out, u)
				break
			}
		}
	}
	return out
}

// describeUsers lists candidates for an error message. Both the name and the
// id are there because the name is what a person recognises and the id is what
// they have to pass to make the ambiguity go away.
func describeUsers(users []User) string {
	const most = 5

	parts := make([]string, 0, min(len(users), most))
	for _, u := range users[:min(len(users), most)] {
		part := u.Display + " (" + u.ID + ")"
		if !u.Active {
			part += " [inactive]"
		}
		// An app account is a user Jira will happily accept and a person will
		// never look at, so it is worth seeing before choosing one.
		if u.Kind != "" && !strings.EqualFold(u.Kind, "atlassian") {
			part += " [" + u.Kind + "]"
		}
		parts = append(parts, part)
	}
	if len(users) > most {
		parts = append(parts, "and "+strconv.Itoa(len(users)-most)+" more")
	}
	return strings.Join(parts, ", ")
}

// ResolveUser resolves against the site this metadata belongs to.
//
// It is not cached, and that is deliberate: the field catalogue is one
// immutable snapshot of a whole site, and this is one search per input. A
// cached answer would also outlive somebody leaving, which is exactly the
// account a caller must not still be assigning work to.
func (m *Metadata) ResolveUser(ctx context.Context, input string) (User, error) {
	return ResolveUser(ctx, m.Client, m.Info, input)
}
