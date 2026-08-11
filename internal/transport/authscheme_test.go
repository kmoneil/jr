package transport_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// TestARefusedSchemeIsAnAuthFailureAndNotAPermissionOne replays a Jira Data
// Center 11 refusing basic authentication.
//
// 11.x disables HTTP Basic by default. The refusal is a 403 with no
// X-Authentication-Denied-Reason, no WWW-Authenticate, and nothing structured
// at all — the message is the only signal — so it landed on the ordinary
// permission branch and came back as FORBIDDEN, exit 6, with the remedy "check
// the project permissions for this account". Three ways wrong: nothing was
// authenticated, it is not about permissions, and the remedy sends the reader
// to a screen that cannot help. Jira's own explanation was printed underneath,
// in the detail, contradicting the headline.
//
// It is the first thing a new user of a current Data Center hits, on their
// first command, because the deployment probe is the first request any run
// makes.
func TestARefusedSchemeIsAnAuthFailureAndNotAPermissionOne(t *testing.T) {
	cassette, err := transport.LoadCassette(
		filepath.Join("testdata", "basic-refused.datacenter.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cassette.Evidence() {
		t.Fatal("basic-refused.datacenter.json is not a recording, so replaying " +
			"it establishes nothing about the API")
	}
	replayer := transport.NewReplayer(cassette)
	client, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	resp, err := client.Do(t.Context(), transport.Request{
		Method: transport.MethodGet, Path: "/rest/api/2/serverInfo",
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}

	err = transport.Err(resp)
	if err == nil {
		t.Fatal("a 403 reported success")
	}
	structured := errs.Coerce(err)

	if structured.Code != "AUTH_SCHEME_REFUSED" {
		t.Errorf("code = %q, want AUTH_SCHEME_REFUSED", structured.Code)
	}
	// Exit 4 and not 6. A caller branches on this: 6 says the account lacks a
	// right, which invites an administrator request, and 4 says the credential
	// is the problem, which is what a personal access token fixes.
	if got := errs.ExitOf(err); got != exitcode.Auth {
		t.Errorf("exit = %v, want %v", got, exitcode.Auth)
	}
	if structured.Retryable {
		t.Error("a refusal that will answer the same way next time was " +
			"published as retryable")
	}
	// The remedy has to name the thing that works. This instance takes a
	// personal access token and nothing else.
	if !strings.Contains(structured.Remedy, "personal access token") {
		t.Errorf("remedy = %q, and it does not name a personal access token",
			structured.Remedy)
	}
	// Jira's own words survive, because the reader needs to know it was the
	// scheme and not their password.
	if !strings.Contains(structured.Detail, "Basic Authentication has been disabled") {
		t.Errorf("detail = %q, and it drops what the server said", structured.Detail)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the probe was never sent: %v", unplayed)
	}
}

// TestAnOrdinaryForbiddenIsStillAPermissionFailure is the other half, and the
// reason the match is narrow.
//
// The signal for a refused scheme is prose in the response body, which is
// fragile by nature. What keeps that acceptable is that both words have to
// appear: an ordinary 403 about project permissions must not be swept into an
// auth failure and sent to a caller as "your credential is wrong".
func TestAnOrdinaryForbiddenIsStillAPermissionFailure(t *testing.T) {
	for _, body := range []string{
		`{"errorMessages":["You do not have permission to view this issue."],"errors":{}}`,
		`{"errorMessages":["Comments are disabled for this project."],"errors":{}}`,
		`{"message":"Basic search is disabled for this user."}`,
	} {
		resp := &transport.Response{
			Status: 403,
			Body:   []byte(body),
			Header: map[string][]string{"Content-Type": {"application/json"}},
		}
		err := transport.Err(resp)
		if err == nil {
			t.Fatalf("a 403 reported success: %s", body)
		}
		if got := errs.ExitOf(err); got != exitcode.Permission {
			t.Errorf("%s\nexit = %v, want %v", body, got, exitcode.Permission)
		}
	}
}
