package issue_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/issue"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

const pretendPDF = "%PDF-1.4 pretend contents"

// TestAttachmentsComeFromTheIssue covers where the metadata lives. Jira has no
// listing endpoint for attachments — they are a field — so this asks for that
// one field rather than the whole issue.
func TestAttachmentsComeFromTheIssue(t *testing.T) {
	conn, replayer := replayConn(t, "attachments.datacenter.json")
	client := &issue.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	items, err := client.Attachments(t.Context(), "ENG-101")
	if err != nil {
		t.Fatalf("attachments: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d attachments, want 2", len(items))
	}
	if items[0].Filename != "report.pdf" || items[0].Size != 1048576 {
		t.Errorf("first = %+v", items[0])
	}
	// The timestamp is normalized like every other, so a Data Center offset
	// with no colon does not reach the output.
	if items[0].Created != "2026-08-01T09:00:00Z" {
		t.Errorf("created = %q, want RFC 3339 UTC", items[0].Created)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the issue was never read: %v", unplayed)
	}
}

// TestTheContentURLIsNotPublished covers a deliberate omission. It is a
// server-supplied absolute URL this tool refuses to follow off-site, and
// printing it would invite a caller to fetch it with their own credentials and
// skip that check.
func TestTheContentURLIsNotPublished(t *testing.T) {
	node := issue.Attachment{
		ID: "1", Filename: "a.txt", Size: 3, Created: "2026-08-01T09:00:00Z",
		Content: "https://jira.acme.invalid/secure/attachment/1/a.txt",
	}.Node()

	rendered := renderNode(t, node)
	if strings.Contains(rendered, "secure/attachment") {
		t.Errorf("the content URL reached the output:\n%s", rendered)
	}
	for _, col := range issue.AttachmentColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}
}

// TestDownloadFollowsTheRightPathPerDeployment is the split. Cloud serves the
// bytes from the REST API; Data Center has no such endpoint and reports an
// absolute URL on the attachment, which costs one extra read.
func TestDownloadFollowsTheRightPathPerDeployment(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "out.pdf")

			doc, replayer := runDownload(t, kind, "download", dest, registry.NewFlags())
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("an expected request was never made: %v", unplayed)
			}
			if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
				t.Errorf("a request went somewhere unrecorded: %v", unmatched)
			}

			written, err := os.ReadFile(dest) //nolint:gosec // test temp dir.
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(written) != pretendPDF {
				t.Errorf("wrote %q", written)
			}
			if bytes, _ := doc.Record.AttrValue("bytes"); bytes != "25" {
				t.Errorf("bytes = %q, want the count taken while writing", bytes)
			}
			name, _ := doc.Record.ChildNamed("filename")
			if name == nil || name.Text != "report.pdf" {
				t.Errorf("filename = %+v, want the attachment's own", name)
			}
		})
	}
}

// TestAnOffSiteContentURLIsRefused is the guard that matters most here. The URL
// comes from the server, so following it blind is how a credential reaches a
// host nobody chose — and unlike a mistyped --site, nothing the caller did
// would show it.
func TestAnOffSiteContentURLIsRefused(t *testing.T) {
	dir := t.TempDir()
	cmd, _ := registry.Lookup("issue.attachment.download")
	conn, replayer := replayConn(t, "offsite.datacenter.json")

	flags := registry.NewFlags()
	flags.SetString("output", filepath.Join(dir, "out.bin"))
	_, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.DataCenter},
		Args:  []string{"ENG-101", "10042"},
		Flags: flags, Stderr: io.Discard, Stdout: io.Discard,
		Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("a content URL on another host was followed")
	}
	if code := errs.Coerce(err).Code; code != "OFF_SITE_URL" {
		t.Fatalf("code = %q, want OFF_SITE_URL", code)
	}
	// Only the metadata read happened. Nothing was fetched from the other host,
	// which the cassette could not have answered anyway.
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request was attempted past the refusal: %v", unmatched)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a file was created for a refused download: %v", entries)
	}
}

