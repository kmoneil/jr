//go:build write

package issue

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/transport"
)

// Kind the attachment upload emits.
const (
	KindAttachmentUpload    = "issue.attachment.upload"
	VersionAttachmentUpload = 1
)

func init() {
	registry.Register(attachmentUploadCommand())

	// The same shape a listing reports, plus the issue it landed on: Jira
	// answers an upload with the attachment it created.
	render.RegisterSchema(KindAttachmentUpload, withIssue(AttachmentSchema()))
}

// xsrfHeader is what Jira requires before it will accept an attachment.
//
// It is not a token and nothing is looked up to produce it. The endpoint
// refuses any request without it, as a guard against a form post from another
// origin, and "no-check" is the documented value for a client that is not a
// browser.
const xsrfHeader = "X-Atlassian-Token"

// multipartField is the form field name Jira reads the file from. It is fixed
// by the API, not by preference.
const multipartField = "file"

func attachmentUploadCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "attachment", "upload"},
		Summary: "Attach a file to an issue",
		Description: strings.TrimSpace(`
Uploads a file and attaches it to an issue.

The file is streamed rather than read into memory, so its size is bounded by
what Jira accepts and not by this process.

The name Jira stores is the file's own base name. --name overrides it, which is
the only way to attach something under a name other than the one on disk.

A retry re-reads the file from the start, which is why this takes a path rather
than accepting input on stdin: an upload retried after a 429 has to send the
same bytes again, and a pipe cannot do that. Reading from a pipe would mean
either refusing every retry or sending a truncated file, and one of those is
much worse than the other.

--dry-run prints the request without the file's contents. The body is the file,
and printing it would be neither useful nor safe.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue attachment upload ENG-101 ./report.pdf",
			buildinfo.App + " issue attachment upload ENG-101 ./out.log --name run-42.log",
		}, "\n"),
		Args: []registry.Arg{
			{Name: "key", Usage: "issue key, e.g. ENG-101", Required: true},
			{Name: "file", Usage: "path to the file to attach", Required: true},
		},
		Flags: []registry.Flag{
			{
				Name: "name", Type: registry.TypeString,
				Usage: "store it under this name instead of the file's own",
			},
			dryRunFlag(),
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindAttachmentUpload, Version: VersionAttachmentUpload},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateUpload,
		Run:       runAttachmentUpload,
	}
}

func validateUpload(_ context.Context, inv *registry.Invocation) error {
	if err := requireIssueKey(inv); err != nil {
		return err
	}
	if len(inv.Args) < 2 || strings.TrimSpace(inv.Args[1]) == "" {
		return errs.Usage("NO_FILE", "a path to the file to attach is required")
	}

	// Checked before anything is sent, because a missing file is the most
	// common way this goes wrong and it costs nothing to find out here.
	info, err := os.Stat(inv.Args[1])
	if err != nil {
		if os.IsNotExist(err) {
			return errs.Usage("NO_SUCH_FILE", "%s does not exist", inv.Args[1])
		}
		return errs.Usage("UNREADABLE_FILE", "cannot read %s", inv.Args[1]).Wrap(err)
	}
	if info.IsDir() {
		return errs.Usage("NOT_A_FILE", "%s is a directory", inv.Args[1]).
			WithRemedy("attach files one at a time")
	}
	if name := inv.Flags.String("name"); name != "" && name != filepath.Base(name) {
		// A name is not a path. Jira would store the separator verbatim, and
		// what comes back is a filename nothing can open.
		return errs.Usage("INVALID_NAME", "--name is a filename, not a path").
			WithDetail("%q contains a path separator", name)
	}
	return nil
}

// UploadRequest builds the upload without sending it.
//
// The body is produced by a factory rather than assembled here, because it is
// the file: a retry re-opens it and streams it again, and holding it in memory
// to make that possible is exactly what this command exists not to do.
func (c *Client) UploadRequest(key, path, name string) (transport.Request, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}
	if name == "" {
		name = filepath.Base(path)
	}

	// The boundary is fixed for this request so the Content-Type header and
	// every re-opened body agree. A fresh one per attempt would leave a retry
	// announcing a boundary its body does not use.
	boundary := multipart.NewWriter(io.Discard).Boundary()

	return transport.Request{
		Method: transport.MethodPost,
		Path: c.Site.APIBase() + "/issue/" +
			url.PathEscape(parsed.String()) + "/attachments",
		Header: map[string][]string{
			"Content-Type": {"multipart/form-data; boundary=" + boundary},
			xsrfHeader:     {"no-check"},
		},
		BodySource: multipartSource(path, name, boundary),
	}, nil
}

// multipartSource returns a factory that streams the file as one multipart
// form, re-opening it on every call.
//
// It uses a pipe so nothing is buffered: the writer runs in its own goroutine
// and the transport reads the form as it is produced. A failure part-way
// through is carried to the reader by CloseWithError rather than truncating the
// body silently, because a short multipart form is one Jira will happily accept
// as a smaller file.
func multipartSource(path, name, boundary string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		file, err := os.Open(path) //nolint:gosec // the caller named the path.
		if err != nil {
			return nil, err
		}

		pr, pw := io.Pipe()
		form := multipart.NewWriter(pw)
		if err := form.SetBoundary(boundary); err != nil {
			_ = file.Close()
			return nil, err
		}

		go func() {
			defer func() { _ = file.Close() }()
			writeMultipartBody(pw, form, name, file)
		}()

		return pipeCloser{PipeReader: pr}, nil
	}
}

// writeMultipartBody streams src as one multipart form and closes the pipe,
// carrying any failure to the reader rather than truncating the body.
//
// It is a named function rather than the goroutine's closure so the recover has
// something to sit on that a test can call. A panic in a goroutine is not caught
// by a recover anywhere else: not by the one in mcp.Server.dispatch, not by one
// in Command.Run, and there is no equivalent of net/http's per-connection
// recovery that reaches it. Without this, a panic here ends the process — and
// under `mcp serve` that is the session, not the upload.
//
// This is the only `go func(` in the module reachable from a request. Nothing
// below looks able to panic today: CreateFormFile and Close return errors,
// io.Copy on a file returns errors, and CloseWithError is safe to call twice.
// The recover is here because the guarantee has to be given from inside the
// goroutine or not at all, and because the cost of being wrong is the process.
func writeMultipartBody(
	pw *io.PipeWriter, form *multipart.Writer, name string, src io.Reader,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = pw.CloseWithError(bodyPanic(recovered))
		}
	}()

	part, err := form.CreateFormFile(multipartField, name)
	if err != nil {
		_ = pw.CloseWithError(err)
		return
	}
	if _, err := io.Copy(part, src); err != nil {
		_ = pw.CloseWithError(err)
		return
	}
	if err := form.Close(); err != nil {
		_ = pw.CloseWithError(err)
		return
	}
	_ = pw.Close()
}

// bodyPanic turns a panic in the body producer into the failure the reader can
// already receive, so the upload fails instead of the process.
//
// CloseWithError is the path every other failure in that goroutine takes, and
// the transport already knows how to report a body that could not be produced.
// A panic is one more way to fail to produce bytes.
//
// The panic value is wrapped rather than put in the message or the detail.
// errs.Wrap keeps it reachable through errors.Unwrap and never renders it — so
// a developer and a test can get at it while the caller cannot, which matters
// because under `mcp serve` this error becomes a tool result and the caller is
// the peer. The same rule the MCP recover follows, for the same reason.
//
// Wrapped and not dropped: a recover that discards what it caught turns a
// reproducible crash into an unexplained failed upload.
func bodyPanic(recovered any) error {
	return errs.Runtime("UPLOAD_BODY_FAILED",
		"the attachment could not be read into a request body").
		WithRemedy("check the file is readable and has not changed, and report this").
		Wrap(fmt.Errorf("panic in the multipart body producer: %v\n%s",
			recovered, debug.Stack()))
}

// pipeCloser makes closing the read end also stop the writer, so an abandoned
// upload does not leave a goroutine blocked on a pipe nobody is reading.
type pipeCloser struct{ *io.PipeReader }

func (p pipeCloser) Close() error { return p.CloseWithError(io.ErrClosedPipe) }

func runAttachmentUpload(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue attachment upload")
	if err != nil {
		return nil, err
	}
	key, path := inv.Args[0], inv.Args[1]

	req, err := client.UploadRequest(key, path, inv.Flags.String("name"))
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		// The body is the file. Printing it would be neither useful nor safe,
		// and DryRunDoc prints whatever Body holds — which is nothing here,
		// because the content comes from BodySource.
		return registry.DryRunDoc("issue.attachment.upload", req), nil
	}

	resp, err := client.Transport.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	// Jira answers with an array of one: the endpoint accepts several files per
	// request even though this command sends one.
	var created []rawAttachment
	if err := json.Unmarshal(resp.Body, &created); err != nil || len(created) == 0 {
		return nil, errs.Remote("MALFORMED_UPLOAD_RESPONSE",
			"%s did not report the attachment it created", req.Path).
			WithRequestID(resp.RequestID).
			WithDetail("the file may be attached; list them before retrying").
			Wrap(err)
	}
	attached, err := created[0].convert()
	if err != nil {
		return nil, err
	}

	return render.Record(KindAttachmentUpload, VersionAttachmentUpload,
		attached.Node().Attr("issue", key)), nil
}
