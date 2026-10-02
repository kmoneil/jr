//go:build write

package workflow

import (
	"net/url"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/transport"
)

// RequestsOf reads a dry-run document back into the requests it renders: the
// inverse of registry.DryRunDoc. It lives with its one caller, the sequence,
// rather than beside DryRunDoc: it composes no request of its own, and this
// package's recordings are the ones behind every request it reads back.
//
// It exists for a caller that plans by dry-running a command and sends later
// exactly what the dry run said, which is how a sequence step records its
// resolved change: the transition id, the field ids, the assignee's account,
// all as the command resolved them, in the request it would have sent.
// Reading the document, rather than asking every command for its requests a
// second way, keeps one rendering of a request, and
// TestRequestsOfReadsBackWhatDryRunDocWrote holds the two directions to each
// other.
//
// The headers come back as the one a dry run implies, the JSON content type
// on a request with a body, because no mutating command sets another and the
// transport adds the rest.
func RequestsOf(doc *render.Doc) ([]transport.Request, error) {
	if doc == nil || doc.Kind != registry.KindDryRun || doc.Record == nil ||
		doc.Record.Name != "requests" {
		return nil, notADryRun(doc)
	}
	list := doc.Record
	out := make([]transport.Request, 0, len(list.Children))
	for _, n := range list.Children {
		method, _ := n.AttrValue("method")
		path, _ := n.AttrValue("path")
		if method == "" || path == "" {
			return nil, errs.Runtime("MALFORMED_DRY_RUN",
				"a dry-run request carries no method or no path")
		}
		r := transport.Request{Method: method, Path: path}
		if query, ok := n.ChildNamed("query"); ok {
			r.Query = url.Values{}
			for _, p := range query.Children {
				name, _ := p.AttrValue("name")
				r.Query.Add(name, p.Text)
			}
		}
		if body, ok := n.ChildNamed("body"); ok {
			r.Body = []byte(body.Text)
			contentType, _ := body.AttrValue("content-type")
			r.Header = map[string][]string{"Content-Type": {contentType}}
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, errs.Runtime("MALFORMED_DRY_RUN", "a dry run that would send nothing")
	}
	return out, nil
}

func notADryRun(doc *render.Doc) error {
	kind := "nothing"
	if doc != nil {
		kind = doc.Kind
	}
	return errs.Runtime("MALFORMED_DRY_RUN",
		"a command asked for its dry run answered with %s", kind)
}
