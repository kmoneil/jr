package site

import (
	"context"
	"encoding/json"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/transport"
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
