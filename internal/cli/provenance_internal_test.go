package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestTheDocumentEnvelopeGetsTheSessionsSite covers the seam between the
// session and the writer.
//
// The writer tests prove a Doc carrying a site renders one, and the session
// knows its site. Neither says the value gets from one to the other, and it is
// one assignment in each of two paths — a document and a stream. Testing the
// two ends and reviewing the middle is how `--order` shipped: declared, bound,
// harvested, passed along, and dropped by the layer nobody drove.
func TestTheDocumentEnvelopeGetsTheSessionsSite(t *testing.T) {
	const want = "https://provenance.invalid/jira"

	var out strings.Builder
	a := &app{stdout: &out, stderr: &strings.Builder{}}
	inv := &registry.Invocation{Jira: siteSession(want)}

	rc := &registry.Command{
		Path:    []string{"probe"},
		Summary: "a command that returns a record",
		Outputs: []registry.Output{{Kind: "probe", Version: 1}},
		Run: func(context.Context, *registry.Invocation) (*render.Doc, error) {
			return render.Record("probe", 1, render.El("probe")), nil
		},
	}

	if err := a.runDocument(t.Context(), rc, inv); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), want) {
		t.Errorf("the document does not name the session's site:\n%s", out.String())
	}
}

// TestTheStreamSpecGetsTheSessionsSite is the other path to stdout.
//
// A streaming command never builds a Doc of its own; the spec becomes the
// envelope inside Stream.Close. So the site has to be on the spec, and a
// document path that worked would say nothing about it.
func TestTheStreamSpecGetsTheSessionsSite(t *testing.T) {
	const want = "https://streamed.invalid"

	rc := &registry.Command{
		Path:           []string{"probe"},
		CollectionName: "probes",
		Columns:        []render.Column{{Header: "key", Path: "@key"}},
		Outputs:        []registry.Output{{Kind: "probe.list", Version: 1}},
	}
	spec := streamSpec(rc, &registry.Invocation{
		Jira:  siteSession(want),
		Flags: registry.NewFlags(),
	})
	if spec.Site != want {
		t.Errorf("stream spec site = %q, want %q", spec.Site, want)
	}
}

// TestACommandWithNoJiraNamesNoSite keeps the attribute honest.
//
// `jr version` and `jr context list` reach no Jira, and an envelope claiming
// one would be worse than the silence this card was written about. siteOf takes
// a nil invocation too, because a caller that has to check first is a caller
// that will one day forget.
func TestACommandWithNoJiraNamesNoSite(t *testing.T) {
	if got := siteOf(nil); got != "" {
		t.Errorf("siteOf(nil) = %q, want empty", got)
	}
	if got := siteOf(&registry.Invocation{}); got != "" {
		t.Errorf("siteOf with no session = %q, want empty", got)
	}
}

// siteSession is a registry.Session that knows nothing but where it points,
// which is all provenance needs from it.
type siteSession string

func (s siteSession) Site() string { return string(s) }

func (siteSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return nil, site.Info{}, errUnreachable
}

func (siteSession) Metadata(context.Context) (*site.Metadata, error) {
	return nil, errUnreachable
}
func (siteSession) Project() string                 { return "" }
func (siteSession) RequireProject() (string, error) { return "", errUnreachable }
func (siteSession) Board() string                   { return "" }
func (siteSession) RequireBoard() (string, error)   { return "", errUnreachable }
func (siteSession) Fields() []string                { return nil }
func (siteSession) CheckWritable(string) error      { return nil }
func (siteSession) Idempotency() *idem.Ledger       { return nil }

// errUnreachable is what every other method answers. Provenance is resolved
// without connecting, so a session that cannot connect at all still names its
// site — and a test that needed a working connection to prove that would be
// proving something else.
var errUnreachable = errs.Runtime("NO_SESSION", "this session reaches nothing")
