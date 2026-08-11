//go:build write

package issue_test

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// An upload is the one thing here a cassette cannot express. The multipart
// boundary is random per request, so the body is not reproducible and matching
// on it is impossible — and matching on everything *but* the body would assert
// nothing about the part that matters.
//
// So these run against a local server that parses the form. That is a stronger
// assertion than byte-matching would have been: it checks the file arrived as a
// file, under the right name, with the header Jira refuses the request without.
// Nothing leaves the machine, which is the rule the cassettes exist to keep.

const uploadResponse = `[{"id":"10044","filename":"run-42.log","mimeType":"text/plain",` +
	`"size":13,"created":"2026-08-06T12:00:00.000+0000",` +
	`"content":"https://recorded.invalid/secure/attachment/10044/run-42.log",` +
	`"author":{"displayName":"Ada Lovelace"}}]`

// received is what the server made of one upload.
type received struct {
	xsrf     string
	field    string
	filename string
	content  string
}

// uploadServer accepts a multipart upload and records what it actually got.
// failFirst answers the first request with a 429, which is refused before
// processing and therefore retried whatever the method.
func uploadServer(t *testing.T, got *atomic.Pointer[received], failFirst bool) *httptest.Server {
	t.Helper()
	var attempts atomic.Int32

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failFirst && attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}

		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(part)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		got.Store(&received{
			xsrf:     r.Header.Get("X-Atlassian-Token"),
			field:    part.FormName(),
			filename: part.FileName(),
			content:  string(body),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, uploadResponse)
	}))
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func uploadClient(t *testing.T, srv *httptest.Server) *transport.Client {
	t.Helper()
	conn, err := transport.New(transport.Options{
		BaseURL: srv.URL, Retries: 2, Jitter: func() float64 { return 0 },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return conn
}

// TestUploadSendsTheFileAsMultipart covers the shape Jira requires, including
// the header it refuses the request without.
func TestUploadSendsTheFileAsMultipart(t *testing.T) {
	var got atomic.Pointer[received]
	srv := uploadServer(t, &got, false)
	defer srv.Close()

	path := writeTempFile(t, "run.log", "hello, attach")
	doc := runUpload(t, srv, []string{"ENG-101", path}, registry.NewFlags())

	sent := got.Load()
	if sent == nil {
		t.Fatal("the server saw no upload")
	}
	if sent.xsrf != "no-check" {
		t.Errorf("X-Atlassian-Token = %q; Jira refuses the request without it", sent.xsrf)
	}
	if sent.field != "file" {
		t.Errorf("form field = %q, want file", sent.field)
	}
	if sent.filename != "run.log" {
		t.Errorf("filename = %q, want the file's own base name", sent.filename)
	}
	if sent.content != "hello, attach" {
		t.Errorf("content = %q", sent.content)
	}

	// The result is the attachment Jira created, which is the listing's shape
	// plus the issue it landed on.
	if id, _ := doc.Record.AttrValue("id"); id != "10044" {
		t.Errorf("id = %q", id)
	}
	if issueKey, _ := doc.Record.AttrValue("issue"); issueKey != "ENG-101" {
		t.Errorf("issue = %q", issueKey)
	}
}

// TestUploadReSendsTheWholeFileOnARetry is why the body is a factory. A 429 is
// refused before processing and retried whatever the method, so an upload has
// to survive one — and without re-opening, the second attempt would send an
// empty form that Jira accepts as a zero-byte file.
func TestUploadReSendsTheWholeFileOnARetry(t *testing.T) {
	var got atomic.Pointer[received]
	srv := uploadServer(t, &got, true)
	defer srv.Close()

	path := writeTempFile(t, "run.log", "hello, attach")
	runUpload(t, srv, []string{"ENG-101", path}, registry.NewFlags())

	sent := got.Load()
	if sent == nil {
		t.Fatal("the retry never reached the server")
	}
	if sent.content != "hello, attach" {
		t.Errorf("the retry sent %q, want the whole file again", sent.content)
	}
}

// TestUploadNameOverridesTheFilename covers the only way to attach something
// under a name other than the one on disk.
func TestUploadNameOverridesTheFilename(t *testing.T) {
	var got atomic.Pointer[received]
	srv := uploadServer(t, &got, false)
	defer srv.Close()

	flags := registry.NewFlags()
	flags.SetString("name", "run-42.log")
	runUpload(t, srv, []string{"ENG-101", writeTempFile(t, "run.log", "x")}, flags)

	if sent := got.Load(); sent == nil || sent.filename != "run-42.log" {
		t.Errorf("filename = %+v, want the override", sent)
	}
}

// TestUploadRefusesWhatItCannotSend covers the checks that run before anything
// goes out, so the common mistakes cost no round trip.
func TestUploadRefusesWhatItCannotSend(t *testing.T) {
	cmd, ok := registry.Lookup("issue.attachment.upload")
	if !ok {
		t.Fatal("issue attachment upload is not registered")
	}
	dir := t.TempDir()
	existing := writeTempFile(t, "ok.txt", "x")

	for _, tc := range []struct {
		name string
		args []string
		flag string
		code string
	}{
		{"no such file", []string{"ENG-101", filepath.Join(dir, "missing")}, "", "NO_SUCH_FILE"},
		{"a directory", []string{"ENG-101", dir}, "", "NOT_A_FILE"},
		{"not an issue key", []string{"nope", existing}, "", "INVALID_KEY"},
		// A name is not a path. Jira would store the separator verbatim and
		// hand back a filename nothing can open.
		{"a name with a path in it", []string{"ENG-101", existing}, "../etc/passwd", "INVALID_NAME"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := registry.NewFlags()
			if tc.flag != "" {
				flags.SetString("name", tc.flag)
			}
			err := cmd.Validate(t.Context(), &registry.Invocation{
				Args: tc.args, Flags: flags, Progress: registry.NoProgress,
			})
			if err == nil {
				t.Fatal("it was accepted")
			}
			if code := errs.Coerce(err).Code; code != tc.code {
				t.Errorf("code = %q, want %q", code, tc.code)
			}
		})
	}
}

// TestUploadDryRunPrintsNoFileContents covers the one dry run whose body is not
// worth printing. --dry-run promises the exact request; here the request is a
// file, and DryRunDoc renders Body, which an upload deliberately leaves empty.
func TestUploadDryRunPrintsNoFileContents(t *testing.T) {
	var got atomic.Pointer[received]
	srv := uploadServer(t, &got, false)
	defer srv.Close()

	flags := registry.NewFlags()
	flags.SetBool("dry-run", true)
	path := writeTempFile(t, "secret.txt", "TOP SECRET CONTENTS")
	doc := runUpload(t, srv, []string{"ENG-101", path}, flags)

	if doc.Kind != registry.DryRunOutput().Kind {
		t.Errorf("kind = %q, want the dry-run kind", doc.Kind)
	}
	var buf strings.Builder
	if err := render.Write(&buf, doc, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(buf.String(), "TOP SECRET") {
		t.Errorf("the dry run printed the file:\n%s", buf.String())
	}
	if got.Load() != nil {
		t.Error("a dry run sent the upload")
	}
}

// TestUploadIsDeclaredForWhatItDoes is what the CLI enforces read-only mode and
// the tag gate from.
func TestUploadIsDeclaredForWhatItDoes(t *testing.T) {
	cmd, _ := registry.Lookup("issue.attachment.upload")
	if !cmd.Mutating {
		t.Error("upload is not marked mutating, so read-only would not refuse it")
	}
	if cmd.Destructive {
		t.Error("upload is marked destructive; attaching a file removes nothing")
	}
	if len(cmd.RequiresTags) != 1 || cmd.RequiresTags[0] != "write" {
		t.Errorf("requires %v, want just write", cmd.RequiresTags)
	}
}

func runUpload(
	t *testing.T, srv *httptest.Server, args []string, flags registry.Flags,
) *render.Doc {
	t.Helper()

	cmd, ok := registry.Lookup("issue.attachment.upload")
	if !ok {
		t.Fatal("issue attachment upload is not registered")
	}
	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: uploadClient(t, srv), kind: site.DataCenter},
		Args: args, Flags: flags,
		Stderr: io.Discard, Stdout: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	return doc
}
