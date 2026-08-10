package transport_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// replayClient builds a Client answering from a cassette and touching no
// network at all.
func replayClient(t *testing.T, deployment transport.Deployment) (
	*transport.Client, *transport.Replayer,
) {
	t.Helper()

	path := filepath.Join("testdata", "serverinfo."+string(deployment)+".json")
	cassette, err := transport.LoadCassette(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	if cassette.Deployment != deployment {
		t.Fatalf("%s records deployment %q, want %q", path, cassette.Deployment, deployment)
	}

	replayer := transport.NewReplayer(cassette)
	c, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: replayer.Client(),
		Retries:    -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c, replayer
}

// The requests below are the ones `jr` really sends, because the Cloud cassette
// is a recording and a recording answers what it was asked. They were synthetic
// before — a bare issue GET and a create with an empty summary — and neither is
// a request this tool makes: `issue get` always names its fields, and an empty
// summary is refused here before it reaches Jira, so that 400 could never have
// been recorded at all.
//
// Coupling this to the default field set is deliberate. If that set changes the
// conversation genuinely changes, and a contract test whose request no longer
// matches the client's is asserting something nobody sends.
const recordedGetFields = "summary,status,assignee,reporter,priority,issuetype,project,created,updated,labels,description,resolution,parent,components,fixVersions"

// recordedCreateBody is a 300-character summary against Jira's 255 limit, which
// is the shortest real request that produces a field error naming `summary`.
const recordedCreateBody = `{"fields":{"issuetype":{"name":"Task"},"project":{"key":"ENG"},"summary":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}`

// apiBase is the REST prefix each deployment answers on. Cloud has moved to v3
// for most endpoints; Data Center is still v2. A fixture recorded against one
// proves nothing about the other, which is why both are replayed.
func apiBase(d transport.Deployment) string {
	if d == transport.Cloud {
		return "/rest/api/3"
	}
	return "/rest/api/2"
}

// TestContractAgainstBothDeployments replays the same conversation against a
// Cloud and a Data Center recording.
//
// These are the tests that would have caught the incumbent's failures: all of
// them live at the seam between the client and a real Jira, where a
// pure-function unit test cannot see.
func TestContractAgainstBothDeployments(t *testing.T) {
	for _, deployment := range transport.Deployments() {
		t.Run(string(deployment), func(t *testing.T) {
			c, replayer := replayClient(t, deployment)
			base := apiBase(deployment)

			t.Run("serverInfo", func(t *testing.T) {
				resp, err := c.Do(t.Context(), transport.Request{
					Method: http.MethodGet, Path: "/rest/api/2/serverInfo",
				})
				if err != nil {
					t.Fatalf("do: %v", err)
				}
				if err := transport.Err(resp); err != nil {
					t.Fatalf("err: %v", err)
				}

				var info struct {
					DeploymentType string `json:"deploymentType"`
					Version        string `json:"version"`
				}
				if err := json.Unmarshal(resp.Body, &info); err != nil {
					t.Fatalf("decode: %v", err)
				}
				// Cloud reports "Cloud"; Data Center reports "Server". This is
				// the value §5.3 probes for rather than freezing into config.
				wantType := "Cloud"
				if deployment == transport.DataCenter {
					wantType = "Server"
				}
				if info.DeploymentType != wantType {
					t.Errorf("deploymentType = %q, want %q", info.DeploymentType, wantType)
				}
				if info.Version == "" {
					t.Error("no version reported")
				}
			})

			t.Run("myself", func(t *testing.T) {
				resp, err := c.Do(t.Context(), transport.Request{
					Method: http.MethodGet, Path: base + "/myself",
				})
				if err != nil {
					t.Fatalf("do: %v", err)
				}
				if err := transport.Err(resp); err != nil {
					t.Fatalf("err: %v", err)
				}
				if !strings.Contains(string(resp.Body), "Ada Lovelace") {
					t.Errorf("body = %s", resp.Body)
				}
			})

			t.Run("missing issue is exit 5", func(t *testing.T) {
				resp, err := c.Do(t.Context(), transport.Request{
					Method: http.MethodGet, Path: base + "/issue/ENG-9999",
					Query: url.Values{"fields": {recordedGetFields}},
				})
				if err != nil {
					t.Fatalf("do: %v", err)
				}
				err = transport.Err(resp)
				if err == nil {
					t.Fatal("a 404 reported success")
				}
				if errs.ExitOf(err) != exitcode.NotFound {
					t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.NotFound)
				}
				// Both deployments word this differently; both must reach the
				// detail so the caller sees what Jira said.
				if errs.Coerce(err).Detail == "" {
					t.Error("Jira's explanation was dropped")
				}
			})

			t.Run("field error is exit 2 and names the field", func(t *testing.T) {
				resp, err := c.Do(t.Context(), transport.Request{
					Method: http.MethodPost,
					Path:   base + "/issue",
					Body:   []byte(recordedCreateBody),
				})
				if err != nil {
					t.Fatalf("do: %v", err)
				}
				err = transport.Err(resp)
				if err == nil {
					t.Fatal("a 400 reported success")
				}
				if errs.ExitOf(err) != exitcode.Usage {
					t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
				}
				if detail := errs.Coerce(err).Detail; !strings.Contains(detail, "summary") {
					t.Errorf("detail %q does not name the offending field", detail)
				}
			})

			// A cassette carrying responses nothing asked for is usually a test
			// that stopped covering what it claims to.
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("fixture has interactions this test never exercised: %v", unplayed)
			}
		})
	}
}

