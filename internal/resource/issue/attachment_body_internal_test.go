//go:build write

// This is the one in-package test file in this resource; every other one is
// `package issue_test`. The seam it needs does not exist from outside, and the
// alternative was reshaping production code — an injectable opener on Client,
// or a package-level func var — purely so an external test could reach a path
// that nothing in the real API can trigger. Bending a test convention is
// cheaper than adding production surface to test an unreachable branch.

package issue

import (
	"errors"
	"io"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/render"
)

// panickingReader is the seam. Nothing in the real body producer can panic —
// CreateFormFile and Close return errors, io.Copy on an *os.File returns
// errors — so the only way to exercise the recover is to hand it a reader that
// does.
type panickingReader struct{ with string }

func (p panickingReader) Read([]byte) (int, error) { panic(p.with) }

// TestAPanicInTheBodyProducerFailsTheUploadNotTheProcess is the assertion the
// card exists for.
//
// A panic in a goroutine is not caught by a recover anywhere else, so the one
// in mcp.Server.dispatch does not cover this and neither would one in
// Command.Run. If this test returns at all, the process survived it.
func TestAPanicInTheBodyProducerFailsTheUploadNotTheProcess(t *testing.T) {
	const secret = "a panic value holding something the peer must never see"

	pr, pw := io.Pipe()
	form := multipart.NewWriter(pw)
	go writeMultipartBody(pw, form, "report.pdf", panickingReader{with: secret})

	// The failure arrives as an error on the body, which is the path every
	// other failure in this producer already takes and the transport already
	// knows how to report.
	_, err := io.ReadAll(pr)
	if err == nil {
		t.Fatal("the body read succeeded despite the producer panicking")
	}

	e := errs.Coerce(err)
	if e.Code != "UPLOAD_BODY_FAILED" {
		t.Fatalf("code = %q, want UPLOAD_BODY_FAILED", e.Code)
	}

	// The panic value is reachable to a developer...
	if !strings.Contains(errors.Unwrap(e).Error(), secret) {
		t.Errorf("the panic value was swallowed: %v", errors.Unwrap(e))
	}

	// ...and reaches the caller nowhere. Under `mcp serve` this error becomes a
	// tool result, so the peer is the caller. Asserted against the whole
	// rendered diagnostic rather than the message field, so moving the value
	// into detail or remedy would still fail.
	var rendered strings.Builder
	if err := render.WriteError(&rendered, e, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(rendered.String(), secret) ||
		strings.Contains(rendered.String(), "goroutine") {
		t.Errorf("the panic value reached the output:\n%s", rendered.String())
	}
}

// TestTheProducerStillWorksAfterOnePanicked is the second half, matching the
// MCP card: asserting only that the panicking call failed would pass against a
// producer that had taken the process down with it.
func TestTheProducerStillWorksAfterOnePanicked(t *testing.T) {
	pr, pw := io.Pipe()
	form := multipart.NewWriter(pw)
	go writeMultipartBody(pw, form, "boom.pdf", panickingReader{with: "boom"})
	if _, err := io.ReadAll(pr); err == nil {
		t.Fatal("the panicking producer did not fail")
	}

	pr2, pw2 := io.Pipe()
	form2 := multipart.NewWriter(pw2)
	go writeMultipartBody(pw2, form2, "report.pdf", strings.NewReader("%PDF-1.4 ok"))

	body, err := io.ReadAll(pr2)
	if err != nil {
		t.Fatalf("the upload after the panic failed: %v", err)
	}
	if !strings.Contains(string(body), "%PDF-1.4 ok") {
		t.Errorf("the body is missing its content:\n%s", body)
	}
	if !strings.Contains(string(body), `filename="report.pdf"`) {
		t.Errorf("the body is missing its filename:\n%s", body)
	}
}
