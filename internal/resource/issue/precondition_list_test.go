package issue_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// listedAt is the version the canned page reports for every row that has one.
// Millisecond precision, because that is what the token carries and what the
// published `updated` element deliberately does not.
const listedAt = "2026-08-04T11:32:07.412+0000"

// listPage is one search response, parameterised by what `updated` each row
// carries, so a test can produce a row Jira gave no version for without
// needing a second fixture.
func listPage(updated ...string) string {
	rows := make([]string, 0, len(updated))
	for i, u := range updated {
		field := ""
		if u != "" {
			field = `,"updated":"` + u + `"`
		}
		rows = append(rows, `{"id":"`+string(rune('1'+i))+`","key":"ENG-10`+
			string(rune('1'+i))+`","fields":{"summary":"s","labels":[],`+
			`"status":{"name":"X","statusCategory":{"key":"new"}}`+field+`}}`)
	}
	return `{"issues":[` + strings.Join(rows, ",") + `],"total":` +
		string(rune('0'+len(updated))) + `,"isLast":true}`
}

// runListing drives the registered command against a canned page, so the flag
// is read where production reads it.
//
// Through the command rather than through Client, for the reason the write-side
// harness gives: the flag, the column set and the stamping are three places,
// and a test that called only the last would pass while the flag did nothing.
func runListing(t *testing.T, body string, f render.Format, flags ...string) (string, error) {
	t.Helper()
	return runListingAs(t, site.Cloud, body, f, flags...)
}

// runListingAs is runListing against a named deployment, because a token
// carries the deployment it was minted against and refuses to be compared
// across one.
func runListingAs(
	t *testing.T, kind site.Kind, body string, f render.Format, flags ...string,
) (string, error) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}))
	t.Cleanup(srv.Close)

	conn, err := transport.New(transport.Options{BaseURL: srv.URL, Retries: -1})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	set := registry.NewFlags()
	for _, name := range flags {
		set.SetBool(name, true)
	}
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn,
			kind: kind, project: "ENG",
		},
		Flags: set, Limit: registry.Limit{N: 50},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, f, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.ColumnsFor(inv),
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), inv, stream)
	if err != nil {
		return buf.String(), err
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.String(), nil
}

// decodeToken opens a token far enough to say what it describes. It is opaque
// to a caller by design; a test is allowed to look, and has to, because
// "a non-empty string appeared" is not evidence that the right issue was named.
func decodeToken(t *testing.T, token string) issue.Precondition {
	t.Helper()

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token is not base64: %v", err)
	}
	var p issue.Precondition
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("token is not a precondition: %v", err)
	}
	return p
}

// tokensIn pulls every precondition attribute out of a rendered listing, in
// row order.
func tokensIn(t *testing.T, xml string) []string {
	t.Helper()

	var out []string
	for _, rest := range strings.Split(xml, `precondition="`)[1:] {
		end := strings.Index(rest, `"`)
		if end < 0 {
			t.Fatalf("unterminated precondition attribute in:\n%s", xml)
		}
		out = append(out, rest[:end])
	}
	return out
}

// TestAListedRowCarriesABaselineForItsOwnIssue is the whole feature: the token
// on a row names that row.
//
// Asserting the attribute is present would pass on a stamp that put one issue's
// version on every row, which is the failure that costs an edit: a write
// conditioned on a baseline taken from a different issue is refused for the
// wrong reason at best, and at worst compares equal and lets a stale write
// through.
func TestAListedRowCarriesABaselineForItsOwnIssue(t *testing.T) {
	out, err := runListing(t, listPage(listedAt, listedAt), render.XML, "precondition")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	tokens := tokensIn(t, out)
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens for 2 rows:\n%s", len(tokens), out)
	}
	for i, want := range []string{"ENG-101", "ENG-102"} {
		got := decodeToken(t, tokens[i])
		if got.Key != want {
			t.Errorf("row %d holds a baseline for %s, want %s", i, got.Key, want)
		}
		if got.Updated != "2026-08-04T11:32:07.412Z" {
			t.Errorf("row %d version = %q", i, got.Updated)
		}
		if got.Deployment != site.Cloud {
			t.Errorf("row %d deployment = %q", i, got.Deployment)
		}
	}
	if tokens[0] == tokens[1] {
		t.Error("two issues were given the same baseline")
	}
}

