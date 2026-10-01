//go:build write

package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestPagingIsInvisible holds every paginated command to one answer whether
// the server sends a collection in one page or one row at a time.
//
// The bugs that reached users in September were page-boundary bugs found
// outside CI: a keyset walk that dropped every project but the first (#144)
// and a feed sorted one page at a time (#197). Each test that could have
// caught them used a fixture that fit on one page. Paging is implemented per
// resource here, nine times over, and this drives every paginated command the
// flag sweep can drive against the same fixtures served two ways: whole, and
// by a server that caps every page at one row, with `total` and `isLast`
// saying there is more. Jira caps pages, and an admin can lower the cap, so
// the second is a server a client meets. The two runs must produce the same
// document: a dropped row, a doubled row, a per-page order, or a different
// `complete` all show up as a difference.
func TestPagingIsInvisible(t *testing.T) {
	var driven, paged int
	for _, c := range cli.Registry().All() {
		if !c.Paginated {
			continue
		}
		if why := commandNotDriven(c); why != "" {
			if pagingNotSwept[c.Name()] == "" {
				t.Errorf("%s is paginated and cannot be driven here (%s); add it "+
					"to pagingNotSwept with where its paging is tested", c.Name(), why)
			}
			continue
		}
		driven++
		for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
			key := c.Name() + " " + string(kind)
			whole, err := runPaged(t, c, kind, false)
			if err != nil {
				t.Errorf("%s fails against the whole fixture (%s), so this sweep "+
					"cannot say anything about its paging", key, errs.Coerce(err).Code)
				continue
			}
			capped, err := runPaged(t, c, kind, true)
			if capped.pages > 1 {
				paged++
			}
			same := err == nil && capped.doc == whole.doc
			switch reason, open := openPaging[key]; {
			case !same && !open:
				t.Errorf("%s answers differently when the server pages it one row "+
					"at a time (%v):\nwhole:\n%s\npaged:\n%s",
					key, errCode(err), whole.doc, capped.doc)
			case same && open:
				t.Errorf("%s pages correctly now and is still in openPaging (%s); "+
					"delete the entry and lower maxOpenPaging", key, reason)
			}
		}
	}
	if driven == 0 || paged == 0 {
		t.Fatalf("drove %d paginated commands and %d paged, so this proves nothing",
			driven, paged)
	}
	if len(openPaging) != maxOpenPaging {
		t.Errorf("%d paging entries are open and the ceiling is %d; the ledger "+
			"only falls", len(openPaging), maxOpenPaging)
	}
	t.Logf("drove %d paginated commands; %d runs walked more than one page", driven, paged)
}

// TestThePagingSweepCanFail is the negative control: a command that reads the
// first page and stops has to answer differently when the server pages.
func TestThePagingSweepCanFail(t *testing.T) {
	firstPageOnly := &registry.Command{
		Path:      []string{"probe", "firstpage"},
		Summary:   "Reads one page and stops",
		Paginated: true,
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: "probe.firstpage", Version: 1}},
		Run: func(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
			conn, _, err := inv.Jira.Connect(ctx)
			if err != nil {
				return nil, err
			}
			resp, err := conn.Do(ctx, transport.Request{
				Method: transport.MethodGet, Path: "/rest/agile/1.0/board",
			})
			if err != nil {
				return nil, err
			}
			var page struct {
				Values []json.RawMessage `json:"values"`
			}
			_ = json.Unmarshal(resp.Body, &page)
			return render.Record("probe.firstpage", 1, render.El("probe").
				Attr("count", strconv.Itoa(len(page.Values)))), nil
		},
	}
	whole, err := runPaged(t, firstPageOnly, site.DataCenter, false)
	if err != nil {
		t.Fatalf("whole: %v", err)
	}
	capped, err := runPaged(t, firstPageOnly, site.DataCenter, true)
	if err == nil && capped.doc == whole.doc {
		t.Error("a command that stops after one page answered identically when the " +
			"server paged it, so TestPagingIsInvisible cannot fail")
	}
}

// openPaging names each command and deployment that answers differently when
// the server pages, by the card that tracks it.
var openPaging = map[string]string{
	"issue.list datacenter":     "a-capped-page-ends-a-data-center-walk",
	"issue.activity datacenter": "a-capped-page-ends-a-data-center-walk",
	"issue.changes datacenter":  "a-capped-page-ends-a-data-center-walk",
}

// maxOpenPaging is openPaging's ceiling. It only falls.
const maxOpenPaging = 3

// pagingNotSwept names every paginated command this harness cannot drive, with
// where its paging is tested instead.
var pagingNotSwept = map[string]string{
	"context.list": "a list of the local config, not a Jira collection; " +
		"TestContextListTruncatesAndSaysSo",
	"schema": "the binary's own command list, not a Jira collection; " +
		"TestSchemaIsCompleteWithNoFlags",
}

