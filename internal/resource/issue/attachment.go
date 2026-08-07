package issue

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kinds the attachment reads emit.
const (
	KindAttachmentList    = "issue.attachment.list"
	VersionAttachmentList = 1
	KindAttachmentGet     = "issue.attachment.download"
	VersionAttachmentGet  = 1
)

func init() {
	registry.Register(attachmentListCommand())
	registry.Register(attachmentDownloadCommand())

	render.RegisterSchema(KindAttachmentList, AttachmentSchema())
	render.RegisterSchema(KindAttachmentGet, DownloadSchema())
}

// Attachment is one file on an issue.
type Attachment struct {
	ID       string
	Filename string
	// MimeType is what the server says the bytes are. It is reported and never
	// acted on: this tool does not decide that a file is text and reformat it.
	MimeType string
	Size     int
	Created  string
	Author   string
	// Content is where the bytes live, as the server gave it. On Data Center
	// that is an absolute URL, which is why nothing follows it without first
	// checking it points at the configured site.
	Content string
}

type rawAttachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Size     int    `json:"size"`
	Created  string `json:"created"`
	Content  string `json:"content"`
	Author   *struct {
		DisplayName string `json:"displayName"`
		Name        string `json:"name"`
	} `json:"author"`
}

func (r rawAttachment) convert() (Attachment, error) {
	created, err := normalizeTime("created", r.Created)
	if err != nil {
		return Attachment{}, err
	}
	out := Attachment{
		ID: r.ID, Filename: r.Filename, MimeType: r.MimeType,
		Size: r.Size, Created: created, Content: r.Content,
	}
	if r.Author != nil {
		out.Author = r.Author.DisplayName
		if out.Author == "" {
			out.Author = r.Author.Name
		}
	}
	return out, nil
}

// Node renders one attachment.
//
// The content URL is deliberately not reported. It is a server-supplied
// absolute URL that this tool refuses to follow off-site, and publishing it
// would invite a caller to curl it with their own credentials — which is the
// hazard rather than a convenience.
func (a Attachment) Node() *render.Node {
	return render.El("attachment").
		Attr("id", a.ID).
		Attr("size", strconv.Itoa(a.Size)).
		AttrIf("mime-type", a.MimeType).
		Leaf("filename", a.Filename).
		LeafIf("author", a.Author).
		LeafIf("created", a.Created)
}

// AttachmentSchema is the shape of one attachment.
func AttachmentSchema() *render.Schema {
	return &render.Schema{
		Element: "attachment",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeString},
			// Bytes, as the server reports them. It is what a caller needs to
			// decide whether to download at all.
			{Name: "size", Type: render.TypeInt},
			{Name: "mime-type", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.Leaf("filename", render.TypeString)},
			{Schema: render.Leaf("author", render.TypeString), Optional: true},
			{Schema: render.Leaf("created", render.TypeTimestamp), Optional: true},
		},
	}
}

// AttachmentColumns is the default TSV column set for `issue attachment list`.
func AttachmentColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "filename", Path: "filename"},
		{Header: "size", Path: "@size"},
		{Header: "created", Path: "created"},
	}
}

func attachmentListCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "attachment", "list"},
		Summary: "List the files attached to an issue",
		Description: strings.TrimSpace(`
Returns an issue's attachments, oldest first.

The size is in bytes and is worth reading before downloading: this tool streams
a file to disk rather than into memory, but the network cost is still yours.

The content URL is deliberately not reported. It is an absolute URL the server
supplies, and ` + "`" + buildinfo.App + ` issue attachment download` + "`" + ` is what follows it — after
checking it points at the configured site. Printing it would invite a caller to
fetch it with their own credentials and skip that check.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue attachment list ENG-101",
			buildinfo.App + " issue attachment list ENG-101 --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "attachments",
		Columns:        AttachmentColumns(),
		Outputs: []registry.Output{
			{Kind: KindAttachmentList, Version: VersionAttachmentList},
		},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			return requireIssueKey(inv)
		},
		Stream: runAttachmentList,
	}
}

// Attachments reads an issue's attachments.
//
// They come from the issue rather than from a listing endpoint, because Jira
// has none: attachments are a field. Asking for that one field keeps the
// response small on an issue with a long description.
func (c *Client) Attachments(ctx context.Context, key string) ([]Attachment, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return nil, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}
	path := c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String())

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path,
		Query: url.Values{"fields": {"attachment"}},
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var payload struct {
		Fields struct {
			Attachment []rawAttachment `json:"attachment"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		return nil, errs.Remote("MALFORMED_ATTACHMENTS",
			"%s did not return usable attachments", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}

	out := make([]Attachment, 0, len(payload.Fields.Attachment))
	for _, raw := range payload.Fields.Attachment {
		converted, err := raw.convert()
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

func runAttachmentList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, err := attachmentClientFor(ctx, inv, "issue attachment list")
	if err != nil {
		return registry.StreamResult{}, err
	}

	items, err := client.Attachments(ctx, inv.Args[0])
	if err != nil {
		return registry.StreamResult{}, err
	}

	items, complete := registry.Bound(inv.Limit, items)
	for _, a := range items {
		if err := out.Write(a.Node()); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), out.Count())
	return registry.StreamResult{Complete: complete}, nil
}