// TestReplayerRefusesUnknownRequests is what keeps a fixture-backed test
// honest. Falling through to the network would be green in CI, where there are
// no credentials, while quietly exercising nothing.
func TestReplayerRefusesUnknownRequests(t *testing.T) {
	c, replayer := replayClient(t, transport.Cloud)

	_, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/rest/api/3/never-recorded",
	})
	if err == nil {
		t.Fatal("an unrecorded request was answered")
	}
	if !strings.Contains(err.Error(), "recorded interaction") {
		t.Errorf("unhelpful error: %v", err)
	}
	if got := replayer.Unmatched(); len(got) != 1 {
		t.Errorf("Unmatched = %v, want the one miss", got)
	}
}

// TestQueryOrderDoesNotAffectMatching stops a fixture depending on the order a
// caller happened to build its parameters in.
func TestQueryOrderDoesNotAffectMatching(t *testing.T) {
	cassette := &transport.Cassette{
		Deployment: transport.Cloud,
		Interactions: []transport.Interaction{{
			Request: transport.RecordedRequest{
				Method: http.MethodGet,
				Path:   "/rest/api/3/search/jql",
				Query:  "maxResults=50&jql=project+%3D+ENG",
			},
			Response: transport.RecordedResponse{Status: 200, Body: `{"issues":[]}`},
		}},
	}
	c, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: transport.NewReplayer(cassette).Client(),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	resp, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet,
		Path:   "/rest/api/3/search/jql",
		// Built in the other order.
		Query: url.Values{"jql": {"project = ENG"}, "maxResults": {"50"}},
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d", resp.Status)
	}
}

// TestJSONFieldOrderDoesNotAffectMatching is the same idea for bodies: a change
// in field order changes nothing about the request.
func TestJSONFieldOrderDoesNotAffectMatching(t *testing.T) {
	cassette := &transport.Cassette{
		Deployment: transport.Cloud,
		Interactions: []transport.Interaction{{
			Request: transport.RecordedRequest{
				Method: http.MethodPost,
				Path:   "/rest/api/3/issue",
				Body:   `{"fields":{"summary":"x","project":{"key":"ENG"}}}`,
			},
			Response: transport.RecordedResponse{Status: 201, Body: `{"key":"ENG-1"}`},
		}},
	}
	c, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: transport.NewReplayer(cassette).Client(),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	resp, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodPost,
		Path:   "/rest/api/3/issue",
		Body:   []byte(`{"fields":{"project":{"key":"ENG"},"summary":"x"}}`),
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Status != 201 {
		t.Errorf("status = %d", resp.Status)
	}
}

// TestHeadersDoNotAffectMatching keeps fixtures from breaking on every release.
// Matching on User-Agent or X-Request-Id would invalidate every cassette the
// moment the version string changed, or on every single run.
func TestHeadersDoNotAffectMatching(t *testing.T) {
	cassette := &transport.Cassette{
		Deployment: transport.Cloud,
		Interactions: []transport.Interaction{{
			Request:  transport.RecordedRequest{Method: http.MethodGet, Path: "/x"},
			Response: transport.RecordedResponse{Status: 200},
		}},
	}
	c, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: transport.NewReplayer(cassette).Client(),
		UserAgent:  "jr/99.0.0 (a version that did not exist when this was recorded)",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/x",
		Header: http.Header{"X-Whatever": {"anything"}},
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
}

func TestCassetteRoundTrip(t *testing.T) {
	original := &transport.Cassette{
		Deployment: transport.DataCenter,
		Interactions: []transport.Interaction{{
			Request: transport.RecordedRequest{
				Method: http.MethodGet, Path: "/rest/api/2/myself",
			},
			Response: transport.RecordedResponse{
				Status: 200,
				Header: map[string][]string{"Content-Type": {"application/json"}},
				Body:   `{"name":"ada"}`,
			},
		}},
	}

	path := filepath.Join(t.TempDir(), "nested", "cassette.json")
	if err := original.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := transport.LoadCassette(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Deployment != original.Deployment {
		t.Errorf("deployment = %q", loaded.Deployment)
	}
	if len(loaded.Interactions) != 1 {
		t.Fatalf("got %d interactions", len(loaded.Interactions))
	}
	if loaded.Interactions[0].Response.Body != `{"name":"ada"}` {
		t.Errorf("body = %q", loaded.Interactions[0].Response.Body)
	}
}

func TestLoadCassetteErrors(t *testing.T) {
	if _, err := transport.LoadCassette(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("a missing fixture loaded successfully")
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := writeFile(bad, "{not json"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := transport.LoadCassette(bad); err == nil {
		t.Error("a malformed fixture loaded successfully")
	}
}

// TestEveryDeploymentHasAFixture is the gate §2.5 describes: a resource that
// ships a Cloud recording and no Data Center one has tested half of what it
// claims to.
func TestEveryDeploymentHasAFixture(t *testing.T) {
	for _, d := range transport.Deployments() {
		path := filepath.Join("testdata", "serverinfo."+string(d)+".json")
		if _, err := transport.LoadCassette(path); err != nil {
			t.Errorf("no fixture for %s: %v", d, err)
		}
	}
}
