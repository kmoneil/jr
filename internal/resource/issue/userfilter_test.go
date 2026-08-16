package issue_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// The person a filter names, and the id each deployment answers with. The two
// ids differ in kind and not only in spelling, which is the whole reason this
// resolution exists: Cloud takes an opaque accountId and Data Center takes a
// username, and neither takes the name a caller has.
const (
	filterDisplayName = "Ada Lovelace"
	cloudAccountID    = "000000:00000000-0000-0000-0000-000000000000"
	dataCenterName    = "ada"
)

// TestEveryUserFilterResolvesThePersonItNames drives each filter that names a
// person and reads the query that reached the server.
//
// The failure it exists for is silent. `watcher = "Ada Lovelace"` is valid JQL
// naming nobody, so Jira answers it with an empty page and a 200, and the
// command reports a complete, empty, exit 0 result: the same answer it gives
// when you genuinely watch nothing. `--reporter` shipped that way, and the
// tests of the day passed, because every one of them either built a
// QueryOptions whose user fields already held an id or asserted the shape of
// the JQL rather than the value in it.
//
// So this asserts the value, end to end, per deployment: the id the site
// answered with is in the query, and the name the caller typed is not. The
// flags come from issue.UserFilterFlagsForTest, which is the list the command
// itself loops over, so a tenth user-valued flag is swept the day it is added
// rather than the day somebody remembers this file.
func TestEveryUserFilterResolvesThePersonItNames(t *testing.T) {
	flags := issue.UserFilterFlagsForTest()
	if len(flags) < 9 {
		t.Fatalf("the command declares %d user-valued filters and there were "+
			"nine when this was written, so either they were removed or this "+
			"sweep is reading the wrong list", len(flags))
	}

	for _, kind := range []site.Kind{site.Cloud, site.DataCenter} {
		want := cloudAccountID
		if kind != site.Cloud {
			want = dataCenterName
		}

		for _, flag := range flags {
			t.Run(string(kind)+"/"+flag, func(t *testing.T) {
				jql := runUserFilter(t, kind, flag, filterDisplayName)

				if !strings.Contains(jql, want) {
					t.Errorf("--%s sent %q, which does not carry the id %s "+
						"resolved to. A name where an id belongs matches "+
						"nobody and comes back complete, empty, and exit 0",
						flag, jql, filterDisplayName)
				}
				if strings.Contains(jql, filterDisplayName) {
					t.Errorf("--%s sent %q, which carries the display name the "+
						"caller typed rather than the id the site answered with",
						flag, jql)
				}
			})
		}
	}
}

// TestOnlyTheAssigneeTakesTheSentinelWords holds the other half of the rule.
//
// `unassigned` is a real state of the assignee field and of nothing else:
// `creator IS EMPTY` matches nothing, and a predicate has no empty form at all,
// because `CHANGED BY EMPTY` is not JQL. Accepting the word everywhere would
// turn a nonsense filter into an empty result and exit 0, which is the failure
// the sweep above exists for, arriving by the other door.
func TestOnlyTheAssigneeTakesTheSentinelWords(t *testing.T) {
	sentinel := issue.SentinelFilterFlagsForTest()
	if len(sentinel) != 1 || sentinel[0] != "assignee" {
		t.Fatalf("the sentinels are honoured on %v; if that is deliberate, this "+
			"test and the invariant both need rewriting", sentinel)
	}

	for _, flag := range issue.UserFilterFlagsForTest() {
		if flag == "assignee" {
			continue
		}
		t.Run(flag, func(t *testing.T) {
			err := validateUserFilter(t, site.Cloud, flag, "unassigned")
			if err == nil {
				t.Fatalf("--%s accepted the word unassigned, which names nobody "+
					"on any field but the assignee", flag)
			}
			if !strings.Contains(err.Error(), "names nobody") {
				t.Errorf("--%s was refused with %q, and the caller needs to be "+
					"told the word is the problem", flag, err)
			}
		})
	}
}

// runUserFilter drives `issue list` with one filter set to input, and returns
// the JQL that reached the server.
func runUserFilter(t *testing.T, kind site.Kind, flag, input string) string {
	t.Helper()

	srv, seen := searchRecorder(t, kind)
	defer srv.Close()

	conn, err := transport.New(transport.Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}

	cmd, inv := userFilterInvocation(t, kind, flag, input, conn)

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.ColumnsFor(inv),
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), inv, stream)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := seen.jql()
	if got == "" {
		t.Fatal("no query reached the server, so this asserts nothing about " +
			"what the filter sent")
	}
	return got
}

// validateUserFilter runs only the validation half, for the inputs that must
// never reach a query at all.
func validateUserFilter(t *testing.T, kind site.Kind, flag, input string) error {
	t.Helper()
	cmd, inv := userFilterInvocation(t, kind, flag, input, nil)
	return cmd.Validate(t.Context(), inv)
}

// userFilterInvocation builds `issue list` with one user-valued flag set, and a
// site that answers a user search with one person.
//
// conn is where the query goes and is nil for the validation-only path, whose
// whole point is that the value never reaches a query at all.
func userFilterInvocation(
	t *testing.T, kind site.Kind, flag, input string, conn *transport.Client,
) (*registry.Command, *registry.Invocation) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	flags := registry.NewFlags()
	flags.SetString(flag, input)
	flags.SetInt("page-size", 50)

	session := &stubSession{
		kind: kind, conn: conn,
		metaClient: &stubDoer{
			body: catalogueJSON,
			byPath: map[string]string{
				"/user/search": userSearchBody(kind),
			},
		},
	}
	return cmd, &registry.Invocation{
		Jira: session, Flags: flags, Limit: registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
}

// userSearchBody is what each deployment's user search answers for one person.
//
// Cloud sends an accountId and no name; Data Center sends a name and no
// accountId, which is why the id under test differs by deployment rather than
// only the endpoint that produced it.
func userSearchBody(kind site.Kind) string {
	if kind == site.Cloud {
		return fmt.Sprintf(
			`[{"accountId":%q,"displayName":%q,"emailAddress":"ada@example.invalid",`+
				`"active":true}]`, cloudAccountID, filterDisplayName)
	}
	return fmt.Sprintf(
		`[{"name":%q,"key":%q,"displayName":%q,"emailAddress":"ada@example.invalid",`+
			`"active":true}]`, dataCenterName, dataCenterName, filterDisplayName)
}

// searchRecorder answers a search with an empty page and keeps the query it was
// asked.
//
// Empty is the right answer here and not a shortcut: this is a test about what
// was asked, not about what came back, and a page of rows would make the
// assertion depend on a fixture that has nothing to do with it.
func searchRecorder(t *testing.T, kind site.Kind) (*httptest.Server, *queryLog) {
	t.Helper()
	seen := &queryLog{}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("jql"); q != "" {
			seen.record(q)
		}
		_, _ = w.Write([]byte(searchBody(kind, nil, 0, 0)))
	})), seen
}

// queryLog holds the last JQL a server was asked for. The client may page, and
// the guard against reading a cursor page rather than the first one is that
// every page of a run carries the same filter.
type queryLog struct {
	mu  sync.Mutex
	got string
}

func (l *queryLog) record(jql string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = jql
}

func (l *queryLog) jql() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.got
}
