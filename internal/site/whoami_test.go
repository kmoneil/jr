package site_test

import (
	"testing"

	"github.com/kmoneil/jira-cli/internal/site"
)

// Cloud names the caller by accountId and Data Center by name and key. A
// fixture carrying both would resolve whichever field the code read first and
// hide which one that was, which is the trap the directory constants in
// users_test.go record having fallen into once already.
const (
	cloudMyself = `{"accountId":"712020:8f3a","displayName":"Ada Lovelace",
		"emailAddress":"ada@example.invalid","active":true}`
	dcMyself = `{"name":"ada","key":"ada","displayName":"Ada Lovelace",
		"emailAddress":"ada@example.invalid","active":true}`
)

// TestWhoamiUsesTheDeploymentsAPIVersion covers the endpoint that proves a
// credential works, on the deployment nothing here drove.
//
// Whoami had no direct test in this package at all — it was reached only
// through the resource packages, all of which stub the deployment as Cloud. So
// a mutation of Info.APIBase's Data Center arm went unnoticed by every test
// that touches /myself, while createmeta, fields, the probe, and user search
// all caught it.
//
// It matters more here than the path count suggests. This is the only call
// that distinguishes "the site is really Jira" from "the token is good", so a
// Data Center login that asked the wrong version would fail verification with
// a 404 about an endpoint rather than anything about the credential.
func TestWhoamiUsesTheDeploymentsAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		kind site.Kind
		body string
		want string
	}{
		{site.Cloud, cloudMyself, "/rest/api/3/myself"},
		{site.DataCenter, dcMyself, "/rest/api/2/myself"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			doer := &pathRecordingDoer{stubDoer: stubDoer{body: tc.body}}
			if _, err := site.Whoami(
				t.Context(), doer, site.Info{Kind: tc.kind},
			); err != nil {
				t.Fatalf("whoami: %v", err)
			}
			if doer.path != tc.want {
				t.Errorf("asked for %q, want %q", doer.path, tc.want)
			}
		})
	}
}

// TestWhoamiReportsTheIdThisDeploymentIdentifiesBy is the other half, and the
// reason the fixtures are separate.
//
// The id falls back accountId → name → key, which reads as defensive and is
// really the deployment split written as a chain: Cloud sends the first and
// never the others, Data Center sends the last two and never the first. A
// caller stores this and sends it back as an assignee, so getting it wrong
// names the wrong person or nobody.
func TestWhoamiReportsTheIdThisDeploymentIdentifiesBy(t *testing.T) {
	for _, tc := range []struct {
		kind site.Kind
		body string
		want string
	}{
		{site.Cloud, cloudMyself, "712020:8f3a"},
		{site.DataCenter, dcMyself, "ada"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			got, err := site.Whoami(t.Context(), &stubDoer{body: tc.body},
				site.Info{Kind: tc.kind})
			if err != nil {
				t.Fatalf("whoami: %v", err)
			}
			if got.ID != tc.want {
				t.Errorf("ID = %q, want %q", got.ID, tc.want)
			}
			if got.Display != "Ada Lovelace" {
				t.Errorf("Display = %q, want the display name", got.Display)
			}
		})
	}
}

// TestWhoamiFallsBackToTheKey covers the third link in that chain, which no
// deployment reaches on its own.
//
// A Data Center account with no `name` but a `key` is what an older instance
// answers with, and an empty id there is not an error anywhere — it is an
// assignee nobody, sent as an empty string.
func TestWhoamiFallsBackToTheKey(t *testing.T) {
	const keyOnly = `{"key":"ada","displayName":"Ada Lovelace","active":true}`

	got, err := site.Whoami(t.Context(), &stubDoer{body: keyOnly},
		site.Info{Kind: site.DataCenter})
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if got.ID != "ada" {
		t.Errorf("ID = %q, want the key when there is no name", got.ID)
	}
}
