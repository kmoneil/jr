package cli

import (
	"github.com/kmoneil/jr/internal/auth"
	"github.com/kmoneil/jr/internal/render"
)

// The built-in commands own five kinds between them. Their schemas live here
// rather than beside each builder because the builders are spread across three
// files and a reader looking for "what does auth.status look like" should find
// all five in one place.
func init() {
	render.RegisterSchema(kindVersion, versionSchema())
	render.RegisterSchema(kindAuthStatus, authStatusSchema())
	render.RegisterSchema(kindAuthToken, authTokenSchema())
	render.RegisterSchema(kindContextList, contextListSchema())
	render.RegisterSchema(kindContextGet, contextGetSchema())
}

// versionSchema is the shape of `jr version`: what this binary is and what it
// can do, which is a compile-time constant.
func versionSchema() *render.Schema {
	return &render.Schema{
		Element: "version",
		Attrs: []render.Field{
			{Name: "app", Type: render.TypeString},
			{Name: "release", Type: render.TypeString},
			{Name: "profile", Type: render.TypeString},
			// Whether the mutating commands are in this binary at all. It is a
			// fact about the build, not a permission that could change.
			{Name: "writable", Type: render.TypeBool},
			{Name: "interactive", Type: render.TypeBool},
		},
		Children: []render.Child{
			{Schema: render.Leaf("display", render.TypeString)},
			{Schema: render.Leaf("commit", render.TypeString)},
			{Schema: render.Leaf("built", render.TypeString)},
			{Schema: render.Leaf("go", render.TypeString)},
			{Schema: render.Leaf("platform", render.TypeString)},
			{Schema: render.ListSchema("tags", "tag", &render.Schema{
				Element: "tag",
				Attrs:   []render.Field{{Name: "name", Type: render.TypeString}},
				Text:    &render.Field{Type: render.TypeString},
			})},
		},
	}
}

// authStatusSchema is the shape of a credential described without being
// revealed. Nothing here can carry the secret: the value never reaches the
// node builder.
func authStatusSchema() *render.Schema {
	return &render.Schema{
		Element: "auth",
		Attrs: []render.Field{
			{Name: "site", Type: render.TypeString},
			{Name: "authenticated", Type: render.TypeBool},
			// The four below are present only when a credential was found.
			{Name: "scheme", Type: render.TypeString, Optional: true, Enum: auth.Schemes()},
			{Name: "source", Type: render.TypeString, Optional: true},
			// Whether the source is keyed by host. False means the credential
			// was not looked up for this site, it was merely the one found:
			// JIRA_API_TOKEN is read whatever site the invocation resolves,
			// deliberately, so that a CI job need not also name the host. See
			// auth.EnvProvider.Lookup for why that is not being changed.
			{Name: "site-scoped", Type: render.TypeBool, Optional: true},
			{Name: "user", Type: render.TypeString, Optional: true},
			// `auth logout` reuses this kind to say what it removed. It is the
			// same fact — which credential a site would use — reported after
			// the removal rather than before.
			{Name: "removed", Type: render.TypeBool, Optional: true},
			// `auth login` names the context it created, for the one case where
			// storing a credential is also the act of configuring the tool.
			// Storing one and then reporting "no site configured" would be the
			// tool ignoring what it was told.
			{Name: "context", Type: render.TypeString, Optional: true},
			// Who the credential authenticates as, when `auth login` verified
			// it against the site rather than only storing it. Present
			// together or not at all.
			{Name: "account", Type: render.TypeString, Optional: true},
			{Name: "display", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			// Everywhere that was searched, in order, so a caller can see
			// which one answered and which were skipped.
			{Schema: render.ListSchema("sources", "source",
				render.Leaf("source", render.TypeString))},
		},
	}
}

// authTokenSchema is the shape of the one command that does reveal a
// credential, because printing it is what it is for.
func authTokenSchema() *render.Schema {
	return &render.Schema{
		Element: "token",
		Attrs: []render.Field{
			{Name: "site", Type: render.TypeString},
			{Name: "scheme", Type: render.TypeString, Enum: auth.Schemes()},
			{Name: "source", Type: render.TypeString},
		},
		Children: []render.Child{
			{Schema: render.Leaf("authorization", render.TypeString)},
		},
	}
}

// contextListSchema is the shape of one stored context.
func contextListSchema() *render.Schema { return contextSchema(false) }

// contextGetSchema is the shape of one context in full: the stored one, or the
// effective settings for this invocation, which is why config and effective
// appear here and not in the listing.
func contextGetSchema() *render.Schema { return contextSchema(true) }

func contextSchema(resolved bool) *render.Schema {
	s := &render.Schema{
		Element: "context",
		Attrs: []render.Field{
			{Name: "name", Type: render.TypeString},
			{Name: "current", Type: render.TypeBool},
			{Name: "site", Type: render.TypeString},
			// Present and empty when the context sets none: project is never
			// mandatory, and "no default" is a fact worth stating.
			{Name: "project", Type: render.TypeString},
			{Name: "board", Type: render.TypeString},
			// A one-way latch. Nothing in the output turns it off.
			{Name: "readonly", Type: render.TypeBool},
			{Name: "credential", Type: render.TypeString},
		},
		Children: []render.Child{
			{Schema: render.ListSchema("fields", "field",
				render.Leaf("field", render.TypeString))},
		},
	}
	if resolved {
		s.Attrs = append(s.Attrs,
			render.Field{Name: "effective", Type: render.TypeBool, Optional: true},
			// `context delete` reuses this kind to report what it removed.
			render.Field{Name: "deleted", Type: render.TypeBool, Optional: true},
			// Present only when --api-version or JIRA_API_VERSION forced one.
			// Absent means the deployment probe decided, which is the case
			// worth saying nothing about.
			render.Field{Name: "api-version", Type: render.TypeInt, Optional: true})
		s.Children = append(s.Children,
			render.Child{Schema: render.Leaf("config", render.TypeString)})
	}
	return s
}