// TestDownloadRefusesToOverwrite covers the flag guarding an irreversible
// mistake: a download that silently replaced a file would be indistinguishable
// from one that worked.
func TestDownloadRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(dest, []byte("do not lose me"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := runDownloadErr(t, site.Cloud, "download", dest, registry.NewFlags())
	if err == nil {
		t.Fatal("an existing file was overwritten")
	}
	e := errs.Coerce(err)
	if e.Code != "DESTINATION_EXISTS" {
		t.Fatalf("code = %q, want DESTINATION_EXISTS", e.Code)
	}
	if e.Exit != exitcode.Conflict {
		t.Errorf("exit = %v, want %v", e.Exit, exitcode.Conflict)
	}
	if got, _ := os.ReadFile(dest); string(got) != "do not lose me" { //nolint:gosec // test temp dir.
		t.Errorf("the existing file was touched: %q", got)
	}

	// With --force it is replaced, so the refusal is a guard and not a wall.
	flags := registry.NewFlags()
	flags.SetBool("force", true)
	if _, err := runDownloadErr(t, site.Cloud, "download", dest, flags); err != nil {
		t.Fatalf("--force did not allow the overwrite: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != pretendPDF { //nolint:gosec // test temp dir.
		t.Errorf("after --force the file is %q", got)
	}
}

// TestDownloadToStdoutEmitsNoDocument is the collision the card names. A file
// and a result document on the same channel means one corrupts the other, and
// the caller asked for the file.
func TestDownloadToStdoutEmitsNoDocument(t *testing.T) {
	cmd, _ := registry.Lookup("issue.attachment.download")
	conn, _ := replayConn(t, "download.cloud.json")

	var out strings.Builder
	flags := registry.NewFlags()
	flags.SetString("output", "-")
	inv := &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.Cloud},
		Args:  []string{"ENG-101", "10042"},
		Flags: flags, Stderr: io.Discard, Stdout: &out,
		Progress: registry.NoProgress,
	}

	// The CLI asks this before running, and it is what stops a document being
	// rendered on top of the bytes.
	if cmd.EmitsDocumentFor(inv) {
		t.Error("--output - still claims to emit a document")
	}
	doc, err := cmd.Run(t.Context(), inv)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if doc != nil {
		t.Errorf("a document was produced alongside the file: %+v", doc)
	}
	if out.String() != pretendPDF {
		t.Errorf("stdout = %q, want the file", out.String())
	}

	// Writing to a path is the ordinary case and does emit one.
	toFile := &registry.Invocation{Flags: registry.NewFlags()}
	toFile.Flags.SetString("output", filepath.Join(t.TempDir(), "x"))
	if !cmd.EmitsDocumentFor(toFile) {
		t.Error("--output <path> was treated as owning stdout")
	}
}

// TestDownloadToStdoutIsRefusedWithoutOne covers the caller that has no stdout
// to give — `mcp serve`, where a file written there is a frame the peer cannot
// parse. That bug has shipped once already.
func TestDownloadToStdoutIsRefusedWithoutOne(t *testing.T) {
	cmd, _ := registry.Lookup("issue.attachment.download")
	flags := registry.NewFlags()
	flags.SetString("output", "-")

	err := cmd.Validate(t.Context(), &registry.Invocation{
		Args: []string{"ENG-101", "10042"}, Flags: flags,
		Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("a file was accepted for a caller with no stdout")
	}
	if code := errs.Coerce(err).Code; code != "NO_STDOUT" {
		t.Errorf("code = %q, want NO_STDOUT", code)
	}
}

// TestAttachmentReadsNeedNoTag keeps listing and downloading in every build.
// Only uploading changes anything.
func TestAttachmentReadsNeedNoTag(t *testing.T) {
	for _, name := range []string{"issue.attachment.list", "issue.attachment.download"} {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if cmd.Mutating || cmd.Destructive {
			t.Errorf("%s is a read but declares otherwise", name)
		}
		if len(cmd.RequiresTags) != 0 {
			t.Errorf("%s requires %v; reading needs no tag", name, cmd.RequiresTags)
		}
	}
}

// runDownload runs the command and requires it to succeed.
func runDownload(
	t *testing.T, kind site.Kind, fixture, dest string, flags registry.Flags,
) (*render.Doc, *transport.Replayer) {
	t.Helper()
	doc, replayer, err := downloadOnce(t, kind, fixture, dest, flags)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	return doc, replayer
}

// runDownloadErr is the same without the assertion, for the failure cases.
func runDownloadErr(
	t *testing.T, kind site.Kind, fixture, dest string, flags registry.Flags,
) (*render.Doc, error) {
	t.Helper()
	doc, _, err := downloadOnce(t, kind, fixture, dest, flags)
	return doc, err
}

func downloadOnce(
	t *testing.T, kind site.Kind, fixture, dest string, flags registry.Flags,
) (*render.Doc, *transport.Replayer, error) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.attachment.download")
	if !ok {
		t.Fatal("issue attachment download is not registered")
	}
	conn, replayer := replayConn(t, fixture+"."+string(kind)+".json")
	if flags.String("output") == "" && !flags.Bool("force") {
		flags = registry.NewFlags()
	}
	flags.SetString("output", dest)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: kind},
		Args:  []string{"ENG-101", "10042"},
		Flags: flags, Stderr: io.Discard, Stdout: io.Discard,
		Progress: registry.NoProgress,
	})
	return doc, replayer, err
}

// renderNode encodes one node so a test can assert on what a consumer sees
// rather than on the tree that produced it.
func renderNode(t *testing.T, n *render.Node) string {
	t.Helper()
	var buf strings.Builder
	doc := render.Record(issue.KindAttachmentList, issue.VersionAttachmentList, n)
	if err := render.Write(&buf, doc, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}
