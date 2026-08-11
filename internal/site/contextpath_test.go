package site_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestTheProbeUnderAContextPathIsAConversationAServerHad is this package's
// first recording, and it covers the deployment shape the tool is most often
// pointed at.
//
// Every other test here answers from a hand-written body through stubDoer,
// which is the weaker claim this project keeps naming: it establishes that a
// response is handled and never that the request was accepted. That gap was
// invisible in the evidence ledger rather than listed in it, because the
// ledger groups cassettes by package and a package with no cassettes at all
// never forms a group — and this is the package where every deployment
// difference in the tool lives.
//
// A context path is where it matters most. Data Center is commonly served
// under one, the base URL then carries it, and the probe path is joined to
// that base. Applying the prefix twice or dropping it both produce a 404 from
// the web server rather than an answer from the API, which is a failure that
// reads as "the site is not Jira".
func TestTheProbeUnderAContextPathIsAConversationAServerHad(t *testing.T) {
	client, replayer := recordedClient(t, "serverinfo-contextpath.datacenter.json")

	info, err := site.Probe(t.Context(), client, time.Now())
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}

	// "Server" is what a real Data Center reports, including one whose licence
	// says dataCenter:true. Nothing in this tree had ever seen that answer
	// arrive from a server rather than from a fixture its author wrote.
	if info.Kind != site.DataCenter {
		t.Errorf("kind = %q, want %q", info.Kind, site.DataCenter)
	}
	if info.Version == "" {
		t.Error("no version reported")
	}
	if info.CursorPaginated() {
		t.Error("a Data Center was treated as cursor-paginated")
	}

	// The base URL the server reports carries the context path, which is how
	// the instance was actually reached.
	if !strings.HasSuffix(info.BaseURL, "/jira") {
		t.Errorf("baseUrl = %q, want it to carry the context path", info.BaseURL)
	}
	// No Unplayed check here. The cassette holds both exchanges of the
	// invocation that produced it — `jr user me --refresh` probes and then
	// fetches the account — and this test is only the probe. The test below
	// drives both and asserts nothing was left.
	_ = replayer
}

// TestWhoamiUnderAContextPathIsAConversationAServerHad is the second half of
// the same recording, and the one that proves a credential works.
//
// Whoami is what `auth login` uses to tell "this site is Jira" from "this
// token is good", so a Data Center under a context path that answered the
// probe and not this one would fail login with something that sounds like a
// bad credential.
func TestWhoamiUnderAContextPathIsAConversationAServerHad(t *testing.T) {
	client, replayer := recordedClient(t, "serverinfo-contextpath.datacenter.json")

	info, err := site.Probe(t.Context(), client, time.Now())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	who, err := site.Whoami(t.Context(), client, info)
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	// Data Center identifies by name, where Cloud sends an accountId.
	if who.ID != "ada" {
		t.Errorf("id = %q, want ada", who.ID)
	}
	if who.Display != "Ada Lovelace" {
		t.Errorf("display = %q, want Ada Lovelace", who.Display)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("a recorded exchange was never requested: %v", unplayed)
	}
}

// recordedClient replays a cassette through a real transport.Client, with the
// base URL carrying the context path the recording was made under.
//
// The base matters here rather than being boilerplate: it is what every path
// in the cassette is joined to, so a client configured at the root would ask
// for /rest/api/2/serverInfo and the replayer would answer nothing.
func recordedClient(t *testing.T, fixture string) (*transport.Client, *transport.Replayer) {
	t.Helper()
	return recordedClientAt(t, fixture, "https://recorded.invalid/jira")
}

// recordedClientAt is the same for a recording made at the root, where the
// base carries no path.
func recordedClientAt(
	t *testing.T, fixture, base string,
) (*transport.Client, *transport.Replayer) {
	t.Helper()

	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	if !cassette.Evidence() {
		t.Fatalf("%s is not a recording, so replaying it establishes nothing "+
			"about the API", fixture)
	}
	replayer := transport.NewReplayer(cassette)
	client, err := transport.New(transport.Options{
		BaseURL:    base,
		HTTPClient: replayer.Client(),
		Retries:    -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, replayer
}
