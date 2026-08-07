package issue_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/issue"
	"github.com/kmoneil/jira-cli/internal/site"
)

func TestBrowseURL(t *testing.T) {
	cases := []struct {
		name, base, key, want string
	}{
		{
			"cloud", "https://acme.atlassian.invalid", "ENG-101",
			"https://acme.atlassian.invalid/browse/ENG-101",
		},
		{
			// The base is whatever the deployment reported, and a Data Center
			// instance behind a context path reports the path. Dropping it
			// would produce a link to the proxy's root.
			"data center under a context path",
			"https://jira.corp.invalid/jira", "OPS-7",
			"https://jira.corp.invalid/jira/browse/OPS-7",
		},
		{
			"a trailing slash is not doubled",
			"https://acme.atlassian.invalid/", "ENG-101",
			"https://acme.atlassian.invalid/browse/ENG-101",
		},
		{
			// ParseKey uppercases the project, so two spellings of one issue
			// produce one link rather than two that differ by case.
			"a lowercase key is the same issue",
			"https://acme.atlassian.invalid", "eng-101",
			"https://acme.atlassian.invalid/browse/ENG-101",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := issue.BrowseURL(site.Info{BaseURL: tc.base}, tc.key)
			if err != nil {
				t.Fatalf("BrowseURL: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestBrowseURLRefusesWhatItCannotAddress covers the two ways there is no link
// to give, both of which are better as an error than as a wrong URL somebody
// clicks.
func TestBrowseURLRefusesWhatItCannotAddress(t *testing.T) {
	if _, err := issue.BrowseURL(site.Info{}, "ENG-101"); err == nil {
		t.Error("a site with no base URL produced a link")
	} else if code := errs.Coerce(err).Code; code != "NO_BASE_URL" {
		t.Errorf("code = %q", code)
	}

	// A key that does not parse cannot be addressed. It can only come from a
	// server response, so this is bad data rather than a bad flag — but a link
	// built by concatenating it is a link that leaves /browse/.
	for _, key := range []string{"", "../../admin-1", "ENG", "ENG-", "-1"} {
		if got, err := issue.BrowseURL(
			site.Info{BaseURL: "https://acme.atlassian.invalid"}, key,
		); err == nil {
			t.Errorf("BrowseURL(%q) produced %q", key, got)
		}
	}
}

// TestURLIsRefusedBeforeAnyOutput is the streaming rule applied to this flag.
//
// `issue list` writes its header — including a `url` column — before the first
// page arrives. Discovering there that the site reports no base URL would leave
// a column name on stdout with nothing ever going under it, and stdout is data
// only. So the check is in Validate, where a refusal costs no bytes.
func TestURLIsRefusedBeforeAnyOutput(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")
	flags := registry.NewFlags()
	flags.SetBool("url", true)

	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{project: "ENG"}, Flags: flags,
		Limit: registry.Limit{N: 50}, Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("--url was accepted against a site with no base URL")
	}
	if code := errs.Coerce(err).Code; code != "NO_BASE_URL" {
		t.Errorf("code = %q", code)
	}

	// The same site is fine for everything else. --url is the only thing that
	// needs a base URL, so it is the only thing that may refuse for want of one.
	if err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{project: "ENG"}, Flags: registry.NewFlags(),
		Limit: registry.Limit{N: 50}, Progress: registry.NoProgress,
	}); err != nil {
		t.Errorf("a list without --url was refused: %v", err)
	}
}

// TestURLAppendsAColumnRatherThanInsertingOne holds the promise that turning
// the flag on cannot move a column somebody already parses.
func TestURLAppendsAColumnRatherThanInsertingOne(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")

	flags := registry.NewFlags()
	inv := &registry.Invocation{
		Jira:  &stubSession{project: "ENG", baseURL: "https://acme.atlassian.invalid"},
		Flags: flags, Limit: registry.Limit{N: 50}, Progress: registry.NoProgress,
	}
	before := cmd.ColumnsFor(inv)

	flags.SetBool("url", true)
	after := cmd.ColumnsFor(inv)

	if len(after) != len(before)+1 {
		t.Fatalf("--url changed the column count from %d to %d", len(before), len(after))
	}
	for i, col := range before {
		if after[i] != col {
			t.Errorf("column %d moved: %+v became %+v", i, col, after[i])
		}
	}
	if last := after[len(after)-1]; last.Header != "url" || last.Path != "url" {
		t.Errorf("last column = %+v", last)
	}
}

// TestTheURLColumnIsABareURL is the decision, kept.
//
// A terminal hyperlink is an OSC 8 escape sequence wrapped around display text.
// It would make the cell clickable and it would put escape bytes in a data
// column, which is the one thing a column may never contain: stdout is data
// only, and `cut -f6` has to yield something a browser can open. Most terminals
// linkify a bare URL anyway, so the clickable version and the parseable version
// are the same string.
func TestTheURLColumnIsABareURL(t *testing.T) {
	i := issue.Issue{Key: "ENG-101", Summary: "x"}
	link, err := issue.BrowseURL(site.Info{BaseURL: "https://acme.atlassian.invalid"}, i.Key)
	if err != nil {
		t.Fatalf("BrowseURL: %v", err)
	}
	i.URL = link

	var out strings.Builder
	doc := render.List(issue.KindList, issue.VersionList, &render.Collection{
		Name: "issues", Items: []*render.Node{i.Node()}, Complete: true,
		Columns: append(issue.ListColumns(), render.Column{Header: "url", Path: "url"}),
	})
	if err := render.Write(&out, doc, render.TSV); err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(out.String(), link) {
		t.Fatalf("the URL is not in the output:\n%s", out.String())
	}
	if strings.ContainsRune(out.String(), 0x1b) {
		t.Errorf("an escape byte reached a data column:\n%q", out.String())
	}
}

// TestURLIsAbsentUnlessAsked covers §2.4: no field appears unless it was
// requested or is in the documented default set.
func TestURLIsAbsentUnlessAsked(t *testing.T) {
	node := issue.Issue{Key: "ENG-101", Summary: "x"}.Node()
	for _, child := range node.Children {
		if child.Name == "url" {
			t.Fatal("an issue nobody asked a URL for carries one")
		}
	}

	cmd, _ := registry.Lookup("issue.list")
	for _, col := range cmd.ColumnsFor(&registry.Invocation{
		Jira: &stubSession{project: "ENG"}, Flags: registry.NewFlags(),
	}) {
		if col.Header == "url" {
			t.Error("the default column set carries a url column")
		}
	}
}

// TestURLExitsUsageOnlyWhereItShould pins the exit of the refusal. NO_BASE_URL
// is a fact about the deployment, not about what the caller typed, so it is not
// a usage error and retrying it will not help.
func TestURLExitsUsageOnlyWhereItShould(t *testing.T) {
	_, err := issue.BrowseURL(site.Info{}, "ENG-101")
	if err == nil {
		t.Fatal("no error")
	}
	if got := errs.ExitOf(err); got != exitcode.Error {
		t.Errorf("exit = %v, want %v", got, exitcode.Error)
	}
	if errs.Coerce(err).Retryable {
		t.Error("a site that reports no base URL was published as retryable")
	}
}

// FuzzBrowseURLStaysUnderBrowse is the rule this repository already applies to
// request paths, applied to a link.
//
// A browse URL is not a request path — nothing is sent to it — but it is
// handed to a person to click, and a key that walked up out of `/browse/` would
// point somewhere nobody chose. The key comes from a server response rather
// than from the command line, which is the case escaping alone is weakest at:
// `..` survives PathEscape's unreserved set intact.
//
// The seeds are the ones ParseKey used to let through, so the regression is
// visible here rather than only in a corpus file.
func FuzzBrowseURLStaysUnderBrowse(f *testing.F) {
	for _, seed := range []string{
		"ENG-1", "eng-12", "ENG-1000", "A1_B-7",
		"../../admin-1", "a/b-1", "ENG/x-9", "%2e%2e-1",
		"a b-1", "a\n-1", "ENG-+1", ".-1", "..-1",
		"", "-", "ENG", "ENG-", "-1", "ENG-abc", "1ENG-1",
		"\x00-1", "É-1", "ＥＮＧ-1", "ENG-١", "ENG-1?x=1", "ENG-1#f",
	} {
		f.Add(seed)
	}

	const base = "https://acme.atlassian.invalid/jira"
	f.Fuzz(func(t *testing.T, key string) {
		got, err := issue.BrowseURL(site.Info{BaseURL: base}, key)
		if err != nil {
			// A key with no link stays with no link.
			return
		}

		const prefix = base + "/browse/"
		if !strings.HasPrefix(got, prefix) {
			t.Fatalf("BrowseURL(%q) = %q, which is not under %s", key, got, prefix)
		}

		// Whatever follows must be one path segment and must not be able to
		// leave it: no slash, no traversal, no query or fragment sneaking the
		// browser somewhere else, and nothing a URL parser would re-read.
		segment := strings.TrimPrefix(got, prefix)
		if segment == "" || segment == "." || segment == ".." {
			t.Fatalf("BrowseURL(%q) = %q, whose segment is %q", key, got, segment)
		}
		if strings.ContainsAny(segment, "/?#\\") {
			t.Fatalf("BrowseURL(%q) = %q, whose segment %q can leave it",
				key, got, segment)
		}

		parsed, perr := url.Parse(got)
		if perr != nil {
			t.Fatalf("BrowseURL(%q) = %q, which does not parse: %v", key, got, perr)
		}
		if parsed.Host != "acme.atlassian.invalid" ||
			!strings.HasPrefix(parsed.EscapedPath(), "/jira/browse/") {
			t.Fatalf("BrowseURL(%q) = %q, which parses to host %q path %q",
				key, got, parsed.Host, parsed.EscapedPath())
		}
	})
}