// TestAListedBaselineIsTheOneGetWouldHaveMinted is what makes the flag worth
// having rather than merely present.
//
// The point of minting on a row is to skip the `issue get` per row, and that is
// only sound if the two commands produce the same value for the same version of
// the same issue. If they ever diverged, a caller would have to know which
// command their token came from, and the flag would be a trap rather than a
// saving.
func TestAListedBaselineIsTheOneGetWouldHaveMinted(t *testing.T) {
	out, err := runListing(t, listPage(listedAt), render.XML, "precondition")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	tokens := tokensIn(t, out)
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens for 1 row:\n%s", len(tokens), out)
	}
	want, err := issue.EncodePrecondition(
		site.Info{Kind: site.Cloud}, "ENG-101", listedAt)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if tokens[0] != want {
		t.Errorf("a listed row and a fetched record disagree:\nlist %s\nget  %s",
			tokens[0], want)
	}
}

// TestTheListingMintsNoBaselineUnlessAsked covers §2.4: no field appears unless
// it was requested or is in the documented default set.
//
// It is also the argument for the flag existing at all. A token is sixty-odd
// bytes on every row of every listing, and most listings are not read by
// somebody about to write.
func TestTheListingMintsNoBaselineUnlessAsked(t *testing.T) {
	out, err := runListing(t, listPage(listedAt, listedAt), render.XML)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out, "precondition") {
		t.Errorf("a listing nobody asked for a baseline carries one:\n%s", out)
	}
}

// TestARowWithNoVersionGetsNoBaseline is the absence case, and it has to be
// absence rather than an empty attribute.
//
// A token that matches anything is worse than no token: it turns
// --if-unchanged from a guarantee into a formality, silently, for the one
// caller who asked for the guarantee.
func TestARowWithNoVersionGetsNoBaseline(t *testing.T) {
	out, err := runListing(t, listPage(listedAt, ""), render.XML, "precondition")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if tokens := tokensIn(t, out); len(tokens) != 1 {
		t.Errorf("got %d tokens, want one for the row that has a version:\n%s",
			len(tokens), out)
	}
	if strings.Contains(out, `precondition=""`) {
		t.Errorf("a row with no version carries an empty baseline:\n%s", out)
	}
}

// TestAMalformedVersionFailsTheListingWithOrWithoutTheFlag says where the
// refusal actually happens, which is not where this test first claimed.
//
// The first version of it asserted that --precondition refuses a timestamp it
// cannot parse, and it passed. Staging the red said otherwise: swallowing the
// error inside the stamping left the test green, because the row never reaches
// the stamping. normalizeTime rejects the value while the response is being
// decoded, through the same site.ParseTime the token is minted with, so
// anything that arrives at stampPreconditions has already parsed once.
//
// That makes the stamp's own error branch unreachable from this command. It is
// kept, because stampPreconditions is a function and not a promise about its
// callers, and an Issue assembled by some later path deserves the same refusal.
// But the branch is not what protects the caller here, and a test claiming it
// was would be a check that cannot fail wearing the name of one that can.
//
// So this pins the true mechanism: the listing fails, at the same point, on the
// same code, whether or not a baseline was asked for.
func TestAMalformedVersionFailsTheListingWithOrWithoutTheFlag(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"asked for a baseline", []string{"precondition"}},
		{"asked for nothing", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runListing(t, listPage("not a timestamp"), render.XML, tc.flags...)
			if err == nil {
				t.Fatal("a timestamp this tool cannot parse was passed over in silence")
			}
			if code := errs.Coerce(err).Code; code != "MALFORMED_TIMESTAMP" {
				t.Errorf("code = %q, want MALFORMED_TIMESTAMP", code)
			}
		})
	}
}

