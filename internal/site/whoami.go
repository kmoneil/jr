package site

import (
	"context"
	"encoding/json"
	"time"

	// The zone database, compiled in. Account.Location resolves an IANA name,
	// and time.LoadLocation otherwise reads the host's copy: /usr/share/zoneinfo
	// on a Unix, and on Windows a file that is not there, so a released binary
	// would refuse a date on the platform with no system zoneinfo at all. A
	// scratch container is the same story on Linux. About 450 KB against 4 MB of
	// headroom under READER_MAX_BYTES, and `make size` is what says so.
	_ "time/tzdata"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/transport"
)

// Account is who a credential authenticates as.
type Account struct {
	// ID is an accountId on Cloud and a username on Data Center. The two are
	// not interchangeable, so the field is named for what it is.
	ID      string
	Name    string
	Display string
	Email   string
	Active  bool
	// TimeZone is the IANA zone on the account's own profile, e.g.
	// "America/Chicago", and is empty where the server does not say.
	//
	// It matters far more than a profile setting sounds like it should: Jira
	// evaluates every relative date and every startOf/endOf function in this
	// zone, not in UTC and not in the caller's. Nothing else in this tool can
	// tell a caller which clock their query was answered on.
	TimeZone string
}

// Location resolves the account's zone into one dates can be read in.
//
// It is an error rather than a fallback to UTC. A caller reaching for this is
// about to turn a wall clock into an instant, and doing that in the wrong zone
// is wrong by the offset with nothing in the output to say so — which is the
// whole defect this exists to close. An account whose zone cannot be resolved
// gets a refusal naming the zone.
//
// Both deployments send one: recorded Cloud says "America/Chicago" and recorded
// Data Center says "Etc/UTC", so the empty case is a server that broke its own
// contract rather than a routine one.
func (a Account) Location() (*time.Location, error) {
	// Both are exit 2 rather than 9. A missing field is malformed data, which
	// is what 9 means, but 9 is also `retryable`, and the second attempt reads
	// the same profile and fails the same way. What the caller has is an
	// unsupported combination — a form of date this site cannot support — and
	// a remedy that works on the next invocation.
	if a.TimeZone == "" {
		return nil, errs.Usage("NO_ACCOUNT_TIMEZONE",
			"this site did not report the account's timezone").
			WithDetail("a date is evaluated by Jira in the account's zone, " +
				"so reading one here without it would be wrong by the offset").
			WithRemedy("use a relative offset like -7d, which names an instant " +
				"and needs no zone")
	}
	loc, err := time.LoadLocation(a.TimeZone)
	if err != nil {
		return nil, errs.Usage("UNKNOWN_ACCOUNT_TIMEZONE",
			"the account's timezone %q is not a zone this build can read", a.TimeZone).
			WithRemedy("use a relative offset like -7d, which names an instant " +
				"and needs no zone").
			Wrap(err)
	}
	return loc, nil
}

// rawAccount covers both deployments.
type rawAccount struct {
	AccountID   string `json:"accountId"`
	Name        string `json:"name"`
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
	Email       string `json:"emailAddress"`
	Active      bool   `json:"active"`
	TimeZone    string `json:"timeZone"`
}

// Whoami asks the site who the caller is.
//
// It is the only check that proves a credential works. A deployment probe
// answers anonymously on most instances, so it establishes that the site is
// really Jira and nothing about whether the token is good — which is exactly
// the gap that lets a login succeed and every later command fail.
func Whoami(ctx context.Context, client Doer, info Info) (Account, error) {
	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   info.APIBase() + "/myself",
	})
	if err != nil {
		return Account{}, err
	}
	if err := transport.Err(resp); err != nil {
		return Account{}, err
	}

	var raw rawAccount
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return Account{}, errs.Remote("MALFORMED_ACCOUNT",
			"%s did not return a usable account", info.APIBase()+"/myself").
			WithRequestID(resp.RequestID).Wrap(err)
	}

	id := raw.AccountID
	if id == "" {
		id = raw.Name
	}
	if id == "" {
		id = raw.Key
	}
	return Account{
		ID:       id,
		Name:     raw.Name,
		Display:  raw.DisplayName,
		Email:    raw.Email,
		Active:   raw.Active,
		TimeZone: raw.TimeZone,
	}, nil
}