// stdoutDestination is what --output takes to mean "the terminal".
const stdoutDestination = "-"

func attachmentDownloadCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "attachment", "download"},
		Summary: "Download an attachment",
		Description: strings.TrimSpace(`
Streams one attachment to a file, or to stdout.

Nothing is held in memory. A 50MB file costs 50MB of disk and a constant amount
of everything else, which is the difference between this and reading a response
the ordinary way.

--output takes a path, or - for stdout. With a path, the result is the usual
document saying what was written and how many bytes. With -, the file *is* the
output and there is no document: a result and a file on the same channel is one
of them corrupting the other, and the caller asked for the file.

--output defaults to the attachment's own filename in the working directory. An
existing file is never overwritten without --force, because a download that
silently replaced a file would be indistinguishable from one that worked.

The two deployments differ in where the bytes live. Cloud serves them from the
REST API. Data Center reports an absolute URL on the attachment itself, and this
follows it only after checking it names the configured site — a server-supplied
URL is not a place to send a credential on trust.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue attachment download ENG-101 10042",
			buildinfo.App + " issue attachment download ENG-101 10042 --output ./report.pdf",
			buildinfo.App + " issue attachment download ENG-101 10042 --output - | file -",
		}, "\n"),
		Args: []registry.Arg{
			{Name: "key", Usage: "issue key, e.g. ENG-101", Required: true},
			{
				Name: "id", Required: true,
				Usage: "attachment id, from `jr issue attachment list`",
			},
		},
		Flags: []registry.Flag{
			{
				// No short flag: -o already means --order on issue list, and a
				// letter that means two things is one a caller has to look up
				// every time.
				Name: "output", Type: registry.TypeString,
				Usage: "where to write it: a path, or - for stdout; " +
					"defaults to the attachment's own filename",
			},
			{
				Name: "force", Type: registry.TypeBool,
				Usage: "overwrite the destination if it already exists",
			},
		},
		NeedsJira: true,
		// Only when the bytes are going to stdout. A download to a file emits
		// its document like anything else.
		OwnsStdoutWhen: downloadOwnsStdout,
		Outputs: []registry.Output{
			{
				Kind: KindAttachmentGet, Version: VersionAttachmentGet,
				When: "--output names a path rather than -",
			},
		},
		ExitCodes: []exitcode.Code{
			exitcode.Auth, exitcode.NotFound, exitcode.Permission,
			exitcode.Conflict, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: validateDownload,
		Run:      runAttachmentDownload,
	}
}

// downloadOwnsStdout reports whether this invocation writes the file to stdout.
func downloadOwnsStdout(inv *registry.Invocation) bool {
	return inv.Flags.String("output") == stdoutDestination
}

func validateDownload(_ context.Context, inv *registry.Invocation) error {
	if err := requireIssueKey(inv); err != nil {
		return err
	}
	if len(inv.Args) < 2 {
		return errs.Usage("INVALID_ATTACHMENT_ID", "an attachment id is required")
	}
	if err := validNumericID("attachment", inv.Args[1]); err != nil {
		return err
	}
	if downloadOwnsStdout(inv) && inv.Stdout == nil {
		// Inside `mcp serve` stdout carries JSON-RPC frames, and a file written
		// there is a frame the peer cannot parse. This has been shipped once
		// already; it is not shipping again.
		return errs.Usage("NO_STDOUT",
			"this caller has no stdout to write a file to").
			WithRemedy("pass --output <path> instead of -")
	}
	return nil
}

// Downloaded is what a download produced.
type Downloaded struct {
	ID       string
	Issue    string
	Filename string
	// Path is where it was written, or "-" for stdout.
	Path  string
	Bytes int64
}

// Node renders the acknowledgement of a download.
func (d Downloaded) Node() *render.Node {
	return render.El("attachment").
		Attr("id", d.ID).
		Attr("issue", d.Issue).
		Attr("bytes", strconv.FormatInt(d.Bytes, 10)).
		Leaf("filename", d.Filename).
		Leaf("path", d.Path)
}

// DownloadSchema is the shape of that acknowledgement.
func DownloadSchema() *render.Schema {
	return &render.Schema{
		Element: "attachment",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeString},
			{Name: "issue", Type: render.TypeString},
			// What was actually written, counted while writing. It is not the
			// size the listing reported: if the two disagree, this is the one
			// that happened.
			{Name: "bytes", Type: render.TypeInt},
		},
		Children: []render.Child{
			{Schema: render.Leaf("filename", render.TypeString)},
			{Schema: render.Leaf("path", render.TypeString)},
		},
	}
}

// ContentPath resolves where an attachment's bytes are, per deployment.
//
// Cloud serves them from the REST API under a path this tool builds. Data
// Center has no such endpoint and reports an absolute URL on the attachment
// instead, so that URL is checked against the configured site before anything
// follows it — the value comes from the server, and following it blind is how a
// credential reaches a host nobody chose.
func (c *Client) ContentPath(ctx context.Context, id string) (string, string, error) {
	if c.Site.Kind == site.Cloud {
		return c.Site.APIBase() + "/attachment/content/" + url.PathEscape(id), "", nil
	}

	meta, err := c.attachmentMeta(ctx, id)
	if err != nil {
		return "", "", err
	}
	relative, err := c.relative(meta.Content)
	if err != nil {
		return "", "", err
	}
	return relative, meta.Filename, nil
}

// attachmentMeta reads one attachment's metadata, which is where Data Center
// keeps the content URL.
func (c *Client) attachmentMeta(ctx context.Context, id string) (Attachment, error) {
	if err := validNumericID("attachment", id); err != nil {
		return Attachment{}, err
	}
	path := c.Site.APIBase() + "/attachment/" + url.PathEscape(id)

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path,
	})
	if err != nil {
		return Attachment{}, err
	}
	if err := transport.Err(resp); err != nil {
		return Attachment{}, err
	}

	var raw rawAttachment
	if err := json.Unmarshal(resp.Body, &raw); err != nil || raw.ID == "" {
		return Attachment{}, errs.Remote("MALFORMED_ATTACHMENT",
			"the response for attachment %s is not a usable attachment", id).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	return raw.convert()
}

func runAttachmentDownload(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := attachmentClientFor(ctx, inv, "issue attachment download")
	if err != nil {
		return nil, err
	}
	key, id := inv.Args[0], inv.Args[1]

	path, filename, err := client.ContentPath(ctx, id)
	if err != nil {
		return nil, err
	}

	resp, err := client.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path, Stream: true,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Close()
	if err := transport.Err(resp); err != nil {
		return nil, err
	}
	if filename == "" {
		filename = filenameFrom(resp, id)
	}

	dest := inv.Flags.String("output")
	if dest == "" {
		// The server named the file, so the server does not also get to name
		// the directory. Checked here rather than where the filename is read,
		// because the value is only dangerous at the moment it becomes a path:
		// reporting it is what `attachment list` already does, and refusing to
		// report a name Jira really stored would be this tool hiding data.
		if err := usableAsDestination(filename); err != nil {
			return nil, err
		}
		dest = filename
	}

	if dest == stdoutDestination {
		// No document: the file is the output. The CLI knows not to render one
		// because OwnsStdoutWhen said so before this ran.
		if _, err := io.Copy(inv.Stdout, resp.Stream); err != nil {
			return nil, errs.Runtime("DOWNLOAD_FAILED",
				"the attachment could not be written to stdout").Wrap(err)
		}
		return nil, nil //nolint:nilnil // the bytes are the result.
	}

	written, err := writeFile(dest, resp.Stream, inv.Flags.Bool("force"))
	if err != nil {
		return nil, err
	}
	return render.Record(KindAttachmentGet, VersionAttachmentGet, Downloaded{
		ID: id, Issue: key, Filename: filename, Path: dest, Bytes: written,
	}.Node()), nil
}

// usableAsDestination reports whether a filename this tool did not choose can
// be the path it writes to.
//
// A filename is one path element. `../../../../home/you/.bashrc` is a filename
// only in the sense that it parses as one, and joining it to the working
// directory is how an attachment lands somewhere nobody asked for.
//
// The rule was already written down one function below, in filenameFrom: a
// server-supplied name containing a directory separator is not a suggestion
// this tool acts on. It was enforced only on the path Cloud takes. Data Center
// reports a filename on the attachment itself and reaches here with the raw
// value, so the same sentence had to hold in two places and held in one.
//
// Refusing rather than reducing to the base name is the call offSite makes
// about a server-supplied URL, for the same reason: repairing the value is this
// tool deciding what the server meant. Here the caller has a better answer than
// either — --output, which names the destination without guessing.
//
// filepath.IsLocal carries the platform's own rules, which is why it is used
// rather than a scan for "..": it refuses an absolute path, a parent reference,
// and on Windows the reserved device names. The two extra cases are this tool's
// own, not the filesystem's — "." is a directory, and "-" is the flag value
// meaning stdout.
func usableAsDestination(name string) error {
	if filepath.IsLocal(name) && filepath.Base(name) == name &&
		name != "." && name != stdoutDestination {
		return nil
	}
	return errs.Runtime("UNSAFE_FILENAME",
		"the server named a file this tool will not write to").
		WithDetail("%q is not a filename", name).
		WithRemedy("pass --output <path> to choose the destination yourself")
}

// writeFile streams to disk, refusing to replace what is already there.
//
// A download that silently overwrote a file would be indistinguishable from one
// that worked, and the file it replaced is not recoverable.
func writeFile(dest string, src io.Reader, force bool) (int64, error) {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}

	f, err := os.OpenFile(dest, flags, 0o600) //nolint:gosec // the caller named the path.
	if err != nil {
		if os.IsExist(err) {
			return 0, errs.New(exitcode.Conflict, "DESTINATION_EXISTS",
				"%s already exists", dest).
				WithRemedy("pass --force to replace it, or --output <path> to " +
					"write somewhere else")
		}
		return 0, errs.Runtime("DOWNLOAD_FAILED", "cannot write %s", dest).Wrap(err)
	}

	written, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if copyErr != nil {
		// A partial file is worse than none: it looks like a download that
		// worked. Remove it and say what happened.
		_ = os.Remove(dest)
		return 0, errs.Runtime("DOWNLOAD_FAILED",
			"the attachment was not written whole; %s has been removed", dest).
			Wrap(copyErr)
	}
	if closeErr != nil {
		return 0, errs.Runtime("DOWNLOAD_FAILED", "cannot finish writing %s", dest).
			Wrap(closeErr)
	}
	return written, nil
}

// filenameFrom recovers a name for the file when the deployment did not supply
// one with the content URL.
//
// Content-Disposition is the server's own answer and is preferred. Anything
// taken from it is reduced to its base name: a filename is not a path, and a
// server-supplied one containing a directory separator is not a suggestion this
// tool acts on.
func filenameFrom(resp *transport.Response, id string) string {
	if disposition := resp.Header.Get("Content-Disposition"); disposition != "" {
		if _, params, err := mime.ParseMediaType(disposition); err == nil {
			if name := strings.TrimSpace(params["filename"]); name != "" {
				if base := filepath.Base(name); base != "." && base != string(filepath.Separator) {
					return base
				}
			}
		}
	}
	return "attachment-" + id
}

// relative is the transport's site check, reached through the interface this
// resource is given. A Doer that is not a *transport.Client cannot answer it,
// which is only the case in a test that never downloads.
func (c *Client) relative(raw string) (string, error) {
	relater, ok := c.Transport.(interface {
		Relative(string) (string, error)
	})
	if !ok {
		return "", errs.Runtime("NO_TRANSPORT",
			"this client cannot check where a server-supplied URL points")
	}
	return relater.Relative(raw)
}

// validNumericID rejects an id this tool cannot address.
//
// Jira's ids are numeric strings. Checking locally means a typo costs no round
// trip, and it keeps a caller's argument from reaching a URL path as anything
// but digits.
//
// It lives here, untagged, rather than beside the write verbs that used it
// first: `attachment download` is a read and needs the same check, and a
// reader build has to contain it.
func validNumericID(what, id string) error {
	code := "INVALID_" + strings.ToUpper(what) + "_ID"
	if id == "" {
		return errs.Usage(code, "a %s id is required", what)
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return errs.Usage(code, "%q is not a %s id", id, what).
				WithDetail("an id is digits, e.g. 10042").
				WithRemedy("take it from `%s issue %s list`", buildinfo.App, what)
		}
	}
	return nil
}

// attachmentClientFor is the opening the attachment reads share.
func attachmentClientFor(
	ctx context.Context, inv *registry.Invocation, command string,
) (*Client, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "%s has no connection to Jira", command)
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{Transport: conn, Site: info}, nil
}
