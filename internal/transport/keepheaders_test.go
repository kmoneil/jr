package transport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/transport"
)

// TestARecordingKeepsOnlyTheHeadersSomethingReads is the guarantee that a
// header carrying identity in an unanticipated shape cannot reach a file.
//
// The residue check is a second opinion and only ever a second opinion: it
// matches shapes, so it catches an email, a UUID, a host, a long hex run. Data
// Center answers with `X-AUSERNAME`, whose value is an account name and which
// matches none of those — a report would have read it and said nothing. What
// closes that is not looking harder, it is not capturing it.
func TestARecordingKeepsOnlyTheHeadersSomethingReads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Retry-After", "30")
		// Everything below is what a real Cloud response carries and no code
		// in this tree reads.
		h.Set("Atl-Traceid", "16abc156eac44d05b4bf840962c9ecf3")
		h.Set("X-Amz-Cf-Id", "N321QlgSaeqC6Kry3ie4pgnK0lYet47EHNDmappNWplWsYrRydT8nA==")
		h.Set("Report-To", `{"endpoints":[{"url":"https://cdn-internal"}]}`)
		h.Set("Via", "1.1 abcdef0123456789 (an edge cache)")
		// The Data Center header this test exists for: a bare account name,
		// identifier-shaped to a human and to no pattern in this package.
		h.Set("X-AUSERNAME", "ada")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rec := transport.NewRecorder(nil, transport.Cloud)
	conn, err := transport.New(transport.Options{
		BaseURL: srv.URL, HTTPClient: &http.Client{Transport: rec}, Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := conn.Do(t.Context(), transport.Request{
		Method: transport.MethodGet, Path: "/rest/api/3/myself",
	}); err != nil {
		t.Fatalf("request: %v", err)
	}

	cassette := rec.Cassette()
	if len(cassette.Interactions) != 1 {
		t.Fatalf("got %d interactions, want 1", len(cassette.Interactions))
	}
	got := http.Header(cassette.Interactions[0].Response.Header)

	for name := range got {
		if _, keep := transport.KeptHeaders[http.CanonicalHeaderKey(name)]; !keep {
			t.Errorf("the recording kept %q, which nothing reads", name)
		}
	}
	if got.Get("Content-Type") == "" {
		t.Error("Content-Type was dropped, and the error parser reads it")
	}
	if got.Get("Retry-After") != "30" {
		t.Errorf("Retry-After = %q, and the retry loop reads it", got.Get("Retry-After"))
	}
	if v := got.Get("X-AUSERNAME"); v != "" {
		t.Errorf("X-AUSERNAME = %q reached the tape; a name is not a shape any "+
			"residue pattern matches, so nothing downstream would have said so", v)
	}
}

// TestEveryKeptHeaderNamesItsReader is what stops the list growing back.
//
// It went from 25 headers to five because twenty of them were kept by accident
// — nothing chose them, they were simply whatever the server sent. An entry
// with no reader named is the same accident with a map around it.
func TestEveryKeptHeaderNamesItsReader(t *testing.T) {
	if len(transport.KeptHeaders) == 0 {
		t.Fatal("no header is kept, so every recording has lost its Content-Type")
	}
	for name, reason := range transport.KeptHeaders {
		if name != http.CanonicalHeaderKey(name) {
			t.Errorf("%q is not in canonical form, so it will match nothing", name)
		}
		if len(reason) < 20 {
			t.Errorf("%s is kept with the reason %q, which does not name a reader",
				name, reason)
		}
		if !strings.Contains(reason, ".go") {
			t.Errorf("%s is kept with a reason that names no file: %q", name, reason)
		}
	}
}

// TestNoCommittedFixtureCarriesAnUnreadHeader holds the tree to what the
// recorder now produces.
//
// Trimming the recorder does nothing for the thirty cassettes already
// committed, and those are the ones somebody greps. This walks them.
func TestNoCommittedFixtureCarriesAnUnreadHeader(t *testing.T) {
	root := filepath.Join("..", "..", "internal")

	var checked int
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".json") ||
			!strings.Contains(path, "testdata") {
			return nil
		}
		raw, readErr := os.ReadFile(path) //nolint:gosec // a path from the test tree.
		if readErr != nil {
			return readErr
		}
		// Not every JSON file under testdata is a cassette; the ones that are
		// not simply have no interactions to check.
		var c transport.Cassette
		if unmarshalErr := json.Unmarshal(raw, &c); unmarshalErr != nil {
			return nil //nolint:nilerr // a non-cassette JSON file is not a failure.
		}
		if len(c.Interactions) == 0 {
			return nil
		}
		checked++
		for i, in := range c.Interactions {
			for name := range in.Response.Header {
				if _, keep := transport.KeptHeaders[http.CanonicalHeaderKey(name)]; !keep {
					t.Errorf("%s interaction %d keeps %q, which nothing reads",
						path, i, name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// A walk that matched nothing would pass silently, which for a guard is
	// worse than failing.
	if checked < 50 {
		t.Fatalf("only %d cassettes walked, so this read the wrong tree", checked)
	}
}