// TestEncodePreconditionRefusesAVersionItCannotParse tests the branch above at
// the level where it is reachable.
//
// A token minted from a timestamp nobody could read would be a baseline
// describing nothing, and it would be compared against a real one on the next
// write. Refusing is the only answer that does not silently weaken
// --if-unchanged.
func TestEncodePreconditionRefusesAVersionItCannotParse(t *testing.T) {
	_, err := issue.EncodePrecondition(
		site.Info{Kind: site.Cloud}, "ENG-101", "not a timestamp")
	if err == nil {
		t.Fatal("a baseline was minted from a timestamp this tool cannot read")
	}
	if code := errs.Coerce(err).Code; code != "MALFORMED_TIMESTAMP" {
		t.Errorf("code = %q, want MALFORMED_TIMESTAMP", code)
	}
}

// TestThePreconditionColumnIsAppended holds the TSV contract: turning a flag on
// adds a column at the end and moves nothing a script already counts.
func TestThePreconditionColumnIsAppended(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")

	flags := registry.NewFlags()
	inv := &registry.Invocation{
		Jira:  &stubSession{project: "ENG"},
		Flags: flags, Limit: registry.Limit{N: 50}, Progress: registry.NoProgress,
	}
	before := cmd.ColumnsFor(inv)

	flags.SetBool("precondition", true)
	after := cmd.ColumnsFor(inv)

	if len(after) != len(before)+1 {
		t.Fatalf("--precondition changed the column count from %d to %d",
			len(before), len(after))
	}
	for i, col := range before {
		if after[i] != col {
			t.Errorf("column %d moved: %+v became %+v", i, col, after[i])
		}
	}
	if last := after[len(after)-1]; last.Header != "precondition" {
		t.Errorf("last column = %+v", last)
	}
}

// TestThePreconditionColumnCarriesTheToken closes the gap between "the column
// is declared" and "the column has the value in it".
//
// The path is a bare attribute reference rather than an element name, which is
// a spelling nothing else on this command uses, and a wrong one resolves to an
// empty cell rather than to an error.
func TestThePreconditionColumnCarriesTheToken(t *testing.T) {
	out, err := runListing(t, listPage(listedAt), render.TSV, "precondition")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want a header and one row:\n%s", len(lines), out)
	}
	header := strings.Split(lines[0], "\t")
	row := strings.Split(lines[1], "\t")
	if len(header) != len(row) {
		t.Fatalf("%d headers and %d cells:\n%s", len(header), len(row), out)
	}
	if header[len(header)-1] != "precondition" {
		t.Fatalf("last header = %q", header[len(header)-1])
	}
	cell := row[len(row)-1]
	if cell == "" {
		t.Fatalf("the precondition cell is empty:\n%s", out)
	}
	if got := decodeToken(t, cell); got.Key != "ENG-101" {
		t.Errorf("the cell holds a baseline for %s", got.Key)
	}
}

// TestAVersionOutsideTheRecordableRangeIsRefusedRatherThanMinted names the case
// the round-trip fuzz found, so it stays fixed under a name a reader can look
// up rather than only as a corpus file.
//
// site.ParseTime accepts an instant before year 1, and formatting it in UTC
// produces a negative year that preconditionLayout cannot read back. Minting it
// anyway would hand the caller a token this tool's own write verbs reject as
// INVALID_PRECONDITION, which reports a typo about a value the caller was
// given. No Jira serves such a date; the refusal is here so that
// "minted implies accepted" is a property rather than a coincidence.
func TestAVersionOutsideTheRecordableRangeIsRefusedRatherThanMinted(t *testing.T) {
	_, err := issue.EncodePrecondition(
		site.Info{Kind: site.Cloud}, "ENG-101", "0000-01-01T0:00:00+0010")
	if err == nil {
		t.Fatal("minted a baseline the write verbs would refuse")
	}
	if code := errs.Coerce(err).Code; code != "MALFORMED_TIMESTAMP" {
		t.Errorf("code = %q, want MALFORMED_TIMESTAMP", code)
	}
}
