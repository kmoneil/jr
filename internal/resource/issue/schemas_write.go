//go:build write

package issue

import "github.com/kmoneil/jira-cli/internal/render"

// The mutating verbs' shapes. They are here rather than beside each verb
// because they have far more in common than the verbs do: nearly all of them
// are an acknowledgement naming what was changed and what happened to it.
//
// Jira answers most of these with 204 and no body, so an acknowledgement
// reports what was asked for. Re-reading the issue would be a second request
// whose answer could differ for reasons unrelated to the command.
func init() {
	render.RegisterSchema(KindCreate, CreateSchema())
	render.RegisterSchema(KindClone, CloneSchema())
	render.RegisterSchema(KindEdit, withPrecondition(ackSchema("issue", "key", "edited")))
	render.RegisterSchema(KindDelete, ackSchema("issue", "key", "deleted"))
	render.RegisterSchema(KindMove, MoveSchema())
	render.RegisterSchema(KindAssign, AssignSchema())
	render.RegisterSchema(KindWatch, WatchSchema())

	render.RegisterSchema(KindCommentAdd, withIssue(CommentSchema()))
	render.RegisterSchema(KindCommentEdit, subAckSchema("comment", "edited"))
	render.RegisterSchema(KindCommentDelete, subAckSchema("comment", "deleted"))

	render.RegisterSchema(KindLinkAdd, LinkAddSchema())
	render.RegisterSchema(KindLinkRemove, ackSchema("link", "id", "removed"))

	render.RegisterSchema(KindWorklogAdd, withIssue(WorklogSchema()))
	render.RegisterSchema(KindWorklogDelete, subAckSchema("worklog", "deleted"))
}

// CreateSchema is the shape of a created issue.
func CreateSchema() *render.Schema {
	return &render.Schema{
		Element: "issue",
		Attrs: []render.Field{
			{Name: "key", Type: render.TypeString},
			{Name: "id", Type: render.TypeString},
			// Present only when the result came from the idempotency ledger
			// rather than from Jira. A caller has to be able to tell "I made
			// this" from "this already existed": the second is not a failure,
			// and it is not the same event.
			{Name: "replayed", Type: render.TypeBool, Optional: true},
		},
	}
}

// CloneSchema is CreateSchema plus the issue it was copied from.
func CloneSchema() *render.Schema {
	s := CreateSchema()
	s.Attrs = append(s.Attrs,
		render.Field{Name: "cloned-from", Type: render.TypeString})
	return s
}

// MoveSchema is the shape of a transition that was applied. The destination is
// reported because the transition's name does not say where it leads.
func MoveSchema() *render.Schema {
	s := ackSchema("issue", "key", "moved")
	s.Attrs = append(s.Attrs,
		render.Field{Name: "transition", Type: render.TypeString})
	s.Children = append(s.Children, render.Child{Schema: &render.Schema{
		Element: "to",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeString},
			{Name: "category", Type: render.TypeString},
		},
		Text: &render.Field{Type: render.TypeString},
	}})
	return withPrecondition(s)
}

// AssignSchema echoes the assignee as the caller named it, so the value that
// was sent is visible rather than inferred.
func AssignSchema() *render.Schema {
	s := ackSchema("issue", "key", "assigned")
	s.Children = append(s.Children,
		render.Child{Schema: render.Leaf("assignee", render.TypeString)})
	return withPrecondition(s)
}

// withPrecondition adds the record of an --if-unchanged check.
//
// On the three verbs that accept the flag and nowhere else. A shape that
// declared it everywhere would tell a consumer to look for a check that verb
// cannot perform, and the point of publishing the method is that a caller can
// tell which guarantee they got.
func withPrecondition(s *render.Schema) *render.Schema {
	s.Children = append(s.Children, preconditionChild())
	return s
}

// WatchSchema reports which way the watch went and whose it was.
func WatchSchema() *render.Schema {
	s := ackSchema("issue", "key", "watching", "not-watching")
	s.Children = append(s.Children,
		render.Child{Schema: render.Leaf("watcher", render.TypeString)})
	return s
}

// LinkAddSchema echoes the sentence the caller wrote, so the direction that was
// applied is visible rather than inferred. "Blocks" reads either way, and the
// issue that ends up blocked is the one nobody was watching.
func LinkAddSchema() *render.Schema {
	return &render.Schema{
		Element: "link",
		Attrs: []render.Field{
			{Name: "type", Type: render.TypeString},
			{Name: "action", Type: render.TypeString, Enum: []string{"linked"}},
		},
		Children: []render.Child{
			{Schema: render.Leaf("from", render.TypeString)},
			{Schema: render.Leaf("relationship", render.TypeString)},
			{Schema: render.Leaf("to", render.TypeString)},
		},
	}
}

// ackSchema is the acknowledgement most verbs emit: the thing that changed,
// named by one identifying attribute, plus what happened to it.
//
// The action is an enum of one value on purpose. A consumer branching on it
// should find out from the contract that there is nothing else to branch on.
func ackSchema(element, identity string, actions ...string) *render.Schema {
	return &render.Schema{
		Element: element,
		Attrs: []render.Field{
			{Name: identity, Type: render.TypeString},
			{Name: "action", Type: render.TypeString, Enum: actions},
		},
	}
}

// subAckSchema is the acknowledgement for something that belongs to an issue —
// a comment, a worklog — which is named by its own id and the issue's key.
func subAckSchema(element, action string) *render.Schema {
	s := ackSchema(element, "id", action)
	s.Attrs = append(s.Attrs,
		render.Field{Name: "issue", Type: render.TypeString})
	return s
}

// withIssue adds the issue key to a shape that is otherwise read the same way
// it is written. `issue comment add` returns the comment Jira created, which is
// the comment list's shape plus the issue it landed on.
func withIssue(s *render.Schema) *render.Schema {
	s.Attrs = append(s.Attrs, render.Field{Name: "issue", Type: render.TypeString})
	return s
}