// pagedRun is what one run of a command against the paging fake produced.
type pagedRun struct {
	doc   string
	pages int
}

func errCode(err error) string {
	if err == nil {
		return "no error"
	}
	return errs.Coerce(err).Code
}

// runPaged drives a command with --limit all against pagingFake, serving whole
// pages or one row per page.
func runPaged(t *testing.T, c *registry.Command, kind site.Kind, capped bool) (pagedRun, error) {
	t.Helper()
	fake := &pagingFake{kind: kind, capped: capped}
	flags := registry.NewFlags()
	for _, f := range c.AllFlags() {
		if f.Required {
			if v, _, ok := probePair(f); ok {
				setProbe(flags, f, v)
			}
		}
	}
	var args []string
	for _, a := range c.Args {
		if v := argProbe(c, a); v != "" {
			args = append(args, v)
		}
	}
	limit, err := registry.ParseLimit("all")
	if err != nil {
		t.Fatalf("limit: %v", err)
	}
	inv := &registry.Invocation{
		Jira:  faultSession{sweepSession: sweepSession{kind: kind}, rt: fake},
		Args:  args,
		Flags: flags, Limit: limit, Format: render.XML,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	var out strings.Builder
	err = runFor(t.Context(), c, inv, &out)
	return pagedRun{doc: freezeNow(out.String()), pages: fake.pages}, err
}

// keyBound reads the keyset bound out of a search's JQL.
var keyBound = regexp.MustCompile(`(?i)(?:issue)?key\s*([<>])\s*"?([A-Z][A-Z0-9_]*-\d+)"?`)

// pagingFake answers with sweepResponse's fixtures, paged the way the request
// asks: by startAt or nextPageToken, honouring a keyset bound in the JQL, and,
// when capped, one row per page whatever maxResults asked for.
type pagingFake struct {
	kind   site.Kind
	capped bool
	pages  int
}

func (p *pagingFake) RoundTrip(r *http.Request) (*http.Response, error) {
	query := r.URL.Query()
	var body map[string]any
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
	}
	param := func(name string) string {
		if v := query.Get(name); v != "" {
			return v
		}
		switch v := body[name].(type) {
		case string:
			return v
		case float64:
			return strconv.Itoa(int(v))
		}
		return ""
	}
	paged := p.page(sweepResponse(r.URL.Path, p.kind),
		param("startAt"), param("maxResults"), param("nextPageToken"), param("jql"))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(paged)),
		Request:    r,
	}, nil
}

func (p *pagingFake) page(full, startAt, maxResults, token, jql string) string {
	var decoded any
	if err := json.Unmarshal([]byte(full), &decoded); err != nil {
		return full
	}
	start, _ := strconv.Atoi(startAt)
	if token != "" {
		start, _ = strconv.Atoi(token)
	}
	size, err := strconv.Atoi(maxResults)
	if err != nil || size <= 0 {
		size = 1 << 30
	}
	window := func(items []any, size int) ([]any, bool) {
		if m := keyBound.FindStringSubmatch(jql); m != nil {
			var kept []any
			for _, item := range items {
				key, _ := item.(map[string]any)["key"].(string)
				if (m[1] == "<" && issueKeyLess(key, m[2])) ||
					(m[1] == ">" && issueKeyLess(m[2], key)) {
					kept = append(kept, item)
				}
			}
			items = kept
		}
		from := min(start, len(items))
		to := min(from+size, len(items))
		return items[from:to], to >= len(items)
	}

	switch v := decoded.(type) {
	case []any:
		// A bare array has no total to say there is more, so a short page is
		// the end by the protocol: honour what was asked for and never cap.
		if maxResults == "" {
			return full
		}
		page, _ := window(v, size)
		p.pages++
		out, _ := json.Marshal(page)
		return string(out)
	case map[string]any:
		for _, name := range []string{"issues", "values", "comments", "worklogs"} {
			items, ok := v[name].([]any)
			if !ok {
				continue
			}
			if p.capped {
				size = 1
			}
			total := len(items)
			page, last := window(items, size)
			p.pages++
			v[name] = page
			v["startAt"], v["maxResults"], v["total"], v["isLast"] = start, size, total, last
			if last {
				delete(v, "nextPageToken")
			} else {
				v["nextPageToken"] = strconv.Itoa(start + len(page))
			}
			out, _ := json.Marshal(v)
			return string(out)
		}
	}
	return full
}

// issueKeyLess orders two keys the way Jira orders them within a project.
func issueKeyLess(a, b string) bool {
	pa, na, _ := strings.Cut(a, "-")
	pb, nb, _ := strings.Cut(b, "-")
	if pa != pb {
		return pa < pb
	}
	ia, _ := strconv.Atoi(na)
	ib, _ := strconv.Atoi(nb)
	return ia < ib
}
