//go:build write

package registry

import "github.com/kmoneil/jira-cli/internal/render"

// The dry-run kind exists only in a build that has mutating commands, so its
// schema is registered from a file carrying the same tag. Registering it
// unconditionally would publish a shape for a payload a reader build cannot
// produce, which the contract test refuses for good reason.
func init() {
	render.RegisterSchema(KindDryRun, DryRunSchema())
}

// DryRunSchema is the shape of a previewed request.
//
// It is the request itself and not a paraphrase of one, which is why the body
// is text rather than anything structured: what --dry-run promises is the exact
// bytes that would have gone out, pasteable into curl.
func DryRunSchema() *render.Schema {
	return render.ListSchema("requests", "request", &render.Schema{
		Element: "request",
		Attrs: []render.Field{
			{Name: "command", Type: render.TypeString},
			{Name: "method", Type: render.TypeString},
			// Always relative. An absolute path would let a server-supplied
			// value redirect the request, and the credential, to another host.
			{Name: "path", Type: render.TypeString},
		},
		Children: []render.Child{
			{Schema: render.ListSchema("query", "param", &render.Schema{
				Element: "param",
				Attrs:   []render.Field{{Name: "name", Type: render.TypeString}},
				Text:    &render.Field{Type: render.TypeString},
			}), Optional: true},
			{Schema: &render.Schema{
				Element: "body",
				Attrs:   []render.Field{{Name: "content-type", Type: render.TypeString}},
				Text:    &render.Field{Type: render.TypeString},
			}, Optional: true},
		},
	})
}
